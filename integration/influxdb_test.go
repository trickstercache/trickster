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
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInfluxDB tests InfluxDB (Flux) capabilities through Trickster.
// Requires: make developer-start (Telegraf continuously writes to InfluxDB 2.x).
func TestInfluxDB(t *testing.T) {
	developerHarness().start(t)
	waitForInfluxDBData(t, "127.0.0.1:8086")

	fluxURL := "http://" + tricksterAddr + "/flux2/api/v2/query?org=trickster-dev"
	post := func(t *testing.T, body, token string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest("POST", fluxURL, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Token "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp, b
	}

	t.Run("flux query", func(t *testing.T) {
		resp, body := post(t, `{"query": "from(bucket: \"trickster\") |> range(start: -1h, stop: now()) |> aggregateWindow(every: 1m, fn: mean) |> limit(n: 5)", "type": "flux"}`, "trickster-dev-token")
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.NotEmpty(t, body)
		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		require.NotEmpty(t, hdr["engine"], "expected engine in X-Trickster-Result")
	})

	// Aggregation matrix: mean/max/sum over -1h windows. Each must round-trip
	// CSV back through Trickster with a valid engine set.
	fluxCases := []struct{ name, fn string }{
		{"mean", "mean"},
		{"max", "max"},
		{"sum", "sum"},
	}
	for _, fc := range fluxCases {
		t.Run("flux_"+fc.name, func(t *testing.T) {
			q := `{"query": "from(bucket: \"trickster\") |> range(start: -1h, stop: now()) |> filter(fn: (r) => r._field == \"usage_idle\") |> aggregateWindow(every: 1m, fn: ` + fc.fn + `) |> limit(n: 5)", "type": "flux"}`
			resp, body := post(t, q, "trickster-dev-token")
			require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
			lines := strings.Split(strings.TrimSpace(string(body)), "\n")
			require.Greater(t, len(lines), 1, "expected more than the header row from %s aggregation", fc.fn)
		})
	}

	t.Run("auth failure", func(t *testing.T) {
		resp, body := post(t, `{"query": "from(bucket: \"trickster\") |> range(start: -1h) |> limit(n: 1)", "type": "flux"}`, "wrong-token")
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"expected 401 for wrong token, got %d: %s", resp.StatusCode, string(body))
	})
}
