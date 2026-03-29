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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMergeStrategy(t *testing.T) {
	tests := []struct {
		input    string
		expected MergeStrategy
		hasErr   bool
	}{
		{"", MergeStrategyDedup, false},
		{"dedup", MergeStrategyDedup, false},
		{"sum", MergeStrategySum, false},
		{"avg", MergeStrategyAvg, false},
		{"min", MergeStrategyMin, false},
		{"max", MergeStrategyMax, false},
		{"count", MergeStrategyCount, false},
		{"invalid", MergeStrategyDedup, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ms, err := ParseMergeStrategy(tt.input)
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
	require.Equal(t, "dedup", MergeStrategyDedup.String())
	require.Equal(t, "sum", MergeStrategySum.String())
	require.Equal(t, "avg", MergeStrategyAvg.String())
	require.Equal(t, "min", MergeStrategyMin.String())
	require.Equal(t, "max", MergeStrategyMax.String())
	require.Equal(t, "count", MergeStrategyCount.String())
}
