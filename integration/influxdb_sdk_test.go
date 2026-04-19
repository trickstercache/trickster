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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/stretchr/testify/require"
)

func TestInfluxDBSDK(t *testing.T) {
	cfg := writeTestConfig(t, 8576, 8577, 8585)
	influxAddr := "127.0.0.1:8576"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: influxAddr, MetricsAddr: "127.0.0.1:8577"}
	h.start(t)
	waitForInfluxDBData(t, "127.0.0.1:8086")

	serverURL := "http://" + influxAddr + "/flux2"
	client := influxdb2.NewClient(serverURL, "trickster-dev-token")
	t.Cleanup(func() { client.Close() })

	queryAPI := client.QueryAPI("trickster-dev")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	t.Run("flux_query", func(t *testing.T) {
		result, err := queryAPI.Query(ctx,
			`from(bucket: "trickster") |> range(start: -5m) |> limit(n: 10)`)
		require.NoError(t, err)

		var count int
		for result.Next() {
			record := result.Record()
			require.NotNil(t, record.Time())
			count++
		}
		require.NoError(t, result.Err())
		require.Greater(t, count, 0, "expected at least one FluxRecord")
		t.Logf("%d records", count)
	})

	t.Run("cache_hit", func(t *testing.T) {
		q := `from(bucket: "trickster") |> range(start: -5m) |> limit(n: 5)`

		result1, err := queryAPI.Query(ctx, q)
		require.NoError(t, err)
		for result1.Next() {
		}
		require.NoError(t, result1.Err())

		result2, err := queryAPI.Query(ctx, q)
		require.NoError(t, err)
		for result2.Next() {
		}
		require.NoError(t, result2.Err())
	})

	// cache_hit_header uses raw HTTP to inspect X-Trickster-Result since the v2
	// SDK hides response headers. Asserts the second identical query lands in
	// the delta-proxy cache — regression coverage for the `now()` range-bound
	// handling in flux.parseRange (which previously flowed to HTTPProxy).
	t.Run("cache_hit_header", func(t *testing.T) {
		fluxURL := "http://" + influxAddr + "/flux2/api/v2/query?org=trickster-dev"
		body := `{"query": "from(bucket: \"trickster\") |> range(start: -1h, stop: now()) |> aggregateWindow(every: 1m, fn: mean) |> limit(n: 5)", "type": "flux"}`
		do := func() *http.Response {
			req, err := http.NewRequest("POST", fluxURL, strings.NewReader(body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Token trickster-dev-token")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return resp
		}
		r1 := do()
		require.Equal(t, http.StatusOK, r1.StatusCode)
		hdr1 := parseTricksterResult(r1.Header.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", hdr1["engine"],
			"expected DeltaProxyCache engine (got %q) — now() range bound may not be parsed",
			r1.Header.Get("X-Trickster-Result"))

		r2 := do()
		require.Equal(t, http.StatusOK, r2.StatusCode)
		hdr2 := parseTricksterResult(r2.Header.Get("X-Trickster-Result"))
		// "hit" = full cache hit, "phit" = partial (next step boundary advanced).
		require.Contains(t, []string{"hit", "phit"}, hdr2["status"],
			"expected cache hit on second call, got %q (header: %q)",
			hdr2["status"], r2.Header.Get("X-Trickster-Result"))
	})
}
