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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseStrategy(t *testing.T) {
	tests := []struct {
		input    string
		expected Strategy
		hasErr   bool
	}{
		{"", StrategyDedup, false},
		{Dedup, StrategyDedup, false},
		{Sum, StrategySum, false},
		{Average, StrategyAvg, false},
		{Minimum, StrategyMin, false},
		{Maximum, StrategyMax, false},
		{Count, StrategyCount, false},
		{Scalar, StrategyScalar, false},
		{"invalid", StrategyDedup, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ms, err := ParseStrategy(tt.input)
			if tt.hasErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.expected, ms)
		})
	}
}

func TestMergeStrategyString(t *testing.T) {
	require.Equal(t, Dedup, StrategyDedup.String())
	require.Equal(t, Sum, StrategySum.String())
	require.Equal(t, Average, StrategyAvg.String())
	require.Equal(t, Minimum, StrategyMin.String())
	require.Equal(t, Maximum, StrategyMax.String())
	require.Equal(t, Count, StrategyCount.String())
	require.Equal(t, Scalar, StrategyScalar.String())
}
