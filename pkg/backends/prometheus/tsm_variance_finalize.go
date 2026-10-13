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
	if spec.SortSet {
		sortFinalSeries(ds, spec.SortDescending)
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

func finalizePooledVarianceStates(ds *dataset.DataSet, operator string) {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		rows := rewriteFirstValues(result.SeriesList, func(_ *dataset.Series, seg *dataset.Segment, i int,
			dst []byte,
		) ([]byte, bool) {
			if seg.NumCols() == 0 {
				appendWarningOnce(ds, invalidPooledVarianceWarning)
				return dst, false
			}
			state, ok := seg.Value(0, i).(dataset.PooledVarianceState)
			if !ok || state.Count <= 0 || math.IsNaN(state.Count) || math.IsInf(state.Count, 0) {
				appendWarningOnce(ds, invalidPooledVarianceWarning)
				return dst, false
			}
			return strconv.AppendFloat(dst, varianceFinalValue(state, operator), 'f', -1, 64), true
		})
		keptSeries := result.SeriesList[:0]
		for si, series := range result.SeriesList {
			if rows[si] == nil {
				continue
			}
			series.SetSegments(rows[si])
			keptSeries = append(keptSeries, series)
		}
		result.SeriesList = keptSeries
	}
}

func isCentralVarianceInput(ds *dataset.DataSet, spec promql.VarianceAggregation) bool {
	// A supported nested plan fans out spec.Inner. The legacy sorted
	// fallback fans out the outer variance aggregation instead, so its statement
	// must not be aggregated a second time.
	return dataSetContainsFinalizerInput(ds, spec.Inner)
}

func dataSetContainsFinalizerInput(ds *dataset.DataSet, input promql.Expr) bool {
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

func finalizeCentralVariance(ds *dataset.DataSet, spec promql.VarianceAggregation) {
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		groups := aggregationGroups{grouping: spec.Grouping}
		var groupOrder []*varianceFinalizeGroup
		for _, series := range result.SeriesList {
			if series == nil || isHistogramSeries(series) {
				continue
			}
			id := groups.id(series.Header.Tags)
			if id == len(groupOrder) {
				groupOrder = append(groupOrder, &varianceFinalizeGroup{
					header: aggregationHeader(series, spec.Grouping, spec.AggregationQuery),
					states: make(map[epoch.Epoch]dataset.PooledVarianceState),
				})
			}
			group := groupOrder[id]
			for c := newRowCursor(series.Segments()); !c.done(); c.next() {
				value, ok := sampleNumber(c.seg(), c.i)
				if !ok {
					continue
				}
				pointEpoch := c.epoch()
				group.states[pointEpoch] = group.states[pointEpoch].Add(value)
			}
		}

		out := newTextSeriesLog()
		ids := make([]int, len(groupOrder))
		var epochs []epoch.Epoch
		for g, group := range groupOrder {
			ids[g] = out.addSeries()
			epochs = epochs[:0]
			for pointEpoch := range group.states {
				epochs = append(epochs, pointEpoch)
			}
			slices.Sort(epochs)
			for _, pointEpoch := range epochs {
				out.add(ids[g], pointEpoch, varianceFinalValue(group.states[pointEpoch], spec.Operator))
			}
		}
		rows := out.finish()
		output := make(dataset.SeriesList, 0, len(groupOrder))
		for g, group := range groupOrder {
			if rows[ids[g]] != nil {
				output = append(output, dataset.NewSeriesOf(group.header, rows[ids[g]]))
			}
		}
		result.SeriesList = output
	}
}

func varianceFinalValue(state dataset.PooledVarianceState, operator string) float64 {
	variance := state.PopulationVariance()
	if operator == aggregation.StdDev {
		return math.Sqrt(variance)
	}
	return variance
}
