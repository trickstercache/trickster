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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCacheKey groups cache-key correctness tests under a single Trickster
// boot so that sequential subtests don't race on the shared :8480 port.
func TestCacheKey(t *testing.T) {
	cfg := writeTestConfig(t, 8576, 8577, 8585)
	ckAddr := "127.0.0.1:8576"
	h := tricksterHarness{ConfigPath: cfg, BaseAddr: ckAddr, MetricsAddr: "127.0.0.1:8577"}
	h.start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	// regression: #965
	t.Run("label values match param", func(t *testing.T) {
		fetch := func(t *testing.T, match string) map[string]string {
			t.Helper()
			u := "http://" + ckAddr + "/prom1/api/v1/label/job/values?" +
				url.Values{"match[]": {match}}.Encode()
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			resp, err := client.Get(u)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode,
				"unexpected status %d for match[]=%s: %s", resp.StatusCode, match, string(body))
			var pr promResponse
			require.NoError(t, json.Unmarshal(body, &pr))
			require.Equal(t, "success", pr.Status)
			return parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		}

		r1 := fetch(t, "up")
		t.Logf("match[]=up: %v", r1)
		require.NotEqual(t, "hit", r1["status"],
			"first request must not be a hit, got %v", r1)

		r2 := fetch(t, "process_cpu_seconds_total")
		t.Logf("match[]=process_cpu_seconds_total: %v", r2)
		require.NotEqual(t, "hit", r2["status"],
			"second request with a different match[] must not collide with the first (issue #965), got %v", r2)
	})

	// regression: #969
	t.Run("POST split params", func(t *testing.T) {
		now := time.Now()
		queryExpr := fmt.Sprintf("up + 0*%d", now.UnixNano())
		body := url.Values{
			"query": {queryExpr},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
		}.Encode()
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}

		post := func(t *testing.T, step string) map[string]string {
			t.Helper()
			u := "http://" + ckAddr + "/prom1/api/v1/query_range?step=" + step
			resp, err := client.Post(u, "application/x-www-form-urlencoded",
				strings.NewReader(body))
			require.NoError(t, err)
			defer resp.Body.Close()
			rb, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode,
				"unexpected status %d: %s", resp.StatusCode, string(rb))
			var pr promResponse
			require.NoError(t, json.Unmarshal(rb, &pr))
			require.Equal(t, "success", pr.Status)
			var qd promQueryData
			require.NoError(t, json.Unmarshal(pr.Data, &qd))
			require.Equal(t, "matrix", qd.ResultType,
				"split-params POST must reach the range handler (step in URL must survive)")
			return parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		}

		r1 := post(t, "15")
		t.Logf("step=15 split POST: %v", r1)
		require.Equal(t, "DeltaProxyCache", r1["engine"],
			"split-params POST must route through DeltaProxyCache (#969 read side)")

		r2 := post(t, "15")
		t.Logf("step=15 repeat: %v", r2)
		require.Equal(t, "hit", r2["status"],
			"repeat of identical split-params POST must hit the cache")

		r3 := post(t, "30")
		t.Logf("step=30 split POST: %v", r3)
		require.NotEqual(t, "hit", r3["status"],
			"changing step in URL must produce a distinct cache key (proves URL params are preserved in GetRequestValues)")
	})
}
