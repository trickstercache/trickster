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

import (
	"errors"
	"math"
)

// PooledVarianceState is an intermediate population-variance state. It is
// stored in a point only between TSM reduction and provider finalization.
type PooledVarianceState struct {
	Count float64
	Mean  float64
	M2    float64
}

// Add incorporates one raw float sample using Welford's online update.
func (s PooledVarianceState) Add(value float64) PooledVarianceState {
	if s.Count == 0 {
		s.Count = 1
		s.Mean = value
		if math.IsNaN(value) || math.IsInf(value, 0) {
			s.M2 = math.NaN()
		}
		return s
	}
	s.Count++
	delta := value - s.Mean
	s.Mean += delta / s.Count
	s.M2 += delta * (value - s.Mean)
	return s
}

// NewPooledVarianceState converts a population variance into a mergeable
// count/mean/M2 state.
func NewPooledVarianceState(count, mean, variance float64) (PooledVarianceState, error) {
	if math.IsNaN(count) || math.IsInf(count, 0) || count <= 0 || math.Trunc(count) != count {
		return PooledVarianceState{}, errors.New("pooled variance requires a positive integral count")
	}
	if math.IsInf(variance, -1) || variance < 0 {
		return PooledVarianceState{}, errors.New("pooled variance requires a non-negative variance")
	}
	state := PooledVarianceState{Count: count, Mean: mean, M2: count * variance}
	if math.IsNaN(variance) || math.IsNaN(mean) || math.IsInf(mean, 0) {
		state.M2 = math.NaN()
	}
	return state, nil
}

// Merge combines another state using the Chan parallel-variance formula.
func (s PooledVarianceState) Merge(other PooledVarianceState) PooledVarianceState {
	if s.Count == 0 {
		return other
	}
	if other.Count == 0 {
		return s
	}
	count := s.Count + other.Count
	if math.IsNaN(s.M2) || math.IsNaN(other.M2) ||
		math.IsNaN(s.Mean) || math.IsNaN(other.Mean) ||
		math.IsInf(s.Mean, 0) || math.IsInf(other.Mean, 0) {
		return PooledVarianceState{Count: count, Mean: math.NaN(), M2: math.NaN()}
	}
	delta := other.Mean - s.Mean
	return PooledVarianceState{
		Count: count,
		Mean:  s.Mean + delta*other.Count/count,
		M2: s.M2 + other.M2 +
			delta*delta*s.Count*other.Count/count,
	}
}

// PopulationVariance returns M2/n.
func (s PooledVarianceState) PopulationVariance() float64 {
	if s.Count <= 0 {
		return math.NaN()
	}
	return s.M2 / s.Count
}
