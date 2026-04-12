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

// TestClickHouse tests ClickHouse-specific capabilities through Trickster.
// Requires: make developer-start && make developer-seed-data.
// Shares the Trickster instance started by TestPrometheus (same dev config, same ports).
func TestClickHouse(t *testing.T) {
	developerHarness().start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	t.Run("time series query", func(t *testing.T) {
		// The seed data is historical NYC taxi trips with timestamps from
		// ~2015, so a "last 7 days" window returns nothing. Use the actual
		// data range instead: query the full span from epoch to now.
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d) "+
				"GROUP BY t ORDER BY t FORMAT JSON",
			0, time.Now().Unix(),
		)
		params := url.Values{"query": {q}}
		u := "http://" + tricksterAddr + "/click1/?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))

		var result struct {
			Meta []struct{ Name string } `json:"meta"`
			Data []json.RawMessage       `json:"data"`
			Rows int                     `json:"rows"`
		}
		require.NoError(t, json.Unmarshal(body, &result))
		require.NotEmpty(t, result.Data, "expected rows from ClickHouse trips table")
		t.Logf("clickhouse: %d rows returned", result.Rows)

		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		t.Logf("clickhouse: %s", resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", hdr["engine"])
	})

	t.Run("non-select proxied", func(t *testing.T) {
		params := url.Values{"query": {"SHOW TABLES"}}
		u := "http://" + tricksterAddr + "/click1/?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.Contains(t, string(body), "trips", "SHOW TABLES should include trips table")
		t.Logf("clickhouse non-select: %s", string(body))
	})

	// regression: #967 — multi-line SQL queries were rejected by the
	// ClickHouse parser because embedded newlines disrupted statement
	// classification. A well-formed SELECT split across lines must proxy
	// and cache through the DeltaProxyCache engine.
	t.Run("multi-line SQL", func(t *testing.T) {
		q := fmt.Sprintf(
			"SELECT\n    toStartOfFiveMinute(pickup_datetime) AS t,\n    count() AS cnt\nFROM trips\nWHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d)\nGROUP BY t\nORDER BY t\nFORMAT JSON",
			0, time.Now().Unix(),
		)
		params := url.Values{"query": {q}}
		u := "http://" + tricksterAddr + "/click1/?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", hdr["engine"],
			"multi-line SQL must reach DeltaProxyCache (issue #967)")
	})

	// Aggregation matrix: exercise a few grouping windows to confirm DPC
	// caches and serves repeat queries at different scales.
	aggCases := []struct {
		name  string
		group string
	}{
		{"five_minute", "toStartOfFiveMinute(pickup_datetime)"},
		{"fifteen_minute", "toStartOfInterval(pickup_datetime, INTERVAL 15 MINUTE)"},
		{"one_hour", "toStartOfHour(pickup_datetime)"},
	}
	for _, tc := range aggCases {
		t.Run("aggregation_"+tc.name, func(t *testing.T) {
			q := fmt.Sprintf(
				"SELECT %s AS t, count() AS cnt FROM trips "+
					"WHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d) "+
					"GROUP BY t ORDER BY t FORMAT JSON",
				tc.group, 0, time.Now().Unix(),
			)
			params := url.Values{"query": {q}}
			u := "http://" + tricksterAddr + "/click1/?" + params.Encode()

			resp, err := http.Get(u)
			require.NoError(t, err)
			resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)
			hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
			require.Equal(t, "DeltaProxyCache", hdr["engine"])

			resp2, err := http.Get(u)
			require.NoError(t, err)
			resp2.Body.Close()
			require.Equal(t, http.StatusOK, resp2.StatusCode)
			hdr2 := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
			require.Contains(t, []string{"hit", "phit"}, hdr2["status"],
				"%s repeat query should hit the cache, got %s", tc.name, hdr2["status"])
		})
	}
}
