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

	"github.com/stretchr/testify/require"
)

// TestInfluxDB tests InfluxDB (Flux) capabilities through Trickster.
// Requires: make developer-start (Telegraf continuously writes to InfluxDB 2.x).
func TestInfluxDB(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "../docs/developer/environment/trickster-config/trickster.yaml")
	waitForTrickster(t, "127.0.0.1:8481")
	waitForInfluxDBData(t, "127.0.0.1:8086")

	t.Run("flux query", func(t *testing.T) {
		fluxQuery := `{
			"query": "from(bucket: \"trickster\") |> range(start: -1h, stop: now()) |> aggregateWindow(every: 1m, fn: mean) |> limit(n: 5)",
			"type": "flux"
		}`
		u := "http://" + tricksterAddr + "/flux2/api/v2/query?org=trickster-dev"
		req, err := http.NewRequest("POST", u, strings.NewReader(fluxQuery))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Token trickster-dev-token")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unexpected status: %s", string(body))
		require.NotEmpty(t, body, "expected non-empty response from InfluxDB Flux query")

		hdr := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		t.Logf("influxdb flux: %s", resp.Header.Get("X-Trickster-Result"))
		require.NotEmpty(t, hdr["engine"], "expected engine in X-Trickster-Result")
		// The engine may be DeltaProxyCache or HTTPProxy depending on whether
		// the Flux query parser can extract the time range from the query body.
	})
}
