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

package model

import (
	"math"
	"net/http"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// MergeAndWriteVectorMergeFunc returns a MergeFunc for Vector (timeseries)
func MergeAndWriteVectorMergeFunc(unmarshaler timeseries.UnmarshalerFunc) merge.MergeFunc {
	standard := merge.TimeseriesMergeFunc(unmarshaler)
	return func(accum *merge.Accumulator, data any, idx int) error {
		ts, err := vectorTimeseries(data, unmarshaler)
		if err != nil {
			return err
		}
		candidate, scalar := ts.(*dataset.DataSet)
		if !scalar || candidate.SourceResultType != string(Scalar) {
			return standard(accum, ts, idx)
		}
		accum.UpdateTSData(func(current timeseries.Timeseries) timeseries.Timeseries {
			if current == nil {
				return candidate
			}
			selected, ok := current.(*dataset.DataSet)
			if !ok || selected.SourceResultType != string(Scalar) {
				return current
			}
			return selectScalarDataSet(selected, candidate)
		})
		return nil
	}
}

// MergeAndWriteVectorBatchMergeFunc batches vector merges while treating
// scalar responses as deterministic pool-member candidates rather than
// ordinary vector fragments.
func MergeAndWriteVectorBatchMergeFunc() merge.BatchMergeFunc {
	standard := merge.TimeseriesBatchMergeFunc()
	return func(accum *merge.Accumulator, items []merge.BatchItem) (bool, error) {
		if len(items) == 0 {
			return false, nil
		}
		candidates := make([]*dataset.DataSet, len(items))
		for i, item := range items {
			ds, ok := item.Data.(*dataset.DataSet)
			if !ok || ds == nil || ds.SourceResultType != string(Scalar) {
				return standard(accum, items)
			}
			candidates[i] = ds
		}
		accum.UpdateTSData(func(current timeseries.Timeseries) timeseries.Timeseries {
			var selected *dataset.DataSet
			if current != nil {
				selected, _ = current.(*dataset.DataSet)
			}
			for _, candidate := range candidates {
				if selected == nil {
					selected = candidate
					continue
				}
				selected = selectScalarDataSet(selected, candidate)
			}
			return selected
		})
		return true, nil
	}
}

func vectorTimeseries(data any, unmarshaler timeseries.UnmarshalerFunc) (timeseries.Timeseries, error) {
	if ts, ok := data.(timeseries.Timeseries); ok {
		return ts, nil
	}
	body, ok := data.([]byte)
	if !ok {
		return nil, timeseries.ErrUnknownFormat
	}
	return unmarshaler(body, nil)
}

func selectScalarDataSet(selected, candidate *dataset.DataSet) *dataset.DataSet {
	selectedWarnings := append([]string(nil), selected.Warnings...)
	if !hasNonNaNScalar(selected) && hasNonNaNScalar(candidate) {
		candidate.Warnings = append(selectedWarnings, candidate.Warnings...)
		return candidate
	}
	selected.Warnings = append(selected.Warnings, candidate.Warnings...)
	return selected
}

func hasNonNaNScalar(ds *dataset.DataSet) bool {
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
				value, ok := point.Values[0].(string)
				if !ok {
					continue
				}
				parsed, err := strconv.ParseFloat(value, 64)
				return err == nil && !math.IsNaN(parsed)
			}
		}
	}
	return false
}

// MergeAndWriteVectorRespondFunc returns a RespondFunc for Vector (timeseries)
func MergeAndWriteVectorRespondFunc(marshaler timeseries.MarshalWriterFunc) merge.RespondFunc {
	return func(w http.ResponseWriter, r *http.Request, accum *merge.Accumulator, statusCode int) {
		ts := accum.GetTSData()
		if ts == nil {
			failures.HandleBadGateway(w, r)
			return
		}

		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		MarshalTSOrVectorWriter(ts, nil, statusCode, w, true) //revive:disable:unhandled-error
	}
}
