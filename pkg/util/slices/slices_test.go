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

package slices

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func collect[T any](s []T, n int) [][]T {
	var out [][]T
	for c := range SlicesChunk(s, n) {
		out = append(out, c)
	}
	return out
}

func TestSlicesChunk(t *testing.T) {
	tests := map[string]struct {
		in   []int
		n    int
		want [][]int
	}{
		"exact multiple":    {[]int{1, 2, 3, 4}, 2, [][]int{{1, 2}, {3, 4}}},
		"trailing partial":  {[]int{1, 2, 3, 4, 5}, 2, [][]int{{1, 2}, {3, 4}, {5}}},
		"chunk exceeds len": {[]int{1, 2}, 10, [][]int{{1, 2}}},
		"chunk of one":      {[]int{1, 2}, 1, [][]int{{1}, {2}}},
		"empty input":       {[]int{}, 3, nil},
		"nil input":         {nil, 3, nil},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, test.want, collect(test.in, test.n))
		})
	}
}

// A non-positive chunk size must yield nothing rather than loop forever,
// which is what an unguarded `i += n` would do.
func TestSlicesChunkNonPositiveSize(t *testing.T) {
	for _, n := range []int{0, -1} {
		require.Empty(t, collect([]int{1, 2, 3}, n))
	}
}

// Breaking out of the range must stop the iteration, not merely stop
// consuming it.
func TestSlicesChunkEarlyTermination(t *testing.T) {
	var seen int
	for range SlicesChunk([]int{1, 2, 3, 4, 5, 6}, 2) {
		seen++
		break
	}
	require.Equal(t, 1, seen)
}

// Every element appears exactly once, in order, however the input divides.
func TestSlicesChunkIsLossless(t *testing.T) {
	in := make([]int, 250)
	for i := range in {
		in[i] = i
	}
	for _, n := range []int{1, 7, 100, 249, 250, 251} {
		var flat []int
		for c := range SlicesChunk(in, n) {
			require.LessOrEqual(t, len(c), n)
			require.NotEmpty(t, c)
			flat = append(flat, c...)
		}
		require.Equal(t, in, flat, "chunk size %d lost or reordered elements", n)
	}
}

// Chunks are subslices, sharing the input's backing array.
func TestSlicesChunkYieldsSubslices(t *testing.T) {
	in := []int{1, 2, 3, 4}
	for c := range SlicesChunk(in, 2) {
		c[0] = 99
		break
	}
	require.Equal(t, 99, in[0])
}
