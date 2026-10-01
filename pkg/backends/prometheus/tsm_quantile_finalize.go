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
	"container/heap"
	"math"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const invalidQuantileParameterWarning = "PromQL warning: quantile parameter is NaN or outside [0, 1]"

type quantileFinalizeGroup struct {
	header dataset.SeriesHeader
	series int
	values []float64
}

type quantileCursor struct {
	rowCursor
	group *quantileFinalizeGroup
}

type quantileCursorHeap []*quantileCursor

func (h quantileCursorHeap) Len() int { return len(h) }

// Less orders cursors by epoch; an epoch's values are sorted for its quantile, so ties needn't be broken
func (h quantileCursorHeap) Less(i, j int) bool {
	return h[i].epoch() < h[j].epoch()
}

func (h quantileCursorHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *quantileCursorHeap) Push(value any) {
	*h = append(*h, value.(*quantileCursor))
}

func (h *quantileCursorHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	*h = old[:last]
	return value
}

func finalizeQuantileAggregation(ds *dataset.DataSet, spec promql.QuantileAggregation) {
	isRangeQuery := ds.Step() > 0
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	if dataSetContainsFinalizerInput(ds, spec.Inner) {
		if math.IsNaN(spec.Phi) || spec.Phi < 0 || spec.Phi > 1 {
			appendWarningOnce(ds, invalidQuantileParameterWarning)
		}
		for _, result := range ds.Results {
			if result != nil {
				finalizeQuantileResult(result, spec)
			}
		}
	}

	if !spec.SortSet {
		return
	}
	if isRangeQuery {
		appendWarningOnce(ds, sortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result != nil && !isRangeQuery {
			sortInstantSeries(result.SeriesList, spec.SortDescending)
		}
	}
}

func finalizeQuantileResult(result *dataset.Result, spec promql.QuantileAggregation) {
	groups := make(map[string]*quantileFinalizeGroup)
	groupOrder := make([]*quantileFinalizeGroup, 0)
	cursors := make(quantileCursorHeap, 0, len(result.SeriesList))
	out := newTextSeriesLog()

	for _, series := range result.SeriesList {
		if series == nil || isHistogramSeries(series) || series.PointCount() == 0 {
			continue
		}
		tags := aggregationGroupingTags(series.Header.Tags, spec.Grouping)
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
			group = &quantileFinalizeGroup{header: header, series: out.addSeries()}
			groups[key] = group
			groupOrder = append(groupOrder, group)
		}
		heap.Push(&cursors, &quantileCursor{rowCursor: newRowCursor(series.Segments()), group: group})
	}

	// the groups given a value at the current epoch
	var touched []*quantileFinalizeGroup
	for len(cursors) > 0 {
		pointEpoch := cursors[0].epoch()
		touched = touched[:0]
		for len(cursors) > 0 && cursors[0].epoch() == pointEpoch {
			cursor := heap.Pop(&cursors).(*quantileCursor)
			if value, ok := sampleNumber(cursor.seg(), cursor.i); ok {
				if len(cursor.group.values) == 0 {
					touched = append(touched, cursor.group)
				}
				cursor.group.values = append(cursor.group.values, value)
			}
			if cursor.next(); !cursor.done() {
				heap.Push(&cursors, cursor)
			}
		}
		for _, group := range touched {
			out.add(group.series, pointEpoch, prometheusValueQuantile(spec.Phi, group.values))
			group.values = group.values[:0]
		}
	}

	rows := out.finish()
	output := make(dataset.SeriesList, 0, len(groupOrder))
	for _, group := range groupOrder {
		if rows[group.series] == nil {
			continue
		}
		output = append(output, dataset.NewSeriesOf(group.header, rows[group.series]))
	}
	result.SeriesList = output
}

// prometheusValueQuantile mirrors Prometheus's exact value-quantile helper,
// including its NaN ordering and IEEE-754 interpolation behavior for infinities.
func prometheusValueQuantile(phi float64, values []float64) float64 {
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

var _ heap.Interface = (*quantileCursorHeap)(nil)
