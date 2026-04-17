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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClickHouseHTTP(t *testing.T) {
	cfg := writeTestConfig(t, 8574, 8575, 8584)
	clickAddr := "127.0.0.1:8574"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: clickAddr, MetricsAddr: "127.0.0.1:8575"}
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	chQuery := func(t *testing.T, query string) []byte {
		t.Helper()
		u := fmt.Sprintf("http://%s/click1/?query=%s", clickAddr, url.QueryEscape(query))
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "query failed: %s", string(b))
		return b
	}

	chQueryJSON := func(t *testing.T, query string) []map[string]any {
		t.Helper()
		b := chQuery(t, query+" FORMAT JSON")
		var doc struct {
			Data []map[string]any `json:"data"`
			Rows int              `json:"rows"`
		}
		require.NoError(t, json.Unmarshal(b, &doc))
		return doc.Data
	}

	t.Run("select_trips", func(t *testing.T) {
		data := chQueryJSON(t, "SELECT pickup_datetime, passenger_count, trip_distance FROM trips LIMIT 10")
		require.Greater(t, len(data), 0)
		row := data[0]
		require.Contains(t, row, "pickup_datetime")
		require.Contains(t, row, "passenger_count")
		require.Contains(t, row, "trip_distance")
		t.Logf("%d rows, first: %v", len(data), row)
	})

	t.Run("select_count", func(t *testing.T) {
		b := chQuery(t, "SELECT count() AS cnt FROM trips FORMAT TabSeparated")
		require.NotEmpty(t, b)
		t.Logf("trips count: %s", string(b))
	})

	t.Run("cache_hit", func(t *testing.T) {
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d) "+
				"GROUP BY t ORDER BY t",
			0, time.Now().Unix(),
		)
		data1 := chQueryJSON(t, q)
		require.Greater(t, len(data1), 0)
		data2 := chQueryJSON(t, q)
		require.Equal(t, len(data1), len(data2))
	})
}
