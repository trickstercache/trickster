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
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const invalidPooledVarianceWarning = "trickster: pooled-variance finalization dropped an invalid intermediate point"

type varianceFinalizeGroup struct {
	header dataset.SeriesHeader
	states map[epoch.Epoch]dataset.PooledVarianceState
}

func finalizeVarianceAggregation(ds *dataset.DataSet, spec promql.VarianceAggregation) {
	isRangeQuery := ds.Step() > 0
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()

	switch {
	case hasPooledVarianceState(ds):
		finalizePooledVarianceStates(ds, spec.Operator)
	case isCentralVarianceInput(ds, spec):
		finalizeCentralVariance(ds, spec)
	default:
		// Unsupported variance shapes retain the established per-shard fallback.
		// A sort wrapper still needs its normal global output handling.
	}

	if !spec.SortSet {
		return
	}
	if isRangeQuery {
		appendWarningOnce(ds, sortInRangeQueryWarning)
	}
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		result.SeriesList = filterHistogramSeries(result.SeriesList)
		if !isRangeQuery {
			sortInstantSeries(result.SeriesList, spec.SortDescending)
		}
	}
}

func hasPooledVarianceState(ds *dataset.DataSet) bool {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			for _, point := range series.Points {
				if len(point.Values) == 0 {
					continue
				}
				if _, ok := point.Values[0].(dataset.PooledVarianceState); ok {
					return true
				}
			}
		}
	}
	return false
}

func finalizePooledVarianceStates(ds *dataset.DataSet, operator string) {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		keptSeries := result.SeriesList[:0]
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			keptPoints := series.Points[:0]
			for _, point := range series.Points {
				if len(point.Values) == 0 {
					appendWarningOnce(ds, invalidPooledVarianceWarning)
					continue
				}
				state, ok := point.Values[0].(dataset.PooledVarianceState)
				if !ok || state.Count <= 0 || math.IsNaN(state.Count) || math.IsInf(state.Count, 0) {
					appendWarningOnce(ds, invalidPooledVarianceWarning)
					continue
				}
				value := varianceFinalValue(state, operator)
				formatted := strconv.FormatFloat(value, 'f', -1, 64)
				point.Values[0] = formatted
				point.Size = len(formatted) + 32
				keptPoints = append(keptPoints, point)
			}
			if len(keptPoints) == 0 {
				continue
			}
			series.Points = keptPoints
			series.PointSize = keptPoints.Size()
			keptSeries = append(keptSeries, series)
		}
		result.SeriesList = keptSeries
	}
}

func isCentralVarianceInput(ds *dataset.DataSet, spec promql.VarianceAggregation) bool {
	// A supported nested plan fans out spec.InnerQuery. The legacy sorted
	// fallback fans out the outer variance aggregation instead, so its statement
	// must not be aggregated a second time.
	return dataSetContainsQueryStatement(ds, spec.InnerQuery)
}

func dataSetContainsQueryStatement(ds *dataset.DataSet, query string) bool {
	candidate := strings.TrimSpace(query)
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
			if statement == candidate {
				return true
			}
		}
	}
	return !foundSeriesStatement && ds.TimeRangeQuery != nil &&
		strings.TrimSpace(ds.TimeRangeQuery.Statement) == candidate
}

func finalizeCentralVariance(ds *dataset.DataSet, spec promql.VarianceAggregation) {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		groups := make(map[string]*varianceFinalizeGroup)
		groupOrder := make([]string, 0)
		for _, series := range result.SeriesList {
			if series == nil || isHistogramSeries(series) {
				continue
			}
			tags := aggregationGroupingTags(series.Header.Tags, spec.Grouping)
			key := tags.JSON()
			group := groups[key]
			if group == nil {
				header := series.Header.Clone()
				header.Tags = tags
				header.Name = tags["__name__"]
				header.TagFieldsList = nil
				header.QueryStatement = spec.AggregationQuery
				header.CalculateHash(true)
				header.CalculateSize()
				group = &varianceFinalizeGroup{
					header: header,
					states: make(map[epoch.Epoch]dataset.PooledVarianceState),
				}
				groups[key] = group
				groupOrder = append(groupOrder, key)
			}
			for _, point := range series.Points {
				value, ok := variancePointFloat(point)
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
					varianceFinalValue(group.states[pointEpoch], spec.Operator), 'f', -1, 64,
				)
				points = append(points, dataset.Point{
					Epoch:  pointEpoch,
					Size:   len(formatted) + 32,
					Values: []any{formatted},
				})
			}
			if len(points) == 0 {
				continue
			}
			output = append(output, &dataset.Series{
				Header:    group.header,
				Points:    points,
				PointSize: points.Size(),
			})
		}
		result.SeriesList = output
	}
}

func aggregationGroupingTags(tags dataset.Tags, grouping promql.AggregationGrouping) dataset.Tags {
	if grouping.Without {
		output := tags.Clone()
		delete(output, "__name__")
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

func variancePointFloat(point dataset.Point) (float64, bool) {
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

func varianceFinalValue(state dataset.PooledVarianceState, operator string) float64 {
	variance := state.PopulationVariance()
	if operator == aggregation.StdDev {
		return math.Sqrt(variance)
	}
	return variance
}
