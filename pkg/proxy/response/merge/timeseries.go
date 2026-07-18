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
	"fmt"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

// TimeseriesMergeFunc creates a MergeFunc for timeseries data
// The returned function accepts a timeseries.Timeseries and merges it into the accumulator
func TimeseriesMergeFunc(unmarshaler timeseries.UnmarshalerFunc) MergeFunc {
	return TimeseriesMergeFuncTolerant(unmarshaler, 0)
}

// TimeseriesMergeFuncTolerant is TimeseriesMergeFunc with an opt-in dedup
// tolerance window. toleranceNanos == 0 preserves legacy exact-epoch dedup.
func TimeseriesMergeFuncTolerant(unmarshaler timeseries.UnmarshalerFunc, toleranceNanos int64) MergeFunc {
	return func(accum *Accumulator, data any, idx int) error {
		ts, ok := data.(timeseries.Timeseries)
		if !ok {
			// If data is []byte, unmarshal it first (for backward compatibility during transition)
			body, ok := data.([]byte)
			if !ok {
				return fmt.Errorf("timeseries merge received unexpected data type %T", data)
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
			return nil
		}
		if toleranceNanos > 0 {
			if om, ok := accum.tsdata.(optsMerger); ok {
				// strategy=0 == merge.StrategyDedup
				om.MergeWithStrategyTolerant(true, 0, toleranceNanos, ts)
				return nil
			}
		}
		accum.tsdata.Merge(false, ts)
		return nil
	}
}

// TimeseriesBatchMergeFunc creates a BatchMergeFunc for decoded timeseries
// data. Inputs that still require unmarshaling are left to the sequential
// MergeFunc fallback.
func TimeseriesBatchMergeFunc() BatchMergeFunc {
	return TimeseriesBatchMergeFuncTolerant(0)
}

// TimeseriesBatchMergeFuncTolerant is TimeseriesBatchMergeFunc with an opt-in
// dedup tolerance window.
func TimeseriesBatchMergeFuncTolerant(toleranceNanos int64) BatchMergeFunc {
	return func(accum *Accumulator, items []BatchItem) (bool, error) {
		collection, ok := batchTimeseries(items)
		if !ok {
			return false, nil
		}

		accum.mu.Lock()
		defer accum.mu.Unlock()
		base := accum.tsdata
		start := 0
		if base == nil {
			base = collection[0]
			start = 1
		}
		if start < len(collection) {
			rest := collection[start:]
			if toleranceNanos > 0 {
				if om, ok := base.(optsMerger); ok {
					// strategy=0 == merge.StrategyDedup
					om.MergeWithStrategyTolerant(true, 0, toleranceNanos, rest...)
				} else {
					base.Merge(false, rest...)
				}
			} else {
				base.Merge(false, rest...)
			}
		}
		accum.tsdata = base
		return true, nil
	}
}

// strategyMerger is implemented by types that support strategy-aware merging
// (e.g., *dataset.DataSet). Using an interface avoids importing the dataset
// package, which would create an import cycle.
type strategyMerger interface {
	MergeWithStrategy(sortPoints bool, strategy int, collection ...timeseries.Timeseries)
}

// optsMerger is the tolerance-aware extension of strategyMerger. Implementers
// honor a sub-step tolerance window when collapsing dedup duplicates produced
// by independent shards. Kept primitive-typed to avoid importing the dataset
// package (which would form an import cycle).
type optsMerger interface {
	MergeWithStrategyTolerant(sortPoints bool, strategy int, toleranceNanos int64,
		collection ...timeseries.Timeseries)
}

// TimeseriesMergeFuncWithStrategy creates a MergeFunc that uses a merge strategy
// (as an int matching merge.Strategy) to aggregate values from matching
// series across backends, rather than deduplicating.
//
// For avg, pairwise merges accumulate sums; the final division happens in the
// RespondFunc via FinalizeAvg on the accumulator.
func TimeseriesMergeFuncWithStrategy(unmarshaler timeseries.UnmarshalerFunc, strategy int) MergeFunc {
	return TimeseriesMergeFuncWithStrategyTolerant(unmarshaler, strategy, 0)
}

// TimeseriesMergeFuncWithStrategyTolerant extends TimeseriesMergeFuncWithStrategy
// with an opt-in dedup tolerance window (nanoseconds). When toleranceNanos > 0,
// the underlying timeseries type, if it supports optsMerger, collapses
// near-duplicate samples whose epochs differ by no more than the window.
func TimeseriesMergeFuncWithStrategyTolerant(unmarshaler timeseries.UnmarshalerFunc,
	strategy int, toleranceNanos int64,
) MergeFunc {
	// For avg, accumulate as sum during pairwise merges
	pairwiseStrategy := strategy
	if strategy == int(merge.StrategyAvg) {
		pairwiseStrategy = int(merge.StrategySum)
	}
	return func(accum *Accumulator, data any, idx int) error {
		ts, ok := data.(timeseries.Timeseries)
		if !ok {
			body, ok := data.([]byte)
			if !ok {
				return fmt.Errorf("timeseries strategy merge received unexpected data type %T", data)
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
			if toleranceNanos > 0 {
				if om, ok := accum.tsdata.(optsMerger); ok {
					om.MergeWithStrategyTolerant(true, pairwiseStrategy, toleranceNanos, ts)
				} else if sm, ok := accum.tsdata.(strategyMerger); ok {
					sm.MergeWithStrategy(true, pairwiseStrategy, ts)
				} else {
					accum.tsdata.Merge(false, ts)
				}
			} else if sm, ok := accum.tsdata.(strategyMerger); ok {
				sm.MergeWithStrategy(true, pairwiseStrategy, ts)
			} else {
				accum.tsdata.Merge(false, ts)
			}
			accum.MergeCount++
		}
		return nil
	}
}

// TimeseriesBatchMergeFuncWithStrategy creates a strategy-aware BatchMergeFunc
// for decoded timeseries data.
func TimeseriesBatchMergeFuncWithStrategy(strategy int) BatchMergeFunc {
	return TimeseriesBatchMergeFuncWithStrategyTolerant(strategy, 0)
}

// TimeseriesBatchMergeFuncWithStrategyTolerant batches the same strategy and
// tolerance semantics as TimeseriesMergeFuncWithStrategyTolerant.
func TimeseriesBatchMergeFuncWithStrategyTolerant(strategy int,
	toleranceNanos int64,
) BatchMergeFunc {
	pairwiseStrategy := strategy
	if strategy == int(merge.StrategyAvg) {
		pairwiseStrategy = int(merge.StrategySum)
	}
	return func(accum *Accumulator, items []BatchItem) (bool, error) {
		collection, ok := batchTimeseries(items)
		if !ok {
			return false, nil
		}

		accum.mu.Lock()
		defer accum.mu.Unlock()
		base := accum.tsdata
		mergeCount := accum.MergeCount
		start := 0
		if base == nil {
			base = collection[0]
			mergeCount = 1
			start = 1
		}
		if start < len(collection) {
			rest := collection[start:]
			if toleranceNanos > 0 {
				if om, ok := base.(optsMerger); ok {
					om.MergeWithStrategyTolerant(true, pairwiseStrategy,
						toleranceNanos, rest...)
				} else if sm, ok := base.(strategyMerger); ok {
					sm.MergeWithStrategy(true, pairwiseStrategy, rest...)
				} else {
					base.Merge(false, rest...)
				}
			} else if sm, ok := base.(strategyMerger); ok {
				sm.MergeWithStrategy(true, pairwiseStrategy, rest...)
			} else {
				base.Merge(false, rest...)
			}
			mergeCount += len(rest)
		}
		accum.tsdata = base
		accum.MergeCount = mergeCount
		return true, nil
	}
}

func batchTimeseries(items []BatchItem) ([]timeseries.Timeseries, bool) {
	if len(items) == 0 {
		return nil, false
	}
	collection := make([]timeseries.Timeseries, len(items))
	for i, item := range items {
		ts, ok := item.Data.(timeseries.Timeseries)
		if !ok || ts == nil {
			return nil, false
		}
		collection[i] = ts
	}
	return collection, true
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
		if strategy == int(merge.StrategyAvg) && mergeCount > 1 {
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
