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

package middleware

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProcessSimulatedLatency(t *testing.T) {
	t.Run("zero latency is noop", func(t *testing.T) {
		rec := httptest.NewRecorder()
		start := time.Now()
		processSimulatedLatency(rec, 0, 0)
		require.Less(t, time.Since(start), 20*time.Millisecond)
		require.Empty(t, rec.Header().Get(latencyHeaderName))
	})

	t.Run("negative latency is noop", func(t *testing.T) {
		rec := httptest.NewRecorder()
		processSimulatedLatency(rec, -time.Millisecond, time.Millisecond)
		require.Empty(t, rec.Header().Get(latencyHeaderName))
		processSimulatedLatency(rec, time.Millisecond, -time.Millisecond)
		require.Empty(t, rec.Header().Get(latencyHeaderName))
	})

	t.Run("fixed latency when min >= max", func(t *testing.T) {
		rec := httptest.NewRecorder()
		start := time.Now()
		processSimulatedLatency(rec, time.Millisecond, time.Millisecond)
		require.GreaterOrEqual(t, time.Since(start), time.Millisecond)
		require.Equal(t, "1ms", rec.Header().Get(latencyHeaderName))
	})

	t.Run("min greater than max uses min", func(t *testing.T) {
		rec := httptest.NewRecorder()
		processSimulatedLatency(rec, 2*time.Millisecond, time.Millisecond)
		require.Equal(t, "2ms", rec.Header().Get(latencyHeaderName))
	})

	t.Run("random range with zero result is noop", func(t *testing.T) {
		// maxMS=1 yields rand.Int63()%1 == 0, which skips sleep/header.
		rec := httptest.NewRecorder()
		processSimulatedLatency(rec, 0, time.Millisecond)
		require.Empty(t, rec.Header().Get(latencyHeaderName))
	})

	t.Run("random range applies latency", func(t *testing.T) {
		rec := httptest.NewRecorder()
		// Keep trying until the random path selects a positive delay.
		for range 50 {
			rec = httptest.NewRecorder()
			processSimulatedLatency(rec, time.Millisecond, 3*time.Millisecond)
			if rec.Header().Get(latencyHeaderName) != "" {
				break
			}
		}
		require.NotEmpty(t, rec.Header().Get(latencyHeaderName))
	})
}
