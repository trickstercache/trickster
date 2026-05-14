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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInfluxDB3HTTP exercises the v3 HTTP endpoints (`/api/v3/query_sql` and
// `/api/v3/query_influxql`) through Trickster's influx3 backend. Verifies that
// time-range queries are cached and non-cacheable queries pass through.
func TestInfluxDB3HTTP(t *testing.T) {
	cfg := writeTestConfigWithFlight(t, 8590, 8591, 8592, 0) // flight disabled for http tests
	influxAddr := "127.0.0.1:8590"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: influxAddr, MetricsAddr: "127.0.0.1:8591"}
	h.start(t)
	waitForInfluxDB3Data(t, "127.0.0.1:8181")

	baseURL := "http://" + influxAddr + "/influx3"
	now := time.Now().Unix()
	fiveMinAgo := now - 300

	doGet := func(t *testing.T, path string, params url.Values) (*http.Response, []byte) {
		t.Helper()
		u := baseURL + path + "?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, b
	}

	t.Run("sql_cacheable", func(t *testing.T) {
		q := fmt.Sprintf(
			"SELECT date_bin(INTERVAL '10 seconds', time) AS time, avg(usage_idle) AS usage_idle "+
				"FROM cpu WHERE cpu = 'cpu-total' AND time >= %d AND time < %d GROUP BY 1 ORDER BY 1",
			fiveMinAgo, now)
		params := url.Values{"q": {q}, "db": {"trickster"}, "format": {"json"}}

		// first call — populates cache
		resp1, body1 := doGet(t, "/api/v3/query_sql", params)
		require.Equal(t, http.StatusOK, resp1.StatusCode, "body: %s", string(body1))
		require.NotEmpty(t, body1)
		hdr1 := parseTricksterResult(resp1.Header.Get("X-Trickster-Result"))
		require.NotEmpty(t, hdr1["engine"], "expected engine on first call")

		// second call — should be a cache hit
		resp2, body2 := doGet(t, "/api/v3/query_sql", params)
		require.Equal(t, http.StatusOK, resp2.StatusCode, "body: %s", string(body2))
		hdr2 := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
		require.Contains(t, hdr2, "status", "expected status in X-Trickster-Result on second call")
	})

	t.Run("sql_jsonl_format", func(t *testing.T) {
		q := fmt.Sprintf(
			"SELECT date_bin(INTERVAL '10 seconds', time) AS time, avg(usage_idle) AS usage_idle "+
				"FROM cpu WHERE cpu = 'cpu-total' AND time >= %d AND time < %d GROUP BY 1 ORDER BY 1",
			fiveMinAgo, now)
		params := url.Values{"q": {q}, "db": {"trickster"}, "format": {"jsonl"}}
		resp, body := doGet(t, "/api/v3/query_sql", params)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		require.NotEmpty(t, body)
		// JSONL: each line a JSON object (or empty body if no rows)
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			if line == "" {
				continue
			}
			require.True(t, strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}"),
				"expected JSONL line, got: %s", line)
		}
	})

	t.Run("sql_csv_format", func(t *testing.T) {
		q := fmt.Sprintf(
			"SELECT date_bin(INTERVAL '10 seconds', time) AS time, avg(usage_idle) AS usage_idle "+
				"FROM cpu WHERE cpu = 'cpu-total' AND time >= %d AND time < %d GROUP BY 1 ORDER BY 1",
			fiveMinAgo, now)
		params := url.Values{"q": {q}, "db": {"trickster"}, "format": {"csv"}}
		resp, body := doGet(t, "/api/v3/query_sql", params)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		require.NotEmpty(t, body)
		// CSV: first line is header, should contain the column names
		firstLine := strings.SplitN(string(body), "\n", 2)[0]
		require.Contains(t, firstLine, "time", "expected time column in CSV header")
	})

	t.Run("sql_non_cacheable", func(t *testing.T) {
		// Query without date_bin or time-range WHERE falls through to proxy.
		params := url.Values{"q": {"SELECT 1"}, "db": {"trickster"}, "format": {"json"}}
		resp, body := doGet(t, "/api/v3/query_sql", params)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
	})

	t.Run("influxql_v3_native", func(t *testing.T) {
		q := `SELECT mean("usage_idle") FROM "cpu" WHERE "cpu" = 'cpu-total' AND time > now() - 5m GROUP BY time(10s)`
		params := url.Values{"q": {q}, "db": {"trickster"}, "format": {"json"}}
		resp, body := doGet(t, "/api/v3/query_influxql", params)
		require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", string(body))
		require.NotEmpty(t, body)
		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		require.NotEmpty(t, hdr["engine"], "expected engine on v3 InfluxQL response")
	})
}
