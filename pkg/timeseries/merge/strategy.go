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

import "fmt"

// Strategy defines how values from matching series (identical labels)
// are combined when merging time-series data from multiple backends.
type Strategy int

type StrategyName = string

const (
	// Common time-series aggregations.
	Dedup   StrategyName = "dedup"
	Sum     StrategyName = "sum"
	Count   StrategyName = "count"
	Average StrategyName = "avg"
	Minimum StrategyName = "min"
	Maximum StrategyName = "max"
	Scalar  StrategyName = "scalar"
)

const (
	// StrategyDedup is the default: for matching epochs, the last value wins.
	StrategyDedup Strategy = iota
	// StrategySum sums values at matching epochs.
	StrategySum
	// StrategyAvg averages values at matching epochs.
	StrategyAvg
	// StrategyMin takes the minimum value at matching epochs.
	StrategyMin
	// StrategyMax takes the maximum value at matching epochs.
	StrategyMax
	// StrategyCount counts the number of values at matching epochs.
	StrategyCount
	// StrategyScalar selects the first non-NaN scalar at each matching epoch.
	StrategyScalar
	// MaxStrategyValue is the largest valid Strategy value.
	MaxStrategyValue = StrategyScalar
)

// ParseStrategy converts a string to a Strategy.
func ParseStrategy(s string) (Strategy, error) {
	switch s {
	case "", Dedup:
		return StrategyDedup, nil
	case Sum:
		return StrategySum, nil
	case Average:
		return StrategyAvg, nil
	case Minimum:
		return StrategyMin, nil
	case Maximum:
		return StrategyMax, nil
	case Count:
		return StrategyCount, nil
	case Scalar:
		return StrategyScalar, nil
	default:
		return StrategyDedup, fmt.Errorf("unknown merge strategy: %q", s)
	}
}

// String returns the string representation of a Strategy.
func (ms Strategy) String() string {
	switch ms {
	case StrategySum:
		return Sum
	case StrategyAvg:
		return Average
	case StrategyMin:
		return Minimum
	case StrategyMax:
		return Maximum
	case StrategyCount:
		return Count
	case StrategyScalar:
		return Scalar
	default:
		return Dedup
	}
}
