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

import "fmt"

// MergeOpts controls a single merge invocation across the dataset package.
// All fields have zero-value defaults that preserve historical behavior:
// SortPoints=false leaves the output unsorted, Strategy=MergeStrategyDedup
// applies last-value-wins, and ToleranceNanos=0 requires exact epoch matches
// for deduplication. ToleranceNanos is a nanosecond window for clustering
// near-duplicate samples from independent shards (Thanos-style); when >0 and
// Strategy is MergeStrategyDedup, adjacent (sorted) points whose epoch
// difference is <= ToleranceNanos collapse to a single survivor.
type MergeOpts struct {
	SortPoints     bool
	Strategy       MergeStrategy
	ToleranceNanos int64
}

// MergeStrategy defines how values from matching series (identical labels)
// are combined when merging time-series data from multiple backends.
type MergeStrategy int

const (
	// MergeStrategyDedup is the default: for matching epochs, the last value wins.
	MergeStrategyDedup MergeStrategy = iota
	// MergeStrategySum sums values at matching epochs.
	MergeStrategySum
	// MergeStrategyAvg averages values at matching epochs.
	MergeStrategyAvg
	// MergeStrategyMin takes the minimum value at matching epochs.
	MergeStrategyMin
	// MergeStrategyMax takes the maximum value at matching epochs.
	MergeStrategyMax
	// MergeStrategyCount counts the number of values at matching epochs.
	MergeStrategyCount
)

// ParseMergeStrategy converts a string to a MergeStrategy.
func ParseMergeStrategy(s string) (MergeStrategy, error) {
	switch s {
	case "", "dedup":
		return MergeStrategyDedup, nil
	case "sum":
		return MergeStrategySum, nil
	case "avg":
		return MergeStrategyAvg, nil
	case "min":
		return MergeStrategyMin, nil
	case "max":
		return MergeStrategyMax, nil
	case "count":
		return MergeStrategyCount, nil
	default:
		return MergeStrategyDedup, fmt.Errorf("unknown merge strategy: %q", s)
	}
}

// String returns the string representation of a MergeStrategy.
func (ms MergeStrategy) String() string {
	switch ms {
	case MergeStrategySum:
		return "sum"
	case MergeStrategyAvg:
		return "avg"
	case MergeStrategyMin:
		return "min"
	case MergeStrategyMax:
		return "max"
	case MergeStrategyCount:
		return "count"
	default:
		return "dedup"
	}
}
