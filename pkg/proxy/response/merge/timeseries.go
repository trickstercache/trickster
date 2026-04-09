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

package merge

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// TimeseriesMergeFunc creates a MergeFunc for timeseries data
// The returned function accepts a timeseries.Timeseries and merges it into the accumulator
func TimeseriesMergeFunc(unmarshaler timeseries.UnmarshalerFunc) MergeFunc {
	return func(accum *Accumulator, data any, idx int) error {
		ts, ok := data.(timeseries.Timeseries)
		if !ok {
			// If data is []byte, unmarshal it first (for backward compatibility during transition)
			body, ok := data.([]byte)
			if !ok {
				// Not a timeseries and not []byte
				return nil
			}
			var err error
			ts, err = unmarshaler(body, nil)
			if err != nil {
				return err
			}
		}
		accum.mu.Lock()
		defer accum.mu.Unlock()
		if accum.tsdata == nil {
			accum.tsdata = ts
		} else {
			accum.tsdata.Merge(false, ts)
		}
		return nil
	}
}

// strategyMerger is implemented by types that support strategy-aware merging
// (e.g., *dataset.DataSet). Using an interface avoids importing the dataset
// package, which would create an import cycle.
type strategyMerger interface {
	MergeWithStrategy(sortPoints bool, strategy int, collection ...timeseries.Timeseries)
}

// These constants must match dataset.MergeStrategy* values. Duplicated here
// to avoid importing dataset (which would create an import cycle).
const (
	mergeStrategySum = 1 // dataset.MergeStrategySum
	mergeStrategyAvg = 2 // dataset.MergeStrategyAvg
)

// TimeseriesMergeFuncWithStrategy creates a MergeFunc that uses a merge strategy
// (as an int matching dataset.MergeStrategy) to aggregate values from matching
// series across backends, rather than deduplicating.
//
// For avg, pairwise merges accumulate sums; the final division happens in the
// RespondFunc via FinalizeAvg on the accumulator.
func TimeseriesMergeFuncWithStrategy(unmarshaler timeseries.UnmarshalerFunc, strategy int) MergeFunc {
	// For avg, accumulate as sum during pairwise merges
	pairwiseStrategy := strategy
	if strategy == mergeStrategyAvg {
		pairwiseStrategy = mergeStrategySum
	}
	return func(accum *Accumulator, data any, idx int) error {
		ts, ok := data.(timeseries.Timeseries)
		if !ok {
			body, ok := data.([]byte)
			if !ok {
				return nil
			}
			var err error
			ts, err = unmarshaler(body, nil)
			if err != nil {
				return err
			}
		}
		accum.mu.Lock()
		defer accum.mu.Unlock()
		if accum.tsdata == nil {
			accum.tsdata = ts
			accum.MergeCount = 1
		} else {
			if sm, ok := accum.tsdata.(strategyMerger); ok {
				sm.MergeWithStrategy(true, pairwiseStrategy, ts)
			} else {
				accum.tsdata.Merge(false, ts)
			}
			accum.MergeCount++
		}
		return nil
	}
}

// TimeseriesMergeFuncFromBytes creates a MergeFunc that accepts []byte and unmarshals it
// This is a convenience function for call sites that still have []byte
func TimeseriesMergeFuncFromBytes(unmarshaler timeseries.UnmarshalerFunc) func(*Accumulator, []byte, int) error {
	mergeFunc := TimeseriesMergeFunc(unmarshaler)
	return func(accum *Accumulator, body []byte, idx int) error {
		ts, err := unmarshaler(body, nil)
		if err != nil {
			return err
		}
		return mergeFunc(accum, ts, idx)
	}
}

// avgFinalizer is implemented by types that can finalize avg values.
type avgFinalizer interface {
	FinalizeAvg(count int)
}

// TimeseriesRespondFuncWithStrategy creates a RespondFunc that finalizes avg
// aggregation before writing the response.
func TimeseriesRespondFuncWithStrategy(marshaler timeseries.MarshalWriterFunc, requestOptions *timeseries.RequestOptions, strategy int) RespondFunc {
	return func(w http.ResponseWriter, r *http.Request, accum *Accumulator, statusCode int) {
		accum.mu.Lock()
		ts := accum.tsdata
		mergeCount := accum.MergeCount
		accum.mu.Unlock()
		if ts == nil {
			failures.HandleBadGateway(w, r)
			return
		}
		// finalize avg: divide accumulated sums by merge count
		if strategy == mergeStrategyAvg && mergeCount > 1 {
			if af, ok := ts.(avgFinalizer); ok {
				af.FinalizeAvg(mergeCount)
			}
		}
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		headers.StripMergeHeaders(w.Header())
		marshaler(ts, requestOptions, statusCode, w)
	}
}

// TimeseriesRespondFunc creates a RespondFunc for timeseries data
// It writes the merged timeseries using the marshaler
func TimeseriesRespondFunc(marshaler timeseries.MarshalWriterFunc, requestOptions *timeseries.RequestOptions) RespondFunc {
	return func(w http.ResponseWriter, r *http.Request, accum *Accumulator, statusCode int) {
		accum.mu.Lock()
		ts := accum.tsdata
		accum.mu.Unlock()
		if ts == nil {
			failures.HandleBadGateway(w, r)
			return
		}
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		headers.StripMergeHeaders(w.Header())
		marshaler(ts, requestOptions, statusCode, w)
	}
}
