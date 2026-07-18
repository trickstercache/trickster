/*
 * Copyright 2018 The Trickster Authors
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

import (
	"cmp"
	"container/heap"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const (
	histogramFieldName      = "histogram"
	sortInRangeQueryWarning = "PromQL warning: sort is ineffective for range queries " +
		"since results are always ordered by labels"
)

type rankBucketKey struct {
	epoch int64
	group string
}

type rankCandidate struct {
	series   *dataset.Series
	pointIdx int
	value    float64
	order    int
}

type sortItem struct {
	series *dataset.Series
	value  float64
	tags   string
}

type rankCandidateHeap struct {
	items    []rankCandidate
	operator string
	tagsJSON map[*dataset.Series]string
}

func (h *rankCandidateHeap) Len() int { return len(h.items) }

// Less keeps the worst selected candidate at the root so a better incoming
// candidate can replace it in O(log k).
func (h *rankCandidateHeap) Less(i, j int) bool {
	return h.compare(h.items[i], h.items[j]) > 0
}

func (h *rankCandidateHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
}

func (h *rankCandidateHeap) Push(value any) {
	h.items = append(h.items, value.(rankCandidate))
}

func (h *rankCandidateHeap) Pop() any {
	last := len(h.items) - 1
	value := h.items[last]
	h.items = h.items[:last]
	return value
}

func (h *rankCandidateHeap) consider(candidate rankCandidate, limit int) {
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

func (h *rankCandidateHeap) compare(a, b rankCandidate) int {
	if c := compareRankCandidateValues(a, b, h.operator); c != 0 {
		return c
	}
	if c := strings.Compare(h.tags(a), h.tags(b)); c != 0 {
		return c
	}
	return cmp.Compare(a.order, b.order)
}

func (h *rankCandidateHeap) tags(candidate rankCandidate) string {
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

// FinalizeTSMMerge applies Prometheus-only rank and sort finalization after TSM
// fanout has accumulated the rewritten inner-query responses. These operations
// belong here so they use globally merged values rather than per-backend data.
func (c *Client) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		return
	}
	if spec, found := promql.ParseRankAggregation(query); found {
		finalizeRankAggregation(ds, spec)
		return
	}
	if spec, found := promql.ParseSortWrapper(query); found {
		if _, aggregationFound := promql.OuterAggregator(spec.InnerQuery); aggregationFound {
			finalizeSortWrapper(ds, spec.Descending)
		}
	}
}

func finalizeSortWrapper(ds *dataset.DataSet, descending bool) {
	isRangeQuery := ds.Step() > 0

	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if isRangeQuery {
		appendWarningOnce(ds, sortInRangeQueryWarning)
	}

	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		result.SeriesList = filterHistogramSeries(result.SeriesList)
		if !isRangeQuery {
			sortInstantSeries(result.SeriesList, descending)
		}
	}
}

func appendWarningOnce(ds *dataset.DataSet, warning string) {
	if !slices.Contains(ds.Warnings, warning) {
		ds.Warnings = append(ds.Warnings, warning)
	}
}

func finalizeRankAggregation(ds *dataset.DataSet, spec promql.RankAggregation) {
	isRangeQuery := ds.Step() > 0

	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if isRangeQuery && spec.SortSet {
		appendWarningOnce(ds, sortInRangeQueryWarning)
	}

	for _, result := range ds.Results {
		if result == nil || len(result.SeriesList) == 0 {
			continue
		}
		buckets := make(map[rankBucketKey]*rankCandidateHeap)
		var order int
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			group := rankGroupKey(series.Header.Tags, spec.Grouping)
			for i, point := range series.Points {
				value, ok := rankPointValue(point)
				if !ok {
					continue
				}
				key := rankBucketKey{epoch: int64(point.Epoch), group: group}
				bucket := buckets[key]
				if bucket == nil {
					bucket = &rankCandidateHeap{operator: spec.Operator}
					buckets[key] = bucket
				}
				bucket.consider(rankCandidate{
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
		result.SeriesList = keepSelectedRankPoints(result.SeriesList, selected)
		descending := spec.Operator == aggregation.TopK
		if spec.SortSet {
			descending = spec.SortDescending
		}
		if !isRangeQuery {
			sortInstantSeries(result.SeriesList, descending)
		}
	}
}

func rankPointValue(point dataset.Point) (float64, bool) {
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

func compareRankCandidateValues(a, b rankCandidate, operator string) int {
	if rankValueLess(a.value, b.value, operator) {
		return -1
	}
	if rankValueLess(b.value, a.value, operator) {
		return 1
	}
	return 0
}

func rankValueLess(a, b float64, operator string) bool {
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

func rankGroupKey(tags dataset.Tags, grouping promql.AggregationGrouping) string {
	if len(grouping.Labels) == 0 {
		if grouping.Without {
			return tags.String()
		}
		return ""
	}
	labelSet := make(map[string]struct{}, len(grouping.Labels))
	for _, label := range grouping.Labels {
		labelSet[label] = struct{}{}
	}
	if grouping.Without {
		keys := tags.Keys()
		kept := make([]string, 0, len(keys))
		for _, key := range keys {
			if _, skip := labelSet[key]; !skip {
				kept = append(kept, key+"="+tags[key])
			}
		}
		return strings.Join(kept, ";")
	}
	parts := make([]string, 0, len(grouping.Labels))
	for _, label := range grouping.Labels {
		parts = append(parts, label+"="+tags[label])
	}
	return strings.Join(parts, ";")
}

func keepSelectedRankPoints(
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
		keptPoints := series.Points[:0]
		for i, point := range series.Points {
			if _, ok := selectedPoints[i]; ok {
				keptPoints = append(keptPoints, point)
			}
		}
		if len(keptPoints) == 0 {
			continue
		}
		series.Points = keptPoints
		series.PointSize = series.Points.Size()
		keptSeries = append(keptSeries, series)
	}
	return keptSeries
}

func isHistogramSeries(series *dataset.Series) bool {
	return series != nil &&
		len(series.Header.ValueFieldsList) > 0 &&
		series.Header.ValueFieldsList[0].Name == histogramFieldName
}

func filterHistogramSeries(seriesList dataset.SeriesList) dataset.SeriesList {
	filtered := seriesList[:0]
	for _, series := range seriesList {
		if series == nil || isHistogramSeries(series) {
			continue
		}
		filtered = append(filtered, series)
	}
	return filtered
}

func sortInstantSeries(seriesList dataset.SeriesList, descending bool) {
	if len(seriesList) < 2 {
		return
	}
	if seriesList[0] == nil || len(seriesList[0].Points) != 1 {
		return
	}
	epoch := seriesList[0].Points[0].Epoch
	for _, series := range seriesList {
		if series == nil || len(series.Points) != 1 || series.Points[0].Epoch != epoch {
			return
		}
	}

	items := make([]sortItem, 0, len(seriesList))
	for _, series := range seriesList {
		value, ok := rankPointValue(series.Points[0])
		if !ok {
			value = math.NaN()
		}
		items = append(items, sortItem{
			series: series,
			value:  value,
			tags:   series.Header.Tags.JSON(),
		})
	}

	operator := aggregation.BottomK
	if descending {
		operator = aggregation.TopK
	}
	slices.SortStableFunc(items, func(a, b sortItem) int {
		if rankValueLess(a.value, b.value, operator) {
			return -1
		}
		if rankValueLess(b.value, a.value, operator) {
			return 1
		}
		return strings.Compare(a.tags, b.tags)
	})

	for i := range items {
		seriesList[i] = items[i].series
	}
}
