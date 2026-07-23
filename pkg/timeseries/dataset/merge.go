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

package dataset

import "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

// ValueMergeOperations lets a wire-format provider extend numeric dataset
// reduction for provider-specific values such as native histograms.
type ValueMergeOperations interface {
	MergeValues(dst, src any, strategy merge.Strategy) (any, bool)
	DivideValue(value any, divisor float64) (any, bool)
	PairingHash(header *SeriesHeader, queryStatement string) Hash
	FinalizeMerge(ds *DataSet, strategy merge.Strategy)
}

// MergeOpts controls a single merge invocation across the dataset package.
// All fields have zero-value defaults that preserve historical behavior:
// SortPoints=false leaves the output unsorted, Strategy=StrategyDedup
// applies last-value-wins, and ToleranceNanos=0 requires exact epoch matches
// for deduplication. ToleranceNanos is a nanosecond window for clustering
// near-duplicate samples from independent shards (Thanos-style); when >0 and
// Strategy is StrategyDedup, adjacent (sorted) points whose epoch
// difference is <= ToleranceNanos collapse to a single survivor.
type MergeOpts struct {
	SortPoints      bool
	Strategy        merge.Strategy
	ToleranceNanos  int64
	ValueOperations ValueMergeOperations
}
