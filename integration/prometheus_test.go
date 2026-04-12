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
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const tricksterAddr = "127.0.0.1:8480"

// TestPrometheus tests Prometheus-specific capabilities through Trickster.
// Requires: make developer-start && a running trickster with the developer config.
func TestPrometheus(t *testing.T) {
	h := developerHarness()
	h.start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	// Validate miss/hit cycle across every configured cache provider.
	runCacheProviderMatrix(t, func(t *testing.T, c cacheProviderCase) {
		t.Run("range query cache miss then hit", func(t *testing.T) {
			now := time.Now()
			params := url.Values{
				// Unique query per backend so cache keys don't collide across subtests.
				"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
				"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
				"end":   {fmt.Sprintf("%d", now.Unix())},
				"step":  {"15"},
			}
			pr, hdr := h.queryProm(t, c.Backend, "/api/v1/query_range", withParams(params))
			require.Equal(t, "success", pr.Status)
			var qd promQueryData
			require.NoError(t, json.Unmarshal(pr.Data, &qd))
			require.Equal(t, "matrix", qd.ResultType)
			requireTricksterResult(t, hdr, map[string]string{
				"engine": "DeltaProxyCache",
				"status": "kmiss",
			})

			_, hdr2 := h.queryProm(t, c.Backend, "/api/v1/query_range", withParams(params))
			requireTricksterResult(t, hdr2, map[string]string{"status": "hit"})
		})
	})

	t.Run("range query partial hit", func(t *testing.T) {
		now := time.Now()
		// First: warm the cache with a 3-minute window
		narrow := url.Values{
			"query": {"process_cpu_seconds_total"},
			"start": {fmt.Sprintf("%d", now.Add(-3*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		_, _ = queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", narrow)

		// Second: request a wider 10-minute window — should be a partial hit
		wide := url.Values{
			"query": {"process_cpu_seconds_total"},
			"start": {fmt.Sprintf("%d", now.Add(-10*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", wide)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("wide request: %s", hdr.Get("X-Trickster-Result"))
		require.Contains(t, []string{"phit", "hit"}, result["status"],
			"expected partial hit or hit, got %s", result["status"])
	})

	t.Run("instant query", func(t *testing.T) {
		params := url.Values{"query": {"up"}}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query", params)
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("instant query: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])
	})

	t.Run("labels", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/labels", nil)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("labels: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])
		var labels []string
		require.NoError(t, json.Unmarshal(pr.Data, &labels))
		require.Contains(t, labels, "job")
		require.Contains(t, labels, "instance")
		require.Contains(t, labels, "__name__")
	})

	t.Run("series", func(t *testing.T) {
		params := url.Values{"match[]": {"up"}}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/series", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("series: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])
		var series []map[string]string
		require.NoError(t, json.Unmarshal(pr.Data, &series))
		require.NotEmpty(t, series)
		require.Contains(t, series[0], "job")
	})

	t.Run("label values", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/label/job/values", nil)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("label values: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])
		var values []string
		require.NoError(t, json.Unmarshal(pr.Data, &values))
		require.Contains(t, values, "prometheus")
	})

	t.Run("negative cache", func(t *testing.T) {
		// Send an invalid PromQL query to trigger 400 from Prometheus
		params := url.Values{"query": {"invalid_query{{{}"}}
		u := "http://" + tricksterAddr + "/prom1/api/v1/query?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		resp.Body.Close()
		t.Logf("negative cache first: status=%d, X-Trickster-Result=%s",
			resp.StatusCode, resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		// Second request — may get nchit if negative caching is configured
		resp2, err := http.Get(u)
		require.NoError(t, err)
		resp2.Body.Close()
		result := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
		t.Logf("negative cache second: status=%d, result=%v", resp2.StatusCode, result)
		if result["status"] == "nchit" {
			t.Log("confirmed negative cache hit")
		} else {
			// Negative caching may not be configured for this status code; just verify we still get 400
			require.Equal(t, http.StatusBadRequest, resp2.StatusCode)
		}
	})

	t.Run("POST range query", func(t *testing.T) {
		now := time.Now()
		form := url.Values{
			"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		u := "http://" + tricksterAddr + "/prom1/api/v1/query_range"
		resp, err := http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var pr promResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		result := parseTricksterResult(resp.Header.Get("X-Trickster-Result"))
		t.Logf("POST range query: %s", resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", result["engine"])
	})

	t.Run("fast forward", func(t *testing.T) {
		now := time.Now()
		params := url.Values{
			"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
			"start": {fmt.Sprintf("%d", now.Add(-30*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			// Step must be > FastForwardTTL (default 15s) for fast-forward to activate.
			"step": {"60"},
		}
		_, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("fast forward: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", result["engine"])
		// ffstatus should be present and not "off" — the query end is "now" with
		// step > fastforward_ttl, so fast-forward should be attempted.
		require.NotEmpty(t, result["ffstatus"], "expected ffstatus in X-Trickster-Result")
		require.NotEqual(t, "off", result["ffstatus"],
			"fast-forward should be attempted for a query ending at now with step > fastforward_ttl")
	})

	// Regression test for https://github.com/trickstercache/trickster/issues/587
	// Two different POST /api/v1/query requests must return different cached results.
	t.Run("POST instant queries with different query params return different results", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		base := "http://" + tricksterAddr + "/prom1/api/v1/query"

		// First POST: query=up
		params1 := url.Values{"query": {"up"}}
		resp1, err := client.Post(base, "application/x-www-form-urlencoded",
			strings.NewReader(params1.Encode()))
		require.NoError(t, err)
		defer resp1.Body.Close()
		require.Equal(t, http.StatusOK, resp1.StatusCode)
		var pr1 promResponse
		require.NoError(t, json.NewDecoder(resp1.Body).Decode(&pr1))
		require.Equal(t, "success", pr1.Status)
		t.Logf("POST query=up: %s", resp1.Header.Get("X-Trickster-Result"))

		// Second POST: query=process_cpu_seconds_total (different metric)
		params2 := url.Values{"query": {"process_cpu_seconds_total"}}
		resp2, err := client.Post(base, "application/x-www-form-urlencoded",
			strings.NewReader(params2.Encode()))
		require.NoError(t, err)
		defer resp2.Body.Close()
		require.Equal(t, http.StatusOK, resp2.StatusCode)
		var pr2 promResponse
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&pr2))
		require.Equal(t, "success", pr2.Status)
		t.Logf("POST query=process_cpu_seconds_total: %s", resp2.Header.Get("X-Trickster-Result"))

		// The two responses must have different data (different metrics).
		require.NotEqual(t, string(pr1.Data), string(pr2.Data),
			"different POST queries must return different results (issue #587)")
	})

	t.Run("negative cache 500", func(t *testing.T) {
		// Use sim1 (Mockster) which supports status_code injection via query labels.
		params := url.Values{"query": {`test{status_code="500"}`}}
		u := "http://" + tricksterAddr + "/sim1/api/v1/query?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		resp.Body.Close()
		t.Logf("negative cache 500 first: status=%d, X-Trickster-Result=%s",
			resp.StatusCode, resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

		// Second request — should get negative cache hit
		resp2, err := http.Get(u)
		require.NoError(t, err)
		resp2.Body.Close()
		result := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
		t.Logf("negative cache 500 second: status=%d, result=%v", resp2.StatusCode, result)
		if result["status"] == "nchit" {
			t.Log("confirmed negative cache hit for 500")
		} else {
			require.Equal(t, http.StatusInternalServerError, resp2.StatusCode)
		}
	})
}
