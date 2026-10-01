/*
 * Copyright 2026 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package prometheus

// The TSM finalizers as they read every series through Points, kept as the oracle for the columnar ones.

import (
	"cmp"
	"container/heap"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type legacyLimitKLogicalSeries struct {
	group   string
	members []*dataset.Series
}

func legacyFinalizeLimitKAggregation(ds *dataset.DataSet, spec promql.LimitKAggregation) {
	isRangeQuery := ds.Step() > 0
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if legacyDataSetContainsFinalizerInput(ds, spec.Inner) {
		for _, result := range ds.Results {
			if result != nil {
				legacyFinalizeLimitKResult(result, spec)
			}
		}
	}

	if !spec.SortSet {
		return
	}
	if isRangeQuery {
		legacyAppendWarningOnce(ds, legacySortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		// PromQL sort functions ignore native-histogram samples even though
		// limitk itself retains them.
		result.SeriesList = legacyFilterHistogramSeries(result.SeriesList)
		if !isRangeQuery {
			legacySortInstantSeries(result.SeriesList, spec.SortDescending)
		}
	}
}

func legacyFinalizeLimitKResult(result *dataset.Result, spec promql.LimitKAggregation) {
	if spec.K < 1 {
		result.SeriesList = result.SeriesList[:0]
		return
	}

	result.SeriesList.SortByTags()
	logicalOrder := make([]*legacyLimitKLogicalSeries, 0, len(result.SeriesList))
	lastTagsKey := ""
	for _, series := range result.SeriesList {
		if series == nil || series.PointCount() == 0 {
			continue
		}
		tagsKey := series.Header.Tags.JSON()
		if len(logicalOrder) == 0 || tagsKey != lastTagsKey {
			logicalOrder = append(logicalOrder, &legacyLimitKLogicalSeries{
				group: legacyRankGroupKey(series.Header.Tags, spec.Grouping),
			})
			lastTagsKey = tagsKey
		}
		last := logicalOrder[len(logicalOrder)-1]
		last.members = append(last.members, series)
	}

	selectedCounts := make(map[legacyRankBucketKey]int64)
	for _, logical := range logicalOrder {
		legacySelectLimitKLogicalPoints(logical, spec.K, selectedCounts)
	}
	kept := result.SeriesList[:0]
	for _, series := range result.SeriesList {
		if series != nil && series.PointCount() > 0 {
			kept = append(kept, series)
		}
	}
	result.SeriesList = kept
}

func legacySelectLimitKLogicalPoints(logical *legacyLimitKLogicalSeries, k int64,
	selectedCounts map[legacyRankBucketKey]int64,
) {
	indexes := make([]int, len(logical.members))
	points := make([]dataset.Points, len(logical.members))
	kept := make([]dataset.Points, len(logical.members))
	for i, series := range logical.members {
		points[i] = dspoints.Of(series)
		kept[i] = points[i][:0]
	}

	for {
		var pointEpoch epoch.Epoch
		found := false
		for i := range logical.members {
			if indexes[i] >= len(points[i]) {
				continue
			}
			candidateEpoch := points[i][indexes[i]].Epoch
			if !found || candidateEpoch < pointEpoch {
				pointEpoch = candidateEpoch
				found = true
			}
		}
		if !found {
			break
		}

		bucket := legacyRankBucketKey{epoch: int64(pointEpoch), group: logical.group}
		selected := selectedCounts[bucket] < k
		if selected {
			selectedCounts[bucket]++
		}
		for i := range logical.members {
			for indexes[i] < len(points[i]) &&
				points[i][indexes[i]].Epoch == pointEpoch {
				point := points[i][indexes[i]]
				if selected {
					kept[i] = append(kept[i], point)
				}
				indexes[i]++
			}
		}
	}

	for i, series := range logical.members {
		series.SetPoints(kept[i])
	}
}

const legacyInvalidQuantileParameterWarning = "PromQL warning: quantile parameter is NaN or outside [0, 1]"

type legacyQuantileFinalizeGroup struct {
	header dataset.SeriesHeader
	points dataset.Points
}

type legacyQuantileCursor struct {
	points     dataset.Points
	pointIndex int
	groupKey   string
	order      int
}

type legacyQuantileCursorHeap []*legacyQuantileCursor

func (h legacyQuantileCursorHeap) Len() int { return len(h) }

func (h legacyQuantileCursorHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	aEpoch := a.points[a.pointIndex].Epoch
	bEpoch := b.points[b.pointIndex].Epoch
	if aEpoch != bEpoch {
		return aEpoch < bEpoch
	}
	return a.order < b.order
}

func (h legacyQuantileCursorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *legacyQuantileCursorHeap) Push(value any) {
	*h = append(*h, value.(*legacyQuantileCursor))
}

func (h *legacyQuantileCursorHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func legacyFinalizeQuantileAggregation(ds *dataset.DataSet, spec promql.QuantileAggregation) {
	isRangeQuery := ds.Step() > 0
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if legacyDataSetContainsFinalizerInput(ds, spec.Inner) {
		if math.IsNaN(spec.Phi) || spec.Phi < 0 || spec.Phi > 1 {
			legacyAppendWarningOnce(ds, legacyInvalidQuantileParameterWarning)
		}
		for _, result := range ds.Results {
			if result != nil {
				legacyFinalizeQuantileResult(result, spec)
			}
		}
	}

	if !spec.SortSet {
		return
	}
	if isRangeQuery {
		legacyAppendWarningOnce(ds, legacySortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result != nil && !isRangeQuery {
			legacySortInstantSeries(result.SeriesList, spec.SortDescending)
		}
	}
}

func legacyFinalizeQuantileResult(result *dataset.Result, spec promql.QuantileAggregation) {
	groups := make(map[string]*legacyQuantileFinalizeGroup)
	groupOrder := make([]string, 0)
	cursors := make(legacyQuantileCursorHeap, 0, len(result.SeriesList))

	for order, series := range result.SeriesList {
		if series == nil || legacyIsHistogramSeries(series) || series.PointCount() == 0 {
			continue
		}
		tags := legacyAggregationGroupingTags(series.Header.Tags, spec.Grouping)
		key := tags.JSON()
		if groups[key] == nil {
			header := series.Header.Clone()
			header.Tags = tags
			header.Name = tags[promql.MetricNameLabel]
			header.TagFieldsList = nil
			header.QueryStatement = spec.AggregationQuery
			header.CalculateHash(true)
			header.CalculateSize()
			groups[key] = &legacyQuantileFinalizeGroup{header: header}
			groupOrder = append(groupOrder, key)
		}
		heap.Push(&cursors, &legacyQuantileCursor{points: dspoints.Of(series), groupKey: key, order: order})
	}

	for len(cursors) > 0 {
		pointEpoch := cursors[0].points[cursors[0].pointIndex].Epoch
		valuesByGroup := make(map[string][]float64)
		for len(cursors) > 0 &&
			cursors[0].points[cursors[0].pointIndex].Epoch == pointEpoch {
			cursor := heap.Pop(&cursors).(*legacyQuantileCursor)
			point := cursor.points[cursor.pointIndex]
			if value, ok := legacyVariancePointFloat(point); ok {
				valuesByGroup[cursor.groupKey] = append(valuesByGroup[cursor.groupKey], value)
			}
			cursor.pointIndex++
			if cursor.pointIndex < len(cursor.points) {
				heap.Push(&cursors, cursor)
			}
		}
		for key, values := range valuesByGroup {
			value := legacyPrometheusValueQuantile(spec.Phi, values)
			formatted := strconv.FormatFloat(value, 'f', -1, 64)
			groups[key].points = append(groups[key].points, dataset.Point{
				Epoch:  pointEpoch,
				Values: []any{formatted},
			})
		}
	}

	output := make(dataset.SeriesList, 0, len(groupOrder))
	for _, key := range groupOrder {
		group := groups[key]
		if len(group.points) == 0 {
			continue
		}
		output = append(output, dataset.NewSeries(group.header, group.points))
	}
	result.SeriesList = output
}

// legacyPrometheusValueQuantile mirrors Prometheus's exact value-quantile helper,
// including its NaN ordering and IEEE-754 interpolation behavior for infinities.
func legacyPrometheusValueQuantile(phi float64, values []float64) float64 {
	if len(values) == 0 || math.IsNaN(phi) {
		return math.NaN()
	}
	if phi < 0 {
		return math.Inf(-1)
	}
	if phi > 1 {
		return math.Inf(1)
	}
	slices.SortFunc(values, func(a, b float64) int {
		aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
		switch {
		case aNaN && !bNaN:
			return -1
		case !aNaN && bNaN:
			return 1
		case a < b:
			return -1
		case a > b:
			return 1
		default:
			return 0
		}
	})

	n := float64(len(values))
	rank := phi * (n - 1)
	lowerIndex := math.Max(0, math.Floor(rank))
	upperIndex := math.Min(n-1, lowerIndex+1)
	weight := rank - math.Floor(rank)
	return values[int(lowerIndex)]*(1-weight) + values[int(upperIndex)]*weight
}

var _ heap.Interface = (*legacyQuantileCursorHeap)(nil)

const (
	legacyHistogramFieldName      = "histogram"
	legacySortInRangeQueryWarning = "PromQL warning: sort is ineffective for range queries " +
		"since results are always ordered by labels"
)

type legacyRankBucketKey struct {
	epoch int64
	group string
}

type legacyRankCandidate struct {
	series   *dataset.Series
	pointIdx int
	value    float64
	order    int
}

type legacySortItem struct {
	series *dataset.Series
	value  float64
	tags   string
}

type legacyRankCandidateHeap struct {
	items    []legacyRankCandidate
	operator string
	tagsJSON map[*dataset.Series]string
}

func (h *legacyRankCandidateHeap) Len() int { return len(h.items) }

// Less keeps the worst selected candidate at the root so a better incoming
// candidate can replace it in O(log k).
func (h *legacyRankCandidateHeap) Less(i, j int) bool {
	return h.compare(h.items[i], h.items[j]) > 0
}

func (h *legacyRankCandidateHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
}

func (h *legacyRankCandidateHeap) Push(value any) {
	h.items = append(h.items, value.(legacyRankCandidate))
}

func (h *legacyRankCandidateHeap) Pop() any {
	last := len(h.items) - 1
	value := h.items[last]
	h.items = h.items[:last]
	return value
}

func (h *legacyRankCandidateHeap) consider(candidate legacyRankCandidate, limit int) {
	if limit <= 0 {
		return
	}
	if len(h.items) < limit {
		heap.Push(h, candidate)
		return
	}
	if h.compare(candidate, h.items[0]) < 0 {
		h.items[0] = candidate
		heap.Fix(h, 0)
	}
}

func (h *legacyRankCandidateHeap) compare(a, b legacyRankCandidate) int {
	if c := legacyCompareRankCandidateValues(a, b, h.operator); c != 0 {
		return c
	}
	if c := strings.Compare(h.tags(a), h.tags(b)); c != 0 {
		return c
	}
	return cmp.Compare(a.order, b.order)
}

func (h *legacyRankCandidateHeap) tags(candidate legacyRankCandidate) string {
	if candidate.series == nil {
		return ""
	}
	if tags, ok := h.tagsJSON[candidate.series]; ok {
		return tags
	}
	if h.tagsJSON == nil {
		h.tagsJSON = make(map[*dataset.Series]string)
	}
	tags := candidate.series.Header.Tags.JSON()
	h.tagsJSON[candidate.series] = tags
	return tags
}

func (c *Client) legacyFinalizeTSMMergeEntry(query string, ts timeseries.Timeseries) {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return
	}
	expr, err := promql.Parse(query)
	if err != nil {
		return
	}
	c.legacyFinalizeTSMMerge(query, ds, expr)
}

func (c *Client) legacyFinalizeTSMMerge(query string, ds *dataset.DataSet, expr promql.Expr) {
	// servePlan calls this only when the planner omitted these outer operations
	// from its variants, preventing them from being applied twice.
	if wrapper, found := promql.ParseScalarBinaryWrapper(expr); found {
		c.legacyFinalizeTSMMerge(wrapper.Inner.String(), ds, wrapper.Inner)
		legacyFinalizeScalarBinaryWrapper(ds, query, wrapper)
		return
	}
	if spec, found := promql.ParseLimitRatioAggregation(expr); found {
		finalizeLimitRatio(ds, spec)
		return
	}
	if spec, found := promql.ParseLimitKAggregation(expr); found {
		legacyFinalizeLimitKAggregation(ds, spec)
		return
	}
	if spec, found := promql.ParseQuantileAggregation(expr); found {
		legacyFinalizeQuantileAggregation(ds, spec)
		return
	}
	if spec, found := promql.ParseVarianceAggregation(expr); found {
		legacyFinalizeVarianceAggregation(ds, spec)
		return
	}
	if spec, found := promql.ParseRankAggregation(expr); found {
		legacyFinalizeRankAggregation(ds, spec)
		return
	}
	if spec, found := promql.ParseSortWrapper(expr); found {
		_, _, aggregationFound := promql.CompleteOuterAggregation(spec.Inner)
		if aggregationFound || zeroFallbackMergesBySum(spec.Inner) {
			legacyFinalizeSortWrapper(ds, spec.Descending)
		}
	}
}

func legacyFinalizeSortWrapper(ds *dataset.DataSet, descending bool) {
	isRangeQuery := ds.Step() > 0

	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if isRangeQuery {
		legacyAppendWarningOnce(ds, legacySortInRangeQueryWarning)
	}

	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		result.SeriesList = legacyFilterHistogramSeries(result.SeriesList)
		if !isRangeQuery {
			legacySortInstantSeries(result.SeriesList, descending)
		}
	}
}

func legacyAppendWarningOnce(ds *dataset.DataSet, warning string) {
	if !slices.Contains(ds.Warnings, warning) {
		ds.Warnings = append(ds.Warnings, warning)
	}
}

func legacyFinalizeRankAggregation(ds *dataset.DataSet, spec promql.RankAggregation) {
	isRangeQuery := ds.Step() > 0

	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if isRangeQuery && spec.SortSet {
		legacyAppendWarningOnce(ds, legacySortInRangeQueryWarning)
	}

	for _, result := range ds.Results {
		if result == nil || len(result.SeriesList) == 0 {
			continue
		}
		buckets := make(map[legacyRankBucketKey]*legacyRankCandidateHeap)
		var order int
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			group := legacyRankGroupKey(series.Header.Tags, spec.Grouping)
			for i, point := range dspoints.Of(series) {
				value, ok := legacyRankPointValue(point)
				if !ok {
					continue
				}
				key := legacyRankBucketKey{epoch: int64(point.Epoch), group: group}
				bucket := buckets[key]
				if bucket == nil {
					bucket = &legacyRankCandidateHeap{operator: spec.Operator}
					buckets[key] = bucket
				}
				bucket.consider(legacyRankCandidate{
					series:   series,
					pointIdx: i,
					value:    value,
					order:    order,
				}, spec.K)
				order++
			}
		}

		selected := make(map[*dataset.Series]map[int]struct{})
		for _, candidates := range buckets {
			for _, candidate := range candidates.items {
				if selected[candidate.series] == nil {
					selected[candidate.series] = make(map[int]struct{})
				}
				selected[candidate.series][candidate.pointIdx] = struct{}{}
			}
		}
		result.SeriesList = legacyKeepSelectedRankPoints(result.SeriesList, selected)
		descending := spec.Operator == aggregation.TopK
		if spec.SortSet {
			descending = spec.SortDescending
		}
		if !isRangeQuery {
			legacySortInstantSeries(result.SeriesList, descending)
		}
	}
}

func legacyRankPointValue(point dataset.Point) (float64, bool) {
	if len(point.Values) == 0 {
		return 0, false
	}
	v, ok := point.Values[0].(string)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func legacyCompareRankCandidateValues(a, b legacyRankCandidate, operator string) int {
	if legacyRankValueLess(a.value, b.value, operator) {
		return -1
	}
	if legacyRankValueLess(b.value, a.value, operator) {
		return 1
	}
	return 0
}

func legacyRankValueLess(a, b float64, operator string) bool {
	aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
	if aNaN || bNaN {
		if aNaN && bNaN {
			return false
		}
		return !aNaN
	}
	if operator == aggregation.BottomK {
		return a < b
	}
	return a > b
}

func legacyRankGroupKey(tags dataset.Tags, grouping promql.AggregationGrouping) string {
	if len(grouping.Labels) == 0 {
		if grouping.Without {
			return tags.JSON()
		}
		return ""
	}
	if grouping.Without {
		kept := tags.Clone()
		for _, label := range grouping.Labels {
			delete(kept, label)
		}
		return kept.JSON()
	}
	kept := make(dataset.Tags, len(grouping.Labels))
	for _, label := range grouping.Labels {
		if value := tags[label]; value != "" {
			kept[label] = value
		}
	}
	return kept.JSON()
}

func legacyKeepSelectedRankPoints(
	seriesList dataset.SeriesList, selected map[*dataset.Series]map[int]struct{},
) dataset.SeriesList {
	keptSeries := seriesList[:0]
	for _, series := range seriesList {
		if series == nil {
			continue
		}
		selectedPoints := selected[series]
		if len(selectedPoints) == 0 {
			continue
		}
		points := dspoints.Of(series)
		keptPoints := points[:0]
		for i, point := range points {
			if _, ok := selectedPoints[i]; ok {
				keptPoints = append(keptPoints, point)
			}
		}
		if len(keptPoints) == 0 {
			continue
		}
		series.SetPoints(keptPoints)
		keptSeries = append(keptSeries, series)
	}
	return keptSeries
}

func legacyIsHistogramSeries(series *dataset.Series) bool {
	return series != nil &&
		len(series.Header.ValueFieldsList) > 0 &&
		series.Header.ValueFieldsList[0].Name == legacyHistogramFieldName
}

func legacyFilterHistogramSeries(seriesList dataset.SeriesList) dataset.SeriesList {
	filtered := seriesList[:0]
	for _, series := range seriesList {
		if series == nil || legacyIsHistogramSeries(series) {
			continue
		}
		filtered = append(filtered, series)
	}
	return filtered
}

func legacySortInstantSeries(seriesList dataset.SeriesList, descending bool) {
	if len(seriesList) < 2 {
		return
	}
	if seriesList[0] == nil || seriesList[0].PointCount() != 1 {
		return
	}
	epoch := dspoints.At(seriesList[0], 0).Epoch
	for _, series := range seriesList {
		if series == nil || series.PointCount() != 1 || dspoints.At(series, 0).Epoch != epoch {
			return
		}
	}

	items := make([]legacySortItem, 0, len(seriesList))
	for _, series := range seriesList {
		value, ok := legacyRankPointValue(dspoints.At(series, 0))
		if !ok {
			value = math.NaN()
		}
		items = append(items, legacySortItem{
			series: series,
			value:  value,
			tags:   series.Header.Tags.JSON(),
		})
	}

	operator := aggregation.BottomK
	if descending {
		operator = aggregation.TopK
	}
	slices.SortStableFunc(items, func(a, b legacySortItem) int {
		if legacyRankValueLess(a.value, b.value, operator) {
			return -1
		}
		if legacyRankValueLess(b.value, a.value, operator) {
			return 1
		}
		return strings.Compare(a.tags, b.tags)
	})

	for i := range items {
		seriesList[i] = items[i].series
	}
}

const legacyMillisecondsPerSecond = float64(time.Second / time.Millisecond)

func legacyFinalizeScalarBinaryWrapper(ds *dataset.DataSet, query string,
	wrapper promql.ScalarBinaryWrapper,
) {
	histogramOperations, _ := ds.ValueOperations.(promql.HistogramScalarOperations)
	dropMetricName := wrapper.DropsMetricName()
	usesEvaluationTime := wrapper.UsesEvaluationTime()
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		keptSeries := result.SeriesList[:0]
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			points := dspoints.Of(series)
			keptPoints := points[:0]
			for _, point := range points {
				if len(point.Values) == 0 {
					continue
				}
				oldValue, ok := point.Values[0].(string)
				if !ok {
					continue
				}
				var evaluationTime float64
				if usesEvaluationTime {
					evaluationTime = float64(int64(point.Epoch)/int64(time.Millisecond)) /
						legacyMillisecondsPerSecond
				}
				var value string
				if legacyIsHistogramSeries(series) {
					updated, keep := wrapper.ApplyHistogram(oldValue, histogramOperations,
						evaluationTime)
					value, ok = updated.(string)
					if !keep || !ok {
						continue
					}
				} else {
					parsed, err := strconv.ParseFloat(oldValue, 64)
					if err != nil {
						continue
					}
					updated, keep := wrapper.ApplyFloat(parsed, evaluationTime)
					if !keep {
						continue
					}
					value = strconv.FormatFloat(updated, 'f', -1, 64)
				}
				point.Values[0] = value
				keptPoints = append(keptPoints, point)
			}
			if len(keptPoints) == 0 {
				continue
			}
			series.SetPoints(keptPoints)
			series.Header.QueryStatement = query
			if dropMetricName {
				series.Header.Name = ""
				delete(series.Header.Tags, promql.MetricNameLabel)
			}
			series.Header.CalculateHash(true)
			series.Header.CalculateSize()
			keptSeries = append(keptSeries, series)
		}
		result.SeriesList = keptSeries
	}
}

const legacyInvalidPooledVarianceWarning = "trickster: pooled-variance finalization dropped an invalid intermediate point"

type legacyVarianceFinalizeGroup struct {
	header dataset.SeriesHeader
	states map[epoch.Epoch]dataset.PooledVarianceState
}

func legacyFinalizeVarianceAggregation(ds *dataset.DataSet, spec promql.VarianceAggregation) {
	isRangeQuery := ds.Step() > 0
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	switch {
	case legacyHasPooledVarianceState(ds):
		legacyFinalizePooledVarianceStates(ds, spec.Operator)
	case legacyIsCentralVarianceInput(ds, spec):
		legacyFinalizeCentralVariance(ds, spec)
	default:
		// Unsupported variance shapes retain the established per-shard fallback.
		// A sort wrapper still needs its normal global output handling.
	}

	if !spec.SortSet {
		return
	}
	if isRangeQuery {
		legacyAppendWarningOnce(ds, legacySortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		result.SeriesList = legacyFilterHistogramSeries(result.SeriesList)
		if !isRangeQuery {
			legacySortInstantSeries(result.SeriesList, spec.SortDescending)
		}
	}
}

func legacyHasPooledVarianceState(ds *dataset.DataSet) bool {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			// a state is held boxed, so only boxed values are read to look for one
			segs := series.Segments()
			for k := range segs {
				seg := &segs[k]
				if seg.NumCols() == 0 {
					continue
				}
				for i := range seg.Len() {
					if seg.KindAt(0, i) != dataset.KindExt {
						continue
					}
					if _, ok := seg.Value(0, i).(dataset.PooledVarianceState); ok {
						return true
					}
				}
			}
		}
	}
	return false
}

func legacyFinalizePooledVarianceStates(ds *dataset.DataSet, operator string) {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		keptSeries := result.SeriesList[:0]
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			points := dspoints.Of(series)
			keptPoints := points[:0]
			for _, point := range points {
				if len(point.Values) == 0 {
					legacyAppendWarningOnce(ds, legacyInvalidPooledVarianceWarning)
					continue
				}
				state, ok := point.Values[0].(dataset.PooledVarianceState)
				if !ok || state.Count <= 0 || math.IsNaN(state.Count) || math.IsInf(state.Count, 0) {
					legacyAppendWarningOnce(ds, legacyInvalidPooledVarianceWarning)
					continue
				}
				value := legacyVarianceFinalValue(state, operator)
				formatted := strconv.FormatFloat(value, 'f', -1, 64)
				point.Values[0] = formatted
				keptPoints = append(keptPoints, point)
			}
			if len(keptPoints) == 0 {
				continue
			}
			series.SetPoints(keptPoints)
			keptSeries = append(keptSeries, series)
		}
		result.SeriesList = keptSeries
	}
}

func legacyIsCentralVarianceInput(ds *dataset.DataSet, spec promql.VarianceAggregation) bool {
	return legacyDataSetContainsFinalizerInput(ds, spec.Inner)
}

func legacyDataSetContainsFinalizerInput(ds *dataset.DataSet, input promql.Expr) bool {
	candidates := map[string]struct{}{input.String(): {}}
	if operator, _, found := promql.CompleteOuterAggregation(input); found &&
		operator == aggregation.Average {
		sumQuery := promql.ReplaceOuterAggregator(input, aggregation.Average, aggregation.Sum)
		candidates[sumQuery] = struct{}{}
	}

	foundSeriesStatement := false
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			statement := strings.TrimSpace(series.Header.QueryStatement)
			if statement == "" {
				continue
			}
			foundSeriesStatement = true
			if _, found := candidates[statement]; found {
				return true
			}
		}
	}
	if foundSeriesStatement || ds.TimeRangeQuery == nil {
		return false
	}
	_, found := candidates[strings.TrimSpace(ds.TimeRangeQuery.Statement)]
	return found
}

func legacyFinalizeCentralVariance(ds *dataset.DataSet, spec promql.VarianceAggregation) {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		groups := make(map[string]*legacyVarianceFinalizeGroup)
		groupOrder := make([]string, 0)
		for _, series := range result.SeriesList {
			if series == nil || legacyIsHistogramSeries(series) {
				continue
			}
			tags := legacyAggregationGroupingTags(series.Header.Tags, spec.Grouping)
			key := tags.JSON()
			group := groups[key]
			if group == nil {
				header := series.Header.Clone()
				header.Tags = tags
				header.Name = tags[promql.MetricNameLabel]
				header.TagFieldsList = nil
				header.QueryStatement = spec.AggregationQuery
				header.CalculateHash(true)
				header.CalculateSize()
				group = &legacyVarianceFinalizeGroup{
					header: header,
					states: make(map[epoch.Epoch]dataset.PooledVarianceState),
				}
				groups[key] = group
				groupOrder = append(groupOrder, key)
			}
			for _, point := range dspoints.Of(series) {
				value, ok := legacyVariancePointFloat(point)
				if !ok {
					continue
				}
				group.states[point.Epoch] = group.states[point.Epoch].Add(value)
			}
		}

		output := make(dataset.SeriesList, 0, len(groupOrder))
		for _, key := range groupOrder {
			group := groups[key]
			epochs := make([]epoch.Epoch, 0, len(group.states))
			for pointEpoch := range group.states {
				epochs = append(epochs, pointEpoch)
			}
			slices.Sort(epochs)
			points := make(dataset.Points, 0, len(epochs))
			for _, pointEpoch := range epochs {
				formatted := strconv.FormatFloat(
					legacyVarianceFinalValue(group.states[pointEpoch], spec.Operator), 'f', -1, 64,
				)
				points = append(points, dataset.Point{
					Epoch:  pointEpoch,
					Values: []any{formatted},
				})
			}
			if len(points) == 0 {
				continue
			}
			output = append(output, dataset.NewSeries(group.header, points))
		}
		result.SeriesList = output
	}
}

func legacyAggregationGroupingTags(tags dataset.Tags, grouping promql.AggregationGrouping) dataset.Tags {
	if grouping.Without {
		output := tags.Clone()
		delete(output, promql.MetricNameLabel)
		for _, label := range grouping.Labels {
			delete(output, label)
		}
		return output
	}
	output := make(dataset.Tags, len(grouping.Labels))
	for _, label := range grouping.Labels {
		if value, found := tags[label]; found && value != "" {
			output[label] = value
		}
	}
	return output
}

func legacyVariancePointFloat(point dataset.Point) (float64, bool) {
	if len(point.Values) == 0 {
		return 0, false
	}
	switch value := point.Values[0].(type) {
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		return parsed, err == nil
	case float64:
		return value, true
	case float32:
		return float64(value), true
	default:
		return 0, false
	}
}

func legacyVarianceFinalValue(state dataset.PooledVarianceState, operator string) float64 {
	variance := state.PopulationVariance()
	if operator == aggregation.StdDev {
		return math.Sqrt(variance)
	}
	return variance
}
