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

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"
)

func TestClickHouseNativeSDK(t *testing.T) {
	cfg := writeTestConfig(t, 8580, 8581, 8587)
	clickAddr := "127.0.0.1:8580"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: clickAddr, MetricsAddr: "127.0.0.1:8581"}
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:        []string{clickAddr},
		Protocol:    clickhouse.HTTP,
		HttpUrlPath: "/click1/",
	})
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	t.Run("ping", func(t *testing.T) {
		require.NoError(t, db.PingContext(ctx))
	})

	t.Run("server_hello", func(t *testing.T) {
		var cnt uint64
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count() FROM trips").Scan(&cnt))
		require.Greater(t, cnt, uint64(0))
		t.Logf("count: %d", cnt)
	})

	t.Run("select_typed", func(t *testing.T) {
		rows, err := db.QueryContext(ctx, "SELECT pickup_datetime, passenger_count, trip_distance FROM trips WHERE pickup_datetime > now() - INTERVAL 1 YEAR ORDER BY pickup_datetime LIMIT 5")
		require.NoError(t, err)
		defer rows.Close()

		var count int
		for rows.Next() {
			var dt time.Time
			var passengers uint8
			var distance float32
			require.NoError(t, rows.Scan(&dt, &passengers, &distance))
			count++
		}
		require.NoError(t, rows.Err())
		require.Greater(t, count, 0)
		t.Logf("%d typed rows", count)
	})
}
