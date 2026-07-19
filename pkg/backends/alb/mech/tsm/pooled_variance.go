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

package tsm

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"

	responsemerge "github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

type pooledVarianceResultKey struct {
	statementID int
	name        string
}

type pooledVarianceSeriesKey struct {
	result pooledVarianceResultKey
	hash   dataset.Hash
}

type pooledVariancePointKey struct {
	series pooledVarianceSeriesKey
	epoch  epoch.Epoch
}

type pooledVarianceValue struct {
	value  float64
	series *dataset.Series
}

type pooledVariancePointIndex struct {
	values  map[pooledVariancePointKey]pooledVarianceValue
	invalid map[pooledVariancePointKey]struct{}
	order   []pooledVariancePointKey
}

type pooledVarianceMemberPoint struct {
	key    pooledVariancePointKey
	header dataset.SeriesHeader
	state  dataset.PooledVarianceState
}

type pooledVarianceStateBits struct {
	count uint64
	mean  uint64
	m2    uint64
}

type pooledVarianceSeenStates struct {
	first  pooledVarianceStateBits
	others map[pooledVarianceStateBits]struct{}
}

type pooledVariancePointRef struct {
	series *dataset.Series
	index  int
}

type pooledVarianceOutput struct {
	dataset *dataset.DataSet
	results map[pooledVarianceResultKey]*dataset.Result
	series  map[pooledVarianceSeriesKey]*dataset.Series
	points  map[pooledVariancePointKey]pooledVariancePointRef
	query   string
}

func reducePooledVariancePlan(
	ctx context.Context,
	plan *tsmerge.TSMMergePlan,
	executions []planVariantExecution,
) (*responsemerge.Accumulator, []string, error) {
	variantIndexes := make(map[string]int, len(plan.Variants))
	for i, variant := range plan.Variants {
		variantIndexes[variant.Name] = i
	}
	inputNames := plan.Reduction.InputVariants
	orderedExecutions := make([]planVariantExecution, len(inputNames))
	for i, name := range inputNames {
		index, ok := variantIndexes[name]
		if !ok {
			return nil, nil, fmt.Errorf("pooled-variance input %q is not in the plan", name)
		}
		orderedExecutions[i] = executions[index]
	}

	memberCount := 0
	if len(orderedExecutions) > 0 {
		memberCount = len(orderedExecutions[0].contributions)
	}
	seenStates := make(map[pooledVariancePointKey]pooledVarianceSeenStates)
	var output *pooledVarianceOutput
	var warnings []string

	for member := range memberCount {
		if err := ctx.Err(); err != nil {
			return nil, warnings, err
		}
		datasets := make([]*dataset.DataSet, len(orderedExecutions))
		complete := true
		for variantIndex := range orderedExecutions {
			contribution := orderedExecutions[variantIndex].contributions[member]
			if contribution == nil {
				complete = false
				break
			}
			ds, ok := contribution.data.(*dataset.DataSet)
			if !ok || ds == nil {
				return nil, warnings, fmt.Errorf(
					"pooled-variance variant %q member %d requires a dataset, got %T",
					inputNames[variantIndex], member, contribution.data,
				)
			}
			datasets[variantIndex] = ds
		}
		if !complete {
			continue
		}
		newOutput := output == nil
		if newOutput {
			output = newPooledVarianceOutput(datasets[0], plan.OriginalQuery)
		}
		if !newOutput {
			// The response-authority variant owns the envelope. Preserve one copy
			// per included member, matching standard TSM merge.
			output.mergeAuthorityEnvelope(datasets[0])
		}

		memberPoints, dropped, err := pairPooledVarianceMember(
			ctx, datasets[0], datasets[1], datasets[2], plan.OriginalQuery,
		)
		if err != nil {
			return nil, warnings, err
		}
		if dropped > 0 {
			warnings = append(warnings,
				"trickster: tsm excluded "+strconv.Itoa(dropped)+
					" incomplete pooled-variance point(s) from pool member "+strconv.Itoa(member))
		}
		for _, point := range memberPoints {
			bits := pooledVarianceBits(point.state)
			seen, found := seenStates[point.key]
			if !found {
				seenStates[point.key] = pooledVarianceSeenStates{first: bits}
			} else {
				if bits == seen.first {
					continue
				}
				if seen.others == nil {
					capacity := min(memberCount-1, 8)
					seen.others = make(map[pooledVarianceStateBits]struct{}, capacity)
					seenStates[point.key] = seen
				}
				if _, duplicate := seen.others[bits]; duplicate {
					continue
				}
				seen.others[bits] = struct{}{}
			}
			output.mergePoint(point.key, point.header, point.state)
		}
	}

	accumulator := responsemerge.NewAccumulator()
	if output != nil {
		output.finish()
		accumulator.SetTSData(output.dataset)
	}
	return accumulator, warnings, nil
}

func indexPooledVariancePoints(
	ctx context.Context, ds *dataset.DataSet, pairingQuery string,
) (pooledVariancePointIndex, error) {
	index := pooledVariancePointIndex{
		values:  make(map[pooledVariancePointKey]pooledVarianceValue),
		invalid: make(map[pooledVariancePointKey]struct{}),
	}
	if err := ctx.Err(); err != nil {
		return index, err
	}
	if ds == nil {
		return index, nil
	}
	for _, result := range ds.Results {
		if err := ctx.Err(); err != nil {
			return index, err
		}
		if result == nil {
			continue
		}
		resultKey := pooledVarianceResultKey{statementID: result.StatementID, name: result.Name}
		for _, series := range result.SeriesList {
			if err := ctx.Err(); err != nil {
				return index, err
			}
			if series == nil {
				continue
			}
			if len(series.Header.ValueFieldsList) > 0 &&
				series.Header.ValueFieldsList[0].Name == "histogram" {
				continue
			}
			seriesKey := pooledVarianceSeriesKey{
				result: resultKey,
				hash:   series.Header.CalculateHashWithQueryStatement(pairingQuery),
			}
			for pointIndex, point := range series.Points {
				if pointIndex&255 == 0 {
					if err := ctx.Err(); err != nil {
						return index, err
					}
				}
				key := pooledVariancePointKey{series: seriesKey, epoch: point.Epoch}
				if _, seen := index.values[key]; !seen {
					if _, invalid := index.invalid[key]; !invalid {
						index.order = append(index.order, key)
					}
				}
				value, ok := pooledVarianceFloat(point)
				if !ok {
					delete(index.values, key)
					index.invalid[key] = struct{}{}
					continue
				}
				if prior, exists := index.values[key]; exists &&
					math.Float64bits(prior.value) != math.Float64bits(value) {
					delete(index.values, key)
					index.invalid[key] = struct{}{}
					continue
				}
				if _, invalid := index.invalid[key]; invalid {
					continue
				}
				index.values[key] = pooledVarianceValue{value: value, series: series}
			}
		}
	}
	return index, nil
}

func pairPooledVarianceMember(
	ctx context.Context,
	countDS, meanDS, varianceDS *dataset.DataSet,
	pairingQuery string,
) (
	[]pooledVarianceMemberPoint, int, error,
) {
	datasets := []*dataset.DataSet{countDS, meanDS, varianceDS}
	indexes := make([]pooledVariancePointIndex, 0, len(datasets))
	for _, ds := range datasets {
		index, err := indexPooledVariancePoints(ctx, ds, pairingQuery)
		if err != nil {
			return nil, 0, err
		}
		indexes = append(indexes, index)
	}
	orderedKeys := make([]pooledVariancePointKey, 0)
	seenKeys := make(map[pooledVariancePointKey]struct{})
	for _, index := range indexes {
		for _, key := range index.order {
			if _, seen := seenKeys[key]; seen {
				continue
			}
			seenKeys[key] = struct{}{}
			orderedKeys = append(orderedKeys, key)
		}
		for key := range index.invalid {
			if _, seen := seenKeys[key]; seen {
				continue
			}
			seenKeys[key] = struct{}{}
			orderedKeys = append(orderedKeys, key)
		}
	}

	points := make([]pooledVarianceMemberPoint, 0, len(orderedKeys))
	dropped := 0
	for keyIndex, key := range orderedKeys {
		if keyIndex&255 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, dropped, err
			}
		}
		values := [3]pooledVarianceValue{}
		usable := true
		for i, index := range indexes {
			if _, invalid := index.invalid[key]; invalid {
				usable = false
				break
			}
			var found bool
			values[i], found = index.values[key]
			if !found {
				usable = false
				break
			}
		}
		if !usable {
			dropped++
			continue
		}
		state, err := dataset.NewPooledVarianceState(
			values[0].value, values[1].value, values[2].value,
		)
		if err != nil {
			dropped++
			continue
		}
		points = append(points, pooledVarianceMemberPoint{
			key:    key,
			header: values[0].series.Header.Clone(),
			state:  state,
		})
	}
	return points, dropped, nil
}

func pooledVarianceFloat(point dataset.Point) (float64, bool) {
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
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func pooledVarianceBits(state dataset.PooledVarianceState) pooledVarianceStateBits {
	return pooledVarianceStateBits{
		count: math.Float64bits(state.Count),
		mean:  math.Float64bits(state.Mean),
		m2:    math.Float64bits(state.M2),
	}
}

func newPooledVarianceOutput(ds *dataset.DataSet, query string) *pooledVarianceOutput {
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	clone := &dataset.DataSet{
		Status:       ds.Status,
		Error:        ds.Error,
		ErrorType:    ds.ErrorType,
		Warnings:     append([]string(nil), ds.Warnings...),
		Sorter:       ds.Sorter,
		Merger:       ds.Merger,
		SizeCropper:  ds.SizeCropper,
		RangeCropper: ds.RangeCropper,
		Results:      make(dataset.Results, 0, len(ds.Results)),
	}
	if ds.ExtentList != nil {
		clone.ExtentList = ds.ExtentList.Clone()
	}
	if ds.VolatileExtentList != nil {
		clone.VolatileExtentList = ds.VolatileExtentList.Clone()
	}
	if ds.TimeRangeQuery != nil {
		clone.TimeRangeQuery = ds.TimeRangeQuery.Clone()
		clone.TimeRangeQuery.Statement = query
	}
	output := &pooledVarianceOutput{
		dataset: clone,
		results: make(map[pooledVarianceResultKey]*dataset.Result),
		series:  make(map[pooledVarianceSeriesKey]*dataset.Series),
		points:  make(map[pooledVariancePointKey]pooledVariancePointRef),
		query:   query,
	}
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		resultClone := &dataset.Result{
			StatementID: result.StatementID,
			Name:        result.Name,
			Error:       result.Error,
		}
		clone.Results = append(clone.Results, resultClone)
		key := pooledVarianceResultKey{statementID: result.StatementID, name: result.Name}
		output.results[key] = resultClone
	}
	return output
}

func (o *pooledVarianceOutput) mergeAuthorityEnvelope(ds *dataset.DataSet) {
	o.dataset.ExtentList = o.dataset.ExtentList.Merge(ds.ExtentList, o.dataset.Step())
	o.dataset.Warnings = append(o.dataset.Warnings, ds.Warnings...)
	if o.dataset.Status == "" || (o.dataset.Status != "success" && ds.Status == "success") {
		o.dataset.Status = ds.Status
	}
}

func (o *pooledVarianceOutput) mergePoint(
	key pooledVariancePointKey,
	header dataset.SeriesHeader,
	state dataset.PooledVarianceState,
) {
	if ref, found := o.points[key]; found {
		point := &ref.series.Points[ref.index]
		current := point.Values[0].(dataset.PooledVarianceState)
		point.Values[0] = current.Merge(state)
		return
	}
	result := o.results[key.series.result]
	if result == nil {
		result = &dataset.Result{
			StatementID: key.series.result.statementID,
			Name:        key.series.result.name,
		}
		o.dataset.Results = append(o.dataset.Results, result)
		o.results[key.series.result] = result
	}
	series := o.series[key.series]
	if series == nil {
		header.QueryStatement = o.query
		header.CalculateHash(true)
		header.CalculateSize()
		series = &dataset.Series{Header: header}
		result.SeriesList = append(result.SeriesList, series)
		o.series[key.series] = series
	}
	series.Points = append(series.Points, dataset.Point{
		Epoch:  key.epoch,
		Size:   56,
		Values: []any{state},
	})
	o.points[key] = pooledVariancePointRef{series: series, index: len(series.Points) - 1}
}

func (o *pooledVarianceOutput) finish() {
	for _, series := range o.series {
		slices.SortFunc(series.Points, func(a, b dataset.Point) int {
			switch {
			case a.Epoch < b.Epoch:
				return -1
			case a.Epoch > b.Epoch:
				return 1
			default:
				return 0
			}
		})
		series.PointSize = series.Points.Size()
	}
}
