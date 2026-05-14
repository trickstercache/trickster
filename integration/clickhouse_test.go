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

func TestClickHouse(t *testing.T) {
	cfg := writeTestConfig(t, 8570, 8571, 8582)
	clickAddr := "127.0.0.1:8570"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: clickAddr, MetricsAddr: "127.0.0.1:8571"}
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	t.Run("time series query", func(t *testing.T) {
		// Seed data timestamps are from ~2015, so query the full span from epoch to now.
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d) "+
				"GROUP BY t ORDER BY t FORMAT JSON",
			0, time.Now().Unix(),
		)
		params := url.Values{"query": {q}}
		u := "http://" + clickAddr + "/click1/?" + params.Encode()
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
		u := "http://" + clickAddr + "/click1/?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.Contains(t, string(body), "trips", "SHOW TABLES should include trips table")
		t.Logf("clickhouse non-select: %s", string(body))
	})

	t.Run("multi-line SQL", func(t *testing.T) {
		q := fmt.Sprintf(
			"SELECT\n    toStartOfFiveMinute(pickup_datetime) AS t,\n    count() AS cnt\nFROM trips\nWHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d)\nGROUP BY t\nORDER BY t\nFORMAT JSON",
			0, time.Now().Unix(),
		)
		params := url.Values{"query": {q}}
		u := "http://" + clickAddr + "/click1/?" + params.Encode()
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

	t.Run("grafana_official_plugin_native", func(t *testing.T) {
		// The official Grafana ClickHouse plugin sends default_format=Native
		// and client_protocol_version=54460 as URL params, with NO FORMAT in
		// the SQL. This triggers TCP-style Native responses (block info +
		// customSerialization flags).
		now := time.Now().Unix()
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime BETWEEN toDateTime(%d) AND toDateTime(%d) "+
				"GROUP BY t ORDER BY t",
			now-3600, now,
		)
		params := url.Values{
			"query":                   {q},
			"default_format":          {"Native"},
			"client_protocol_version": {"54460"},
			"database":                {"default"},
		}
		u := "http://" + clickAddr + "/click1/?" + params.Encode()

		// First request — should go through DPC and return Native binary
		resp, err := http.Get(u)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.Greater(t, len(body), 10, "expected Native binary response with data")

		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", hdr["engine"])

		// Verify the response is Native binary — first byte is numCols (uvarint),
		// or block info field 1 (0x01) if TCP-style. Either way, it should be
		// a small positive number, not a printable ASCII character.
		require.Less(t, body[0], byte(0x20), "expected Native binary, got text (byte 0x%02x)", body[0])

		// Second request — should hit DPC cache
		resp2, err := http.Get(u)
		require.NoError(t, err)
		body2, err := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp2.StatusCode)
		require.Greater(t, len(body2), 10, "expected cached Native response with data")

		hdr2 := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
		require.Contains(t, []string{"hit", "phit"}, hdr2["status"],
			"second request should hit the cache, got %s", hdr2["status"])
	})

	aggCases := []struct {
		name  string
		group string
	}{
		{"five_minute", "toStartOfFiveMinute(pickup_datetime)"},
		{"fifteen_minute", "toStartOfInterval(pickup_datetime, INTERVAL 15 MINUTE)"},
		{"one_hour", "toStartOfHour(pickup_datetime)"},
		{"one_day", "toStartOfDay(pickup_datetime)"},
		{"one_month", "toStartOfMonth(pickup_datetime)"},
		{"date_trunc_hour", "date_trunc('hour', pickup_datetime)"},
		{"date_trunc_day", "date_trunc('day', pickup_datetime)"},
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
			u := "http://" + clickAddr + "/click1/?" + params.Encode()

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
