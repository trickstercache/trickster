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
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The tests in this file target ALB regression and expansion coverage
// using testdata/alb.yaml. Each top-level TestALB_* function boots its
// own Trickster instance (daemon.Start is mutex-serialized) and drains
// it via context cancellation in t.Cleanup. Tests in the same package
// run sequentially by default, so the shared 8490/8491/8492 listeners
// do not conflict.

// startALB boots a Trickster instance using testdata/alb.yaml and
// waits until the metrics endpoint and Prometheus origin are ready.
func startALB(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/alb.yaml")
	waitForTrickster(t, "127.0.0.1:8491")
	waitForPrometheusData(t, "127.0.0.1:9090")
}

// rangeParams returns a 5-minute range-query param set centered on now.
func rangeParams() url.Values {
	now := time.Now()
	return url.Values{
		"query": {"up"},
		"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
		"end":   {fmt.Sprintf("%d", now.Unix())},
		"step":  {"15"},
	}
}

// TestALB_FR exercises the "first_response" ALB mechanism.
// FR fans out to every pool member and serves the first response that
// comes back, regardless of HTTP status. With two identical Prometheus
// members, either response is valid so we only assert success shape.
func TestALB_FR(t *testing.T) {
	startALB(t)

	t.Run("range query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fr", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		require.NotEmpty(t, qd.Result, "fr range query should return a non-empty matrix")
		t.Logf("fr range: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("instant query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fr", "/api/v1/query",
			url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result, "fr instant query should return a non-empty vector")
		t.Logf("fr instant: %s", hdr.Get("X-Trickster-Result"))
	})
}

// TestALB_UR exercises the "user_router" ALB mechanism. Basic-auth
// credentials observed by the Trickster authenticator select the
// destination backend; the two routed destinations inject distinct
// prometheus labels (region=us-east vs region=us-west) so the assertion
// can prove both branches resolved as expected.
func TestALB_UR(t *testing.T) {
	startALB(t)

	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}

	doQuery := func(t *testing.T, authz string) (promQueryData, http.Header) {
		t.Helper()
		u := "http://" + albAddr + "/alb-ur/api/v1/query?query=up"
		req, err := http.NewRequest(http.MethodGet, u, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", authz)
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"expected 200 from alb-ur, got %d", resp.StatusCode)
		var pr promResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		return qd, resp.Header.Clone()
	}

	// Each returned series carries a "region" metric label, injected by
	// prometheus.labels on the destination prom*-labeled backend.
	extractRegions := func(t *testing.T, qd promQueryData) map[string]bool {
		var series []struct {
			Metric map[string]string `json:"metric"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series, "alb-ur query must return at least one series")
		regions := make(map[string]bool)
		for _, s := range series {
			regions[s.Metric["region"]] = true
		}
		return regions
	}

	t.Run("alice routes to prom1-labeled (us-east)", func(t *testing.T) {
		qd, hdr := doQuery(t, basic("alice", "alicepw"))
		regions := extractRegions(t, qd)
		require.True(t, regions["us-east"],
			"alice should be routed to prom1-labeled (region=us-east); got %v", regions)
		require.False(t, regions["us-west"],
			"alice must not see prom2-labeled's us-west label; got %v", regions)
		t.Logf("ur alice: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("bob routes to prom2-labeled (us-west)", func(t *testing.T) {
		qd, hdr := doQuery(t, basic("bob", "bobpw"))
		regions := extractRegions(t, qd)
		require.True(t, regions["us-west"],
			"bob should be routed to prom2-labeled (region=us-west); got %v", regions)
		require.False(t, regions["us-east"],
			"bob must not see prom1-labeled's us-east label; got %v", regions)
		t.Logf("ur bob: %s", hdr.Get("X-Trickster-Result"))
	})
}

// TestALB_HeaderPropagation verifies that custom response headers set
// on the inner Prometheus backend path survive the ALB FGR transform
// path when the backend has prometheus.labels configured (which enables
// the hasTransformations capture pipeline). Both labeled pool members
// advertise X-Test-Origin=prom1 via a path override so the fanout winner
// is irrelevant.
//
// regression: #970
func TestALB_HeaderPropagation(t *testing.T) {
	startALB(t)

	t.Run("fgr instant query propagates X-Test-Origin", func(t *testing.T) {
		backend := "alb-fgr-labeled"
		_, hdr := queryTricksterProm(t, albAddr, backend, "/api/v1/query",
			url.Values{"query": {"up"}})
		require.Equal(t, "prom1", hdr.Get("X-Test-Origin"),
			"%s: backend-emitted response headers must survive the ALB FGR transform path (issue #970)",
			backend)
		t.Logf("%s headers: %v", backend, hdr)
	})

	t.Run("tsm instant query propagates X-Test-Origin", func(t *testing.T) {
		backend := "alb-tsm-labeled"
		_, hdr := queryTricksterProm(t, albAddr, backend, "/api/v1/query",
			url.Values{"query": {"up"}})
		require.Equal(t, "prom1", hdr.Get("X-Test-Origin"),
			"%s: TSM merge path must preserve custom response headers from pool members (issue #970)",
			backend)
		t.Logf("%s headers: %v", backend, hdr)
	})
}

// TestALB_TSM_AggregationMerge is a regression test for the TSM per-query
// merge + label strip behavior. A `sum by (job) (up)` aggregation query
// through an alb-tsm-labeled ALB (where each pool member injects a
// distinct `region` label) used to produce empty or broken results
// because the injected region label defeated the aggregation hash
// equality during the per-query merge. The merge path strips the
// injected labels before hashing so aggregation rows collapse properly.
//
// The Prometheus backend's TSMMergeProvider auto-classifies any outer
// `sum` / `count` / `min` / `max` / `avg` aggregator as a non-dedup
// strategy, so no ALB-side config knob is needed — the strip path
// activates automatically for this query.
//
// regression: #956
func TestALB_TSM_AggregationMerge(t *testing.T) {
	startALB(t)

	// Range query so ParseTimeRangeQuery populates rsc.TimeRangeQuery for
	// the TSM merge-strategy classifier. Nonce in the inner `up` expression
	// forces a unique cache key. The outer `sum by (job)` is what triggers
	// MergeStrategySum and the strip-injected-labels path.
	now := time.Now()
	queryExpr := fmt.Sprintf("sum by (job) (up + 0*%d)", now.UnixNano())
	rangeVals := url.Values{
		"query": {queryExpr},
		"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
		"end":   {fmt.Sprintf("%d", now.Unix())},
		"step":  {"60"},
	}
	pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm-labeled", "/api/v1/query_range", rangeVals)
	require.Equal(t, "success", pr.Status)
	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	require.Equal(t, "matrix", qd.ResultType)
	require.NotEmpty(t, qd.Result,
		"tsm merge must return a non-empty result for `sum by (job) (up)` (issue #956)")

	var series []struct {
		Metric map[string]string `json:"metric"`
		Value  []any             `json:"value"`
	}
	require.NoError(t, json.Unmarshal(qd.Result, &series))
	require.NotEmpty(t, series,
		"aggregation merge must not drop all rows (issue #956)")

	// Each result series must carry the `job` grouping label and must NOT
	// carry the `region` label — the strip path is supposed to remove
	// injected labels before the per-query merge so backends' series hash
	// identically and aggregation collapses duplicates.
	jobs := make(map[string]int)
	for _, s := range series {
		require.NotEmpty(t, s.Metric["job"],
			"each `sum by (job)` result series must carry a job label (issue #956); got %v",
			s.Metric)
		require.Empty(t, s.Metric["region"],
			"aggregation merge must strip injected labels before hashing; "+
				"series still carries region=%q (issue #956)", s.Metric["region"])
		jobs[s.Metric["job"]]++
	}

	// Collapse check: each distinct job must appear EXACTLY once in the
	// merged result. Before the fix, the injected region label caused the
	// two labeled pool members' rows to hash differently and the merge
	// would emit one row per (job, region) pair instead of collapsing to
	// one row per job.
	for job, n := range jobs {
		require.Equal(t, 1, n,
			"job=%q appears %d times in sum-by aggregation; rows failed to collapse (issue #956)",
			job, n)
	}
	t.Logf("tsm aggregation merge: %s  (%d distinct jobs)", hdr.Get("X-Trickster-Result"), len(jobs))
}

// TestALB_TSM_DeflateOrigin starts an httptest.Server on the reserved
// loopback port 18500 that serves a Prometheus-shaped instant-query
// response with `Content-Encoding: deflate`. It then issues a TSM
// instant query through alb-tsm-deflate (whose pool contains the
// deflate origin and a real Prometheus origin) and asserts the TSM
// merge path decodes the non-gzip compressed body.
//
// regression: #938
func TestALB_TSM_DeflateOrigin(t *testing.T) {
	// Build a valid Prometheus instant-query body and deflate-encode it.
	body := `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up","job":"fake","instance":"deflate:1"},` +
		`"value":[1700000000,"1"]}]}}`
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = fw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	deflated := buf.Bytes()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "deflate")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(deflated)
	})
	// Fallback for any other probe/path the backend may touch.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})

	// Bind the httptest server to the reserved worktree port so alb.yaml's
	// hardcoded origin_url matches the real listener.
	l, err := net.Listen("tcp", "127.0.0.1:18500")
	if err != nil {
		t.Skipf("TODO(#938): port 18500 unavailable (%v); deflate-origin test skipped", err)
		return
	}
	srv := &httptest.Server{
		Listener: l,
		Config:   &http.Server{Handler: mux},
	}
	srv.Start()
	t.Cleanup(srv.Close)

	startALB(t)

	pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm-deflate", "/api/v1/query",
		url.Values{"query": {"up"}})
	require.Equal(t, "success", pr.Status)
	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	require.Equal(t, "vector", qd.ResultType)
	require.NotEmpty(t, qd.Result,
		"tsm merge must decode deflate-encoded upstream and return a non-empty merged vector (issue #938)")
	t.Logf("tsm deflate origin: %s", hdr.Get("X-Trickster-Result"))
}

// TestALB_FR_CancelRace exercises the FR fanout path under aggressive
// client cancellation. Issue #945 was a write-after-return race where a
// slower pool member could attempt to write to the ResponseWriter after
// ServeHTTP had already returned via ctx.Done(). This test makes N
// rapid-cancel requests and asserts no panics are observed. The test is
// only meaningful with `go test -race`; without -race it merely proves
// there is no panic.
//
// regression: #945
func TestALB_FR_CancelRace(t *testing.T) {
	startALB(t)

	const iterations = 50
	u := "http://" + albAddr + "/alb-fr/api/v1/query?query=up"

	var wg sync.WaitGroup
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				cancel()
				return
			}
			// Immediately cancel mid-flight; the goal is to race the ALB
			// fanout serve() path against the request context.Done() path.
			go cancel()
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	// A surviving process proves no panic fired on any fanout goroutine.
	// Run under `go test -race` for full coverage of the write-after-return
	// data race on w / wmu / returned in fr.ServeHTTP.
}
