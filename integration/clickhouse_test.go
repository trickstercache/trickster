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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClickHouseCacheMatrix(t *testing.T) {
	h := configHarness(t)
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")
	start, end := clickHouseTripBounds(t)
	const step = int64(5 * 60)
	start = start / step * step
	end = ((end / step) + 1) * step
	mid := start + ((end-start)/step/2)*step
	require.Greater(t, mid, start)
	require.Less(t, mid, end)

	for _, backend := range []string{"click1", "click-native"} {
		t.Run(backend, func(t *testing.T) {
			query := func(rangeEnd int64) (http.Header, []byte) {
				t.Helper()
				sql := fmt.Sprintf(
					"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
						"FROM trips WHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d) "+
						"GROUP BY t ORDER BY t FORMAT JSON",
					start, rangeEnd,
				)
				resp, body := h.do(t, "/"+backend+"/", withParams(url.Values{"query": {sql}}))
				require.Equal(t, http.StatusOK, resp.StatusCode, "query failed: %s", body)
				require.Contains(t, string(body), `"data":[`)
				return resp.Header.Clone(), body
			}

			firstHeader, firstBody := query(mid)
			require.Equal(t, "kmiss", parseTricksterResult(
				firstHeader.Get("X-Trickster-Result"),
			)["status"])
			secondHeader, secondBody := query(mid)
			require.Equal(t, firstBody, secondBody)
			require.Contains(t, []string{"hit", "phit"}, parseTricksterResult(
				secondHeader.Get("X-Trickster-Result"),
			)["status"])
			wideHeader, wideBody := query(end)
			require.Greater(t, len(wideBody), len(firstBody))
			require.Equal(t, "phit", parseTricksterResult(
				wideHeader.Get("X-Trickster-Result"),
			)["status"])
		})
	}
}

func clickHouseTripBounds(t *testing.T) (int64, int64) {
	t.Helper()
	query := url.QueryEscape(
		"SELECT toUnixTimestamp(min(pickup_datetime)), toUnixTimestamp(max(pickup_datetime)) FROM trips FORMAT TSV",
	)
	resp, err := http.Get("http://127.0.0.1:8123/?query=" + query)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "bounds query failed: %s", body)
	fields := strings.Fields(string(body))
	require.Len(t, fields, 2)
	start, err := strconv.ParseInt(fields[0], 10, 64)
	require.NoError(t, err)
	end, err := strconv.ParseInt(fields[1], 10, 64)
	require.NoError(t, err)
	return start, end
}

func TestClickHouse(t *testing.T) {
	h := configHarness(t)
	clickAddr := h.BaseAddr
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")
	fiveMinuteEnd := time.Now().Truncate(5 * time.Minute).Unix()

	t.Run("time series query", func(t *testing.T) {
		// Seed data timestamps are from ~2015, so query from epoch through the
		// most recent complete five-minute boundary.
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d) "+
				"GROUP BY t ORDER BY t FORMAT JSON",
			0, fiveMinuteEnd,
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
			"SELECT\n    toStartOfFiveMinute(pickup_datetime) AS t,\n    count() AS cnt\nFROM trips\nWHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d)\nGROUP BY t\nORDER BY t\nFORMAT JSON",
			0, fiveMinuteEnd,
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
		now := time.Now().Truncate(5 * time.Minute).Unix()
		q := fmt.Sprintf(
			"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
				"FROM trips "+
				"WHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d) "+
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
		step  time.Duration
	}{
		{"five_minute", "toStartOfFiveMinute(pickup_datetime)", 5 * time.Minute},
		{"fifteen_minute", "toStartOfInterval(pickup_datetime, INTERVAL 15 MINUTE)", 15 * time.Minute},
		{"one_hour", "toStartOfHour(pickup_datetime)", time.Hour},
		{"one_day", "toStartOfDay(pickup_datetime)", 24 * time.Hour},
	}
	for _, tc := range aggCases {
		t.Run("aggregation_"+tc.name, func(t *testing.T) {
			q := fmt.Sprintf(
				"SELECT %s AS t, count() AS cnt FROM trips "+
					"WHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d) "+
					"GROUP BY t ORDER BY t FORMAT JSON",
				tc.group, 0, time.Now().Truncate(tc.step).Unix(),
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
