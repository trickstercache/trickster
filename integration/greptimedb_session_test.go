/*
 * Copyright 2026 The Trickster Authors
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

package integration

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
)

// TestGreptimePGSessionSettings uses only SELECT/SHOW and session-local settings.
// It never changes fixture tables or account permissions.
func TestGreptimePGSessionSettings(t *testing.T) {
	var target pgwireTarget
	for _, candidate := range pgwireTargets() {
		if candidate.Provider == providers.GreptimeDB {
			target = candidate
			break
		}
	}
	require.NotEmpty(t, target.OriginAddr)
	probe, err := net.DialTimeout("tcp", target.OriginAddr, time.Second)
	if err != nil {
		t.Skipf("developer GreptimeDB is unavailable: %v", err)
	}
	_ = probe.Close()
	harness, address := pgwireHarness(t, target)
	harness.start(t)
	direct, err := pgwireConnect(t, target.OriginAddr, target, target.ClientPassword)
	require.NoError(t, err)
	defer direct.Close(context.Background())
	proxy, err := pgwireConnect(t, address, target, target.ClientPassword)
	require.NoError(t, err)
	defer proxy.Close(context.Background())
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	query := func(template string) string {
		return fmt.Sprintf(template, start.Format(time.RFC3339), start.Add(2*time.Hour).Format(time.RFC3339))
	}
	agree := func(sql string) {
		t.Helper()
		want, err := pgwireQuery(t, direct, sql)
		require.NoError(t, err, sql)
		got, err := pgwireQuery(t, proxy, sql)
		require.NoError(t, err, sql)
		require.Equal(t, want, got, sql)
	}
	stampSQL, epochSQL := query(target.DeltaSQLs[0]), query(target.DeltaSQLs[1])
	agree(stampSQL)
	agree(stampSQL)
	t.Run("failed SET preserves cache identity", func(t *testing.T) {
		_, wantErr := pgwireQuery(t, direct, "SET time_zone = 'not/a/timezone'")
		_, gotErr := pgwireQuery(t, proxy, "SET time_zone = 'not/a/timezone'")
		require.Error(t, wantErr)
		require.Equal(t, pgwireSQLState(wantErr), pgwireSQLState(gotErr))
		before := pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit")
		agree(stampSQL)
		require.Equal(t, before+1, pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit"))
	})
	t.Run("no-op PostgreSQL settings keep lossless epochs cacheable", func(t *testing.T) {
		agree(epochSQL)
		before := pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit")
		agree("SET extra_float_digits = -14")
		agree(epochSQL)
		agree("SET standard_conforming_strings = off")
		agree(epochSQL)
		require.Equal(t, before+2, pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit"))
	})
	t.Run("date style changes do not reuse timestamp bytes", func(t *testing.T) {
		agree("SET DateStyle = 'SQL, DMY'")
		agree(stampSQL)
		agree(stampSQL)
		agree("SET DateStyle = 'ISO, MDY'")
		agree(stampSQL)
		agree(stampSQL)
	})
	t.Run("LOCAL alias persists but partitions the cache", func(t *testing.T) {
		before := pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "kmiss")
		agree("SET LOCAL time_zone = 'Asia/Kolkata'")
		agree("SHOW TIMEZONE")
		agree(epochSQL)
		require.Equal(t, before+1, pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "kmiss"))
		before = pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit")
		agree(epochSQL)
		require.Equal(t, before+1, pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit"))
	})
	t.Run("an unknown setting disables caching", func(t *testing.T) {
		before := pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit")
		agree("SET trickster_phase5_unknown = 1")
		agree(epochSQL)
		agree(epochSQL)
		require.Equal(t, before, pgwireCacheCount(t, harness.MetricsAddr, target.Dialect, "delta", "hit"))
	})
}
