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
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const invalidQuantileParameterWarning = "PromQL warning: quantile parameter is NaN or outside [0, 1]"

type quantileFinalizeGroup struct {
	header dataset.SeriesHeader
	points dataset.Points
}

type quantileCursor struct {
	series     *dataset.Series
	pointIndex int
	groupKey   string
	order      int
}

type quantileCursorHeap []*quantileCursor

func (h quantileCursorHeap) Len() int { return len(h) }

func (h quantileCursorHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	aEpoch := a.series.Points[a.pointIndex].Epoch
	bEpoch := b.series.Points[b.pointIndex].Epoch
	if aEpoch != bEpoch {
		return aEpoch < bEpoch
	}
	return a.order < b.order
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

	if dataSetContainsQueryStatement(ds, spec.InnerQuery) {
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
	groupOrder := make([]string, 0)
	cursors := make(quantileCursorHeap, 0, len(result.SeriesList))

	for order, series := range result.SeriesList {
		if series == nil || isHistogramSeries(series) || len(series.Points) == 0 {
			continue
		}
		tags := aggregationGroupingTags(series.Header.Tags, spec.Grouping)
		key := tags.JSON()
		if groups[key] == nil {
			header := series.Header.Clone()
			header.Tags = tags
			header.Name = tags["__name__"]
			header.TagFieldsList = nil
			header.QueryStatement = spec.AggregationQuery
			header.CalculateHash(true)
			header.CalculateSize()
			groups[key] = &quantileFinalizeGroup{header: header}
			groupOrder = append(groupOrder, key)
		}
		heap.Push(&cursors, &quantileCursor{series: series, groupKey: key, order: order})
	}

	for len(cursors) > 0 {
		pointEpoch := cursors[0].series.Points[cursors[0].pointIndex].Epoch
		valuesByGroup := make(map[string][]float64)
		for len(cursors) > 0 &&
			cursors[0].series.Points[cursors[0].pointIndex].Epoch == pointEpoch {
			cursor := heap.Pop(&cursors).(*quantileCursor)
			point := cursor.series.Points[cursor.pointIndex]
			if value, ok := variancePointFloat(point); ok {
				valuesByGroup[cursor.groupKey] = append(valuesByGroup[cursor.groupKey], value)
			}
			cursor.pointIndex++
			if cursor.pointIndex < len(cursor.series.Points) {
				heap.Push(&cursors, cursor)
			}
		}
		for key, values := range valuesByGroup {
			value := prometheusValueQuantile(spec.Phi, values)
			formatted := strconv.FormatFloat(value, 'f', -1, 64)
			groups[key].points = append(groups[key].points, dataset.Point{
				Epoch:  pointEpoch,
				Size:   len(formatted) + 32,
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
		output = append(output, &dataset.Series{
			Header:    group.header,
			Points:    group.points,
			PointSize: group.points.Size(),
		})
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
