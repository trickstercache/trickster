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

func TestPrometheus(t *testing.T) {
	h := developerHarness()
	h.start(t)
	waitForPrometheusData(t, "127.0.0.1:9090")

	runCacheProviderMatrix(t, func(t *testing.T, c cacheProviderCase) {
		t.Run("range query cache miss then hit", func(t *testing.T) {
			now := time.Now()
			params := url.Values{
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
		narrow := url.Values{
			"query": {"process_cpu_seconds_total"},
			"start": {fmt.Sprintf("%d", now.Add(-3*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		_, _ = queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", narrow)

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
		params := url.Values{"query": {"invalid_query{{{}"}}
		u := "http://" + tricksterAddr + "/prom1/api/v1/query?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		resp.Body.Close()
		t.Logf("negative cache first: status=%d, X-Trickster-Result=%s",
			resp.StatusCode, resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		resp2, err := http.Get(u)
		require.NoError(t, err)
		resp2.Body.Close()
		result := parseTricksterResult(resp2.Header.Get("X-Trickster-Result"))
		t.Logf("negative cache second: status=%d, result=%v", resp2.StatusCode, result)
		if result["status"] == "nchit" {
			t.Log("confirmed negative cache hit")
		} else {
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
			// step must exceed FastForwardTTL (default 15s) for fast-forward to activate
			"step": {"60"},
		}
		_, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("fast forward: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", result["engine"])
		require.NotEmpty(t, result["ffstatus"], "expected ffstatus in X-Trickster-Result")
		require.NotEqual(t, "off", result["ffstatus"],
			"fast-forward should be attempted for a query ending at now with step > fastforward_ttl")
	})

	t.Run("POST instant queries with different query params return different results", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		base := "http://" + tricksterAddr + "/prom1/api/v1/query"

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

		require.NotEqual(t, string(pr1.Data), string(pr2.Data),
			"different POST queries must return different results (issue #587)")
	})

	t.Run("metadata endpoint cached", func(t *testing.T) {
		params := url.Values{"limit": {"5"}}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/metadata", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("metadata: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])

		var md map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(pr.Data, &md))
		require.NotEmpty(t, md)
	})

	t.Run("format_query endpoint cached", func(t *testing.T) {
		params := url.Values{"query": {"up{job='prometheus'}"}}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/format_query", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("format_query: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])

		// Second request should be a cache hit
		_, hdr2 := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/format_query", params)
		result2 := parseTricksterResult(hdr2.Get("X-Trickster-Result"))
		require.Equal(t, "hit", result2["status"])
	})

	t.Run("parse_query endpoint cached", func(t *testing.T) {
		params := url.Values{"query": {"rate(up[5m])"}}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/parse_query", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("parse_query: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])
	})

	t.Run("scrape_pools endpoint cached", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/scrape_pools", nil)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("scrape_pools: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])

		var wrapper struct {
			ScrapePools []string `json:"scrapePools"`
		}
		require.NoError(t, json.Unmarshal(pr.Data, &wrapper))
		require.Contains(t, wrapper.ScrapePools, "prometheus")
	})

	t.Run("stats=all uses separate cache key", func(t *testing.T) {
		params := url.Values{"query": {"up"}}
		_, hdr1 := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query", params)
		result1 := parseTricksterResult(hdr1.Get("X-Trickster-Result"))
		t.Logf("query without stats: %s", hdr1.Get("X-Trickster-Result"))

		paramsStats := url.Values{"query": {"up"}, "stats": {"all"}}
		_, hdr2 := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query", paramsStats)
		result2 := parseTricksterResult(hdr2.Get("X-Trickster-Result"))
		t.Logf("query with stats=all: %s", hdr2.Get("X-Trickster-Result"))

		// The stats=all request should NOT be a cache hit from the non-stats query
		// (it should be a miss since it has a different cache key)
		if result1["status"] == "hit" || result1["status"] == "kmiss" {
			require.NotEqual(t, "hit", result2["status"],
				"stats=all query should have a different cache key")
		}
	})

	t.Run("native histogram instant query", func(t *testing.T) {
		params := url.Values{"query": {"prometheus_http_request_duration_seconds"}}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("histogram instant: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "ObjectProxyCache", result["engine"])

		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)

		// The result should contain at least one series with a "histogram" field
		// (Prometheus 3.x with native-histograms enabled + PrometheusProto scrape)
		var results []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &results))
		require.NotEmpty(t, results)

		foundHistogram := false
		for _, raw := range results {
			var entry map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &entry))
			if _, ok := entry["histogram"]; ok {
				foundHistogram = true
				break
			}
		}
		require.True(t, foundHistogram,
			"expected at least one native histogram in prometheus_http_request_duration_seconds results")
	})

	t.Run("native histogram range query", func(t *testing.T) {
		now := time.Now()
		params := url.Values{
			"query": {"prometheus_http_request_duration_seconds"},
			"start": {fmt.Sprintf("%d", now.Add(-2*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("histogram range: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", result["engine"])

		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)

		var results []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &results))
		require.NotEmpty(t, results)

		foundHistograms := false
		for _, raw := range results {
			var entry map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &entry))
			if _, ok := entry["histograms"]; ok {
				foundHistograms = true
				break
			}
		}
		require.True(t, foundHistograms,
			"expected at least one series with histograms array in range query")
	})

	t.Run("native histogram range query cache hit", func(t *testing.T) {
		now := time.Now()
		step := 15 * time.Second
		end := now.Truncate(step)
		start := end.Add(-2 * time.Minute)
		params := url.Values{
			"query": {fmt.Sprintf("prometheus_http_request_duration_seconds + 0*%d", now.UnixNano())},
			"start": {fmt.Sprintf("%d", start.Unix())},
			"end":   {fmt.Sprintf("%d", end.Unix())},
			"step":  {"15"},
		}

		// First request: cache miss
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		require.Equal(t, "success", pr.Status)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("histogram cache miss: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "kmiss", result["status"])

		// Second request: cache hit
		_, hdr2 := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		result2 := parseTricksterResult(hdr2.Get("X-Trickster-Result"))
		t.Logf("histogram cache hit: %s", hdr2.Get("X-Trickster-Result"))
		require.Equal(t, "hit", result2["status"])
	})

	t.Run("native histogram round-trip fidelity", func(t *testing.T) {
		query := fmt.Sprintf("prometheus_http_request_duration_seconds + 0*%d", time.Now().UnixNano())
		params := url.Values{"query": {query}}

		// Query directly against Prometheus
		prDirect, _ := queryTricksterProm(t, "127.0.0.1:9090", "", "/api/v1/query", params)
		// Query through Trickster (cache miss)
		prProxy, _ := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query", params)

		require.Equal(t, prDirect.Status, prProxy.Status)

		var qdDirect, qdProxy promQueryData
		require.NoError(t, json.Unmarshal(prDirect.Data, &qdDirect))
		require.NoError(t, json.Unmarshal(prProxy.Data, &qdProxy))
		require.Equal(t, qdDirect.ResultType, qdProxy.ResultType)

		var directResults, proxyResults []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(qdDirect.Result, &directResults))
		require.NoError(t, json.Unmarshal(qdProxy.Result, &proxyResults))

		// Both should have the same number of series
		require.Equal(t, len(directResults), len(proxyResults),
			"Trickster proxy should return same number of series as direct Prometheus query")

		// Count histogram vs value series in each
		directHist := 0
		for _, r := range directResults {
			if _, ok := r["histogram"]; ok {
				directHist++
			}
		}
		proxyHist := 0
		for _, r := range proxyResults {
			if _, ok := r["histogram"]; ok {
				proxyHist++
			}
		}
		require.Equal(t, directHist, proxyHist,
			"same number of native histogram series should come through proxy")
	})

	t.Run("negative cache 500", func(t *testing.T) {
		params := url.Values{"query": {`test{status_code="500"}`}}
		u := "http://" + tricksterAddr + "/sim1/api/v1/query?" + params.Encode()
		resp, err := http.Get(u)
		require.NoError(t, err)
		resp.Body.Close()
		t.Logf("negative cache 500 first: status=%d, X-Trickster-Result=%s",
			resp.StatusCode, resp.Header.Get("X-Trickster-Result"))
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

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
