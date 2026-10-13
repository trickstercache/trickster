//go:build !race

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

package global

import (
	"io"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/prometheus/prometheus/promql"
	"github.com/stretchr/testify/require"
)

// limitTestRounds is how many results the buffer reuse test encodes
const limitTestRounds = 20

func TestResultBodyReadToEndIsReused(t *testing.T) {
	// a result read to its end without a close, as a cache filling from it reads it, has its buffer reused; the race
	// detector makes a sync.Pool drop some of what it is given, so this runs without it
	points := make([]promql.FPoint, 10_000)
	for i := range points {
		points[i] = promql.FPoint{T: int64(i) * 15000, F: float64(i) / 7}
	}
	value := promql.Matrix{{Metric: podLabels("a"), Floats: points}}
	size := len(writeTestResult(t, value, nil, nil))
	require.Less(t, size, maxPooledResponse)
	k := &keepingWriter{ResponseRecorder: httptest.NewRecorder(), t: t}
	encode := func() {
		require.NoError(t, WriteResult(k, value, nil, nil, 0))
		n, err := io.Copy(io.Discard, k.body)
		require.NoError(t, err)
		require.Equal(t, int64(size), n)
	}
	encode()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range limitTestRounds {
		encode()
	}
	runtime.ReadMemStats(&after)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(size*limitTestRounds/4),
		"%d rounds of a %d-byte result", limitTestRounds, size)
}
