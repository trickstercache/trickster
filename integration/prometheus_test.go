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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tricksterAddr = "localhost:8480"

// TestPrometheus tests Prometheus-specific capabilities through Trickster.
// Requires: make developer-start && a running trickster with the developer config.
func TestPrometheus(t *testing.T) {
	// Ensure trickster is running with the developer config.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "../docs/developer/environment/trickster-config/trickster.yaml")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get("http://localhost:8481/metrics")
		if !assert.NoError(collect, err) {
			return
		}
		resp.Body.Close()
		assert.Equal(collect, 200, resp.StatusCode)
	}, 10*time.Second, 250*time.Millisecond, "trickster did not become ready")

	t.Run("range query cache miss then hit", func(t *testing.T) {
		now := time.Now()
		params := url.Values{
			"query": {"up"},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
		// First request: expect cache miss
		pr, hdr := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("first request: %s", hdr.Get("X-Trickster-Result"))
		require.Equal(t, "DeltaProxyCache", result["engine"])
		require.Equal(t, "kmiss", result["status"])

		// Second identical request: expect cache hit
		_, hdr2 := queryTricksterProm(t, tricksterAddr, "prom1", "/api/v1/query_range", params)
		result2 := parseTricksterResult(hdr2.Get("X-Trickster-Result"))
		t.Logf("second request: %s", hdr2.Get("X-Trickster-Result"))
		require.Equal(t, "hit", result2["status"])
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
}

const albAddr = "localhost:8490"

// TestPrometheusALB tests ALB mechanisms with Prometheus backends.
// Requires: make developer-start (for Prometheus on :9090).
func TestPrometheusALB(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/alb.yaml")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := http.Get("http://localhost:8491/metrics")
		if !assert.NoError(collect, err) {
			return
		}
		resp.Body.Close()
		assert.Equal(collect, 200, resp.StatusCode)
	}, 10*time.Second, 250*time.Millisecond, "trickster did not become ready")

	rangeParams := func() url.Values {
		now := time.Now()
		return url.Values{
			"query": {"up"},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
	}

	t.Run("fgr range query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fgr", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("fgr range: %s", hdr.Get("X-Trickster-Result"))
		require.NotEmpty(t, result["engine"])

		// Second request: should benefit from caching
		_, hdr2 := queryTricksterProm(t, albAddr, "alb-fgr", "/api/v1/query_range", rangeParams())
		t.Logf("fgr range (repeat): %s", hdr2.Get("X-Trickster-Result"))
	})

	t.Run("fgr instant query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fgr", "/api/v1/query", url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		t.Logf("fgr instant: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("rr multiple requests", func(t *testing.T) {
		for i := range 3 {
			pr, hdr := queryTricksterProm(t, albAddr, "alb-rr", "/api/v1/query_range", rangeParams())
			require.Equal(t, "success", pr.Status)
			require.NotEmpty(t, hdr.Get("X-Trickster-Result"))
			t.Logf("rr request %d: %s", i, hdr.Get("X-Trickster-Result"))
		}
	})

	t.Run("tsm range query merges", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("tsm range: %s", hdr.Get("X-Trickster-Result"))
		require.NotEmpty(t, result["engine"])
	})

	t.Run("tsm instant query", func(t *testing.T) {
		// Regression test for https://github.com/trickstercache/trickster/issues/937
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/query", url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result, "instant query through TSM should return non-empty result")
		t.Logf("tsm instant: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("tsm labels merge", func(t *testing.T) {
		// Regression test for https://github.com/trickstercache/trickster/issues/936
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/labels", nil)
		require.Equal(t, "success", pr.Status)
		var labels []string
		require.NoError(t, json.Unmarshal(pr.Data, &labels), "labels through TSM should return valid JSON array")
		require.Contains(t, labels, "job")
		require.Contains(t, labels, "__name__")
		t.Logf("tsm labels: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("tsm label values merge", func(t *testing.T) {
		// Regression test for https://github.com/trickstercache/trickster/issues/936
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/label/job/values", nil)
		require.Equal(t, "success", pr.Status)
		var values []string
		require.NoError(t, json.Unmarshal(pr.Data, &values), "label values through TSM should return valid JSON array")
		require.Contains(t, values, "prometheus")
		t.Logf("tsm label values: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("nlm range query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-nlm", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		t.Logf("nlm range: %s", hdr.Get("X-Trickster-Result"))
	})
}
