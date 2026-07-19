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
	"math"
	"testing"
)

// Pooling changes floating-point operation order; require 1e-12 relative
// agreement with a direct Welford pass rather than bit-for-bit identity.
const pooledVarianceRelativeTolerance = 1e-12

func TestPooledVarianceStateMatchesDirectWelford(t *testing.T) {
	tests := []struct {
		name   string
		shards [][]float64
	}{
		{"unequal shards", [][]float64{{-5, 1}, {4, 8, 10, 13, 19}}},
		{"large offset", [][]float64{{1e12 + 1, 1e12 + 2}, {1e12 + 3, 1e12 + 4, 1e12 + 5}}},
		{"zero variance", [][]float64{{7, 7, 7}, {7, 7}}},
		{"singleton", [][]float64{{-12}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var direct, pooled PooledVarianceState
			for _, shard := range tt.shards {
				var shardState PooledVarianceState
				for _, value := range shard {
					direct = direct.Add(value)
					shardState = shardState.Add(value)
				}
				state, err := NewPooledVarianceState(
					shardState.Count, shardState.Mean, shardState.PopulationVariance(),
				)
				if err != nil {
					t.Fatalf("state: %v", err)
				}
				pooled = pooled.Merge(state)
			}
			got, want := pooled.PopulationVariance(), direct.PopulationVariance()
			tolerance := pooledVarianceRelativeTolerance * math.Max(1, math.Abs(want))
			if math.Abs(got-want) > tolerance {
				t.Fatalf("variance got %.17g want %.17g tolerance %.17g", got, want, tolerance)
			}
		})
	}
}

func TestPooledVarianceStateSpecialValues(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		state := PooledVarianceState{}.Add(value).Add(1)
		if !math.IsNaN(state.PopulationVariance()) {
			t.Fatalf("value %v produced variance %v", value, state.PopulationVariance())
		}
	}

	finite, err := NewPooledVarianceState(2, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := NewPooledVarianceState(1, math.Inf(1), math.NaN())
	if err != nil {
		t.Fatal(err)
	}
	if got := finite.Merge(invalid).PopulationVariance(); !math.IsNaN(got) {
		t.Fatalf("poisoned merge variance got %v", got)
	}
}

func TestNewPooledVarianceStateRejectsInvalidInputs(t *testing.T) {
	for _, input := range []struct {
		count    float64
		variance float64
	}{
		{0, 0},
		{-1, 0},
		{1.5, 0},
		{math.Inf(1), 0},
		{1, -1},
		{1, math.Inf(-1)},
	} {
		if _, err := NewPooledVarianceState(input.count, 0, input.variance); err == nil {
			t.Fatalf("input %#v unexpectedly accepted", input)
		}
	}
}
