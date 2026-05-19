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
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestALB_TSM_Scale(t *testing.T) {
	const (
		listenPort         = 8590
		metricsPort        = 8591
		mgmtPort           = 8592
		listenAddr         = "127.0.0.1:8590"
		backendName        = "alb-tsm-scale"
		labeledBackendName = "alb-tsm-scale-labeled"
		fgrBackendName     = "alb-fgr-scale"
		nlmBackendName     = "alb-nlm-scale"
		numBackends        = 50
		numLabeledBackends = 10
	)

	fakes := make([]*fakeProm, numBackends)
	for i := range fakes {
		fakes[i] = newFakeProm(t, fmt.Sprintf("prom-%02d", i))
	}

	cfgPath := writeScaleConfig(t, fakes, listenPort, metricsPort, mgmtPort,
		backendName, labeledBackendName, numLabeledBackends,
		fgrBackendName, nlmBackendName)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))

	rangeParams := func() url.Values {
		now := time.Now()
		return url.Values{
			"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
	}
	instantParams := func() url.Values {
		return url.Values{"query": {fmt.Sprintf("up + 0*%d", time.Now().UnixNano())}}
	}

	resetAll := func() {
		for _, f := range fakes {
			f.setBehavior(behaviorOK())
		}
	}

	t.Run("50_backends_range_query", func(t *testing.T) {
		resetAll()
		params := rangeParams()
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", params)
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)

		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.GreaterOrEqual(t, len(series), numBackends)
		t.Logf("%d series, %s", len(series), hdr.Get("X-Trickster-Result"))

		_, hdr2 := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", params)
		t.Logf("repeat: %s", hdr2.Get("X-Trickster-Result"))
	})

	t.Run("50_backends_instant_query", func(t *testing.T) {
		resetAll()
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query", instantParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result)
		t.Logf("%s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("oversized_responses_not_truncated", func(t *testing.T) {
		resetAll()
		const oversizedN = 10
		for i, f := range fakes {
			if i < oversizedN {
				f.setBehavior(behaviorOversized(80))
			} else {
				f.setBehavior(behaviorEmpty())
			}
		}
		body, hdr := rawQuery(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Greater(t, len(body), 64*1024)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		t.Logf("%d bytes, %s", len(body), hdr.Get("X-Trickster-Result"))
	})

	t.Run("backend_5xx_partial_success", func(t *testing.T) {
		resetAll()
		fakes[0].setBehavior(behaviorStatus(http.StatusInternalServerError))
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series)
		t.Logf("%d series, %s", len(series), hdr.Get("X-Trickster-Result"))
	})

	t.Run("mismatched_vector_shape", func(t *testing.T) {
		resetAll()
		fakes[0].setBehavior(behaviorBadShape())
		body, hdr := rawQueryAllowError(t, listenAddr, backendName, "/api/v1/query", instantParams())
		t.Logf("%d bytes, %s", len(body), hdr.Get("X-Trickster-Result"))
	})

	t.Run("bad_encoding_advertised_as_gzip", func(t *testing.T) {
		// Upstream claims gzip but sends plaintext; DecompressResponseBody
		// fails. The merge surfaces the partial-failure marker but the
		// remaining 49 backends still produce a successful merged response.
		resetAll()
		fakes[0].setBehavior(behaviorBadEncoding())
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series)
		t.Logf("%d series, %s", len(series), hdr.Get("X-Trickster-Result"))
	})

	t.Run("truncating_upstream_does_not_poison_cache", func(t *testing.T) {
		resetAll()
		params := rangeParams()
		fakes[0].setBehavior(behaviorTruncate())
		firstBody, firstHdr, firstSC := doRaw(t, listenAddr, backendName, "/api/v1/query_range", params)
		require.NotContains(t, string(firstBody), "runtime error")
		require.NotContains(t, string(firstBody), "goroutine ")
		t.Logf("first (truncating): status=%d, %d bytes, %s",
			firstSC, len(firstBody), firstHdr.Get("X-Trickster-Result"))

		resetAll()
		body, hdr := rawQuery(t, listenAddr, backendName, "/api/v1/query_range", params)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series)
		t.Logf("%d series, %s", len(series), hdr.Get("X-Trickster-Result"))
	})

	t.Run("concurrent_clients_collapse", func(t *testing.T) {
		resetAll()
		params := rangeParams()
		for _, f := range fakes {
			f.hits.Store(0)
		}
		const clients = 25
		var wg sync.WaitGroup
		errCh := make(chan error, clients)
		for range clients {
			wg.Go(func() {
				body, _, sc := doRaw(t, listenAddr, backendName, "/api/v1/query_range", params)
				if sc != http.StatusOK {
					errCh <- fmt.Errorf("status %d: %s", sc, body)
				}
			})
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}
		var total int64
		for _, f := range fakes {
			total += f.hits.Load()
		}
		require.LessOrEqual(t, total, int64(numBackends*3))
		t.Logf("%d clients → %d upstream fetches (expected ~%d)", clients, total, numBackends)
	})

	t.Run("slow_backend_merge", func(t *testing.T) {
		resetAll()
		fakes[0].setBehavior(behaviorSlow(150 * time.Millisecond))
		start := time.Now()
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		elapsed := time.Since(start)
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.GreaterOrEqual(t, len(series), numBackends-1)
		t.Logf("%d series in %s, %s", len(series), elapsed, hdr.Get("X-Trickster-Result"))
	})

	t.Run("client_cancel_during_merge", func(t *testing.T) {
		resetAll()
		for _, f := range fakes {
			f.setBehavior(behaviorSlow(200 * time.Millisecond))
		}
		u := "http://" + listenAddr + "/" + backendName + "/api/v1/query_range?" + rangeParams().Encode()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		resetAll()
		pr, _ := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
	})

	t.Run("all_backends_5xx", func(t *testing.T) {
		resetAll()
		for _, f := range fakes {
			f.setBehavior(behaviorStatus(http.StatusInternalServerError))
		}
		body, hdr, sc := doRaw(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.GreaterOrEqual(t, sc, 500, "expected 5xx, got %d: %s", sc, body)
		t.Logf("status=%d, %s", sc, hdr.Get("X-Trickster-Result"))
	})

	t.Run("labeled_backends_merge", func(t *testing.T) {
		resetAll()
		pr, hdr := queryTricksterProm(t, listenAddr, labeledBackendName, "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []struct {
			Metric map[string]string `json:"metric"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.GreaterOrEqual(t, len(series), numLabeledBackends)
		regions := make(map[string]struct{})
		for _, s := range series {
			r, ok := s.Metric["region"]
			require.True(t, ok, "every merged series must carry the injected region label")
			regions[r] = struct{}{}
		}
		require.GreaterOrEqual(t, len(regions), numLabeledBackends)
		t.Logf("%d series across %d regions, %s", len(series), len(regions), hdr.Get("X-Trickster-Result"))
	})

	for _, alb := range []string{fgrBackendName, nlmBackendName} {
		t.Run(alb+"_cross_mechanism", func(t *testing.T) {
			resetAll()
			pr, hdr := queryTricksterProm(t, listenAddr, alb, "/api/v1/query_range", rangeParams())
			require.Equal(t, "success", pr.Status)
			t.Logf("%s: %s", alb, hdr.Get("X-Trickster-Result"))
		})
	}

	t.Run("labels_50_partial_fail", func(t *testing.T) {
		resetAll()
		fakes[0].setBehavior(behaviorStatus(http.StatusInternalServerError))
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/labels", nil)
		require.Equal(t, "success", pr.Status)
		var labels []string
		require.NoError(t, json.Unmarshal(pr.Data, &labels))
		require.NotEmpty(t, labels)
		t.Logf("%d labels, %s", len(labels), hdr.Get("X-Trickster-Result"))
	})

	t.Run("label_values_50_fanout_oversized", func(t *testing.T) {
		resetAll()
		for _, f := range fakes {
			f.setBehavior(behaviorLabelValuesKB(2))
		}
		body, hdr, sc := doRaw(t, listenAddr, backendName, "/api/v1/label/__name__/values", nil)
		require.Equal(t, http.StatusOK, sc)
		require.Greater(t, len(body), 64*1024)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var values []string
		require.NoError(t, json.Unmarshal(pr.Data, &values))
		require.NotEmpty(t, values)
		t.Logf("%d values, %d bytes, %s", len(values), len(body), hdr.Get("X-Trickster-Result"))
	})

	t.Run("series_50_fanout", func(t *testing.T) {
		resetAll()
		now := time.Now()
		params := url.Values{
			"match[]": {"up"},
			"start":   {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":     {fmt.Sprintf("%d", now.Unix())},
		}
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/series", params)
		require.Equal(t, "success", pr.Status)
		var series []map[string]string
		require.NoError(t, json.Unmarshal(pr.Data, &series))
		require.NotEmpty(t, series)
		t.Logf("%d series, %s", len(series), hdr.Get("X-Trickster-Result"))
	})

	t.Run("post_query_range", func(t *testing.T) {
		resetAll()
		form := rangeParams().Encode()
		u := "http://" + listenAddr + "/" + backendName + "/api/v1/query_range"
		resp, err := http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.GreaterOrEqual(t, len(series), numBackends)
		t.Logf("POST: %d series, %s", len(series), resp.Header.Get("X-Trickster-Result"))
	})

	t.Run("variable_refresh_burst", func(t *testing.T) {
		resetAll()
		const vars = 10
		var wg sync.WaitGroup
		errCh := make(chan error, vars)
		for i := range vars {
			wg.Go(func() {
				path := fmt.Sprintf("/api/v1/label/var%d/values", i)
				body, _, sc := doRaw(t, listenAddr, backendName, path, nil)
				if sc != http.StatusOK {
					errCh <- fmt.Errorf("var%d status %d: %s", i, sc, body)
				}
			})
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}
	})

	t.Run("post_concurrent_clients_safe", func(t *testing.T) {
		// POST query_range fans out N clones of the same parent request. The
		// body must be re-readable per clone or the upstreams see truncated
		// requests. Race the request body cache under -race to catch any
		// unsynchronized r.Body mutation in the clone path.
		resetAll()
		form := rangeParams().Encode()
		u := "http://" + listenAddr + "/" + backendName + "/api/v1/query_range"
		const clients = 25
		var wg sync.WaitGroup
		errCh := make(chan error, clients)
		for range clients {
			wg.Go(func() {
				resp, err := http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form))
				if err != nil {
					errCh <- err
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errCh <- fmt.Errorf("status %d", resp.StatusCode)
					return
				}
				body, _ := io.ReadAll(resp.Body)
				var pr promResponse
				if err := json.Unmarshal(body, &pr); err != nil {
					errCh <- fmt.Errorf("invalid json: %w", err)
					return
				}
				if pr.Status != "success" {
					errCh <- fmt.Errorf("non-success: %s", pr.Status)
				}
			})
		}
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}
	})

	t.Run("error_body_passthrough", func(t *testing.T) {
		resetAll()
		for _, f := range fakes {
			f.setBehavior(behaviorErrJSON())
		}
		path := fmt.Sprintf("/api/v1/label/errprobe%d/values", time.Now().UnixNano())
		body, hdr, sc := doRaw(t, listenAddr, backendName, path, nil)
		require.GreaterOrEqual(t, sc, 400, "body=%s", body)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr),
			"merged error body must still be parseable Prometheus error JSON")
		require.Equal(t, "error", pr.Status)
		t.Logf("status=%d, %s", sc, hdr.Get("X-Trickster-Result"))
	})
}

func TestALB_TSM_RealProm_Scale(t *testing.T) {
	const (
		listenPort  = 8690
		metricsPort = 8691
		mgmtPort    = 8692
		listenAddr  = "127.0.0.1:8690"
		promAddr    = "127.0.0.1:9090"
		backendName = "alb-tsm-real-scale"
		numShards   = 50
	)

	cfgPath := writeRealPromScaleConfig(t, listenPort, metricsPort, mgmtPort,
		promAddr, backendName, numShards)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))
	waitForPrometheusData(t, promAddr)

	uniq := func() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }
	rangeParams := func() url.Values {
		now := time.Now()
		return url.Values{
			"query": {"up + 0*" + uniq()},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
	}

	t.Run("range_query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		var series []struct {
			Metric map[string]string `json:"metric"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		shards := make(map[string]struct{})
		for _, s := range series {
			if v, ok := s.Metric["shard"]; ok {
				shards[v] = struct{}{}
			}
		}
		require.GreaterOrEqual(t, len(shards), numShards,
			"merged matrix must carry %d distinct shard labels, got %d", numShards, len(shards))
		t.Logf("%d series across %d shards, %s", len(series), len(shards), hdr.Get("X-Trickster-Result"))
	})

	t.Run("instant_query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query",
			url.Values{"query": {"up + 0*" + uniq()}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result)
		t.Logf("%s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("labels", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/labels", nil)
		require.Equal(t, "success", pr.Status)
		var labels []string
		require.NoError(t, json.Unmarshal(pr.Data, &labels))
		require.Contains(t, labels, "shard")
		t.Logf("%d labels, %s", len(labels), hdr.Get("X-Trickster-Result"))
	})

	t.Run("label_values_shard", func(t *testing.T) {
		body, hdr, sc := doRaw(t, listenAddr, backendName, "/api/v1/label/shard/values", nil)
		require.Equal(t, http.StatusOK, sc)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var values []string
		require.NoError(t, json.Unmarshal(pr.Data, &values))
		t.Logf("%d values, %s", len(values), hdr.Get("X-Trickster-Result"))
	})

	t.Run("series", func(t *testing.T) {
		now := time.Now()
		params := url.Values{
			"match[]": {"up"},
			"start":   {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":     {fmt.Sprintf("%d", now.Unix())},
		}
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/series", params)
		require.Equal(t, "success", pr.Status)
		var series []map[string]string
		require.NoError(t, json.Unmarshal(pr.Data, &series))
		require.NotEmpty(t, series)
		t.Logf("%d series, %s", len(series), hdr.Get("X-Trickster-Result"))
	})

	t.Run("post_query_range", func(t *testing.T) {
		form := rangeParams().Encode()
		u := "http://" + listenAddr + "/" + backendName + "/api/v1/query_range"
		resp, err := http.Post(u, "application/x-www-form-urlencoded", strings.NewReader(form))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series)
		t.Logf("POST: %d series, %s", len(series), resp.Header.Get("X-Trickster-Result"))
	})
}

type fakeProm struct {
	srv      *httptest.Server
	label    string
	behavior atomic.Pointer[promBehavior]
	hits     atomic.Int64
}

type promBehavior struct {
	mode     string
	status   int
	seriesKB int
	delay    time.Duration
}

func behaviorOK() *promBehavior    { return &promBehavior{mode: "ok"} }
func behaviorEmpty() *promBehavior { return &promBehavior{mode: "empty"} }
func behaviorOversized(kb int) *promBehavior {
	return &promBehavior{mode: "oversized", seriesKB: kb}
}
func behaviorStatus(code int) *promBehavior {
	return &promBehavior{mode: "status", status: code}
}
func behaviorBadShape() *promBehavior            { return &promBehavior{mode: "badshape"} }
func behaviorTruncate() *promBehavior            { return &promBehavior{mode: "truncate"} }
func behaviorSlow(d time.Duration) *promBehavior { return &promBehavior{mode: "ok", delay: d} }
func behaviorErrJSON() *promBehavior             { return &promBehavior{mode: "errjson"} }
func behaviorBadEncoding() *promBehavior         { return &promBehavior{mode: "badencoding"} }
func behaviorLabelValuesKB(kb int) *promBehavior {
	return &promBehavior{mode: "labelvalues", seriesKB: kb}
}

func newFakeProm(t *testing.T, label string) *fakeProm {
	t.Helper()
	f := &fakeProm{label: label}
	f.behavior.Store(behaviorOK())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query_range", f.handleRange)
	mux.HandleFunc("/api/v1/query", f.handleInstant)
	mux.HandleFunc("/api/v1/labels", f.handleLabels)
	mux.HandleFunc("/api/v1/label/", f.handleLabelValues)
	mux.HandleFunc("/api/v1/series", f.handleSeries)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProm) URL() string                 { return f.srv.URL }
func (f *fakeProm) setBehavior(b *promBehavior) { f.behavior.Store(b) }

func (f *fakeProm) handleRange(w http.ResponseWriter, _ *http.Request) {
	f.hits.Add(1)
	b := f.behavior.Load()
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	switch b.mode {
	case "status":
		http.Error(w, "fake error", b.status)
		return
	case "empty":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		return
	case "oversized":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buildOversizedMatrix(f.label, b.seriesKB))
		return
	case "truncate":
		full := buildMatrixBody(f.label)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(full)))
		cut := len(full) / 3
		_, _ = w.Write(full[:cut])
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	case "badencoding":
		// Advertise gzip but send plaintext. The TSM gather path's
		// DecompressResponseBody will fail, surfacing as a partial-failure.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buildMatrixBody(f.label))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buildMatrixBody(f.label))
}

func (f *fakeProm) handleInstant(w http.ResponseWriter, _ *http.Request) {
	f.hits.Add(1)
	b := f.behavior.Load()
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	switch b.mode {
	case "status":
		http.Error(w, "fake error", b.status)
		return
	case "empty":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		return
	case "badshape":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buildMatrixBody(f.label))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buildVectorBody(f.label))
}

func (f *fakeProm) handleLabels(w http.ResponseWriter, _ *http.Request) {
	f.hits.Add(1)
	b := f.behavior.Load()
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	switch b.mode {
	case "status":
		http.Error(w, "fake error", b.status)
		return
	case "errjson":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(buildPromErrBody("bad_data", "invalid parameter"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(fmt.Appendf(nil,
		`{"status":"success","data":["__name__","job","instance","label_%s"]}`, f.label))
}

func (f *fakeProm) handleLabelValues(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	b := f.behavior.Load()
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	switch b.mode {
	case "status":
		http.Error(w, "fake error", b.status)
		return
	case "errjson":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(buildPromErrBody("bad_data", "invalid label name"))
		return
	case "labelvalues":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buildOversizedLabelValues(f.label, b.seriesKB))
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/label/"), "/values")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(fmt.Appendf(nil,
		`{"status":"success","data":["value-%s-%s-a","value-%s-%s-b"]}`,
		name, f.label, name, f.label))
}

func (f *fakeProm) handleSeries(w http.ResponseWriter, _ *http.Request) {
	f.hits.Add(1)
	b := f.behavior.Load()
	if b.delay > 0 {
		time.Sleep(b.delay)
	}
	switch b.mode {
	case "status":
		http.Error(w, "fake error", b.status)
		return
	case "errjson":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(buildPromErrBody("bad_data", "invalid matcher"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(fmt.Appendf(nil,
		`{"status":"success","data":[{"__name__":"up","job":"fake","instance":%q}]}`, f.label))
}

func buildPromErrBody(errType, msg string) []byte {
	return fmt.Appendf(nil,
		`{"status":"error","errorType":%q,"error":%q}`, errType, msg)
}

func buildOversizedLabelValues(label string, targetKB int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"status":"success","data":[`)
	target := targetKB * 1024
	for i := 0; sb.Len() < target; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"value-%s-%06d"`, label, i)
	}
	sb.WriteString("]}")
	return []byte(sb.String())
}

func buildVectorBody(instance string) []byte {
	return fmt.Appendf(nil,
		`{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"__name__":"up","job":"fake","instance":%q},`+
			`"value":[%d,"1"]}]}}`,
		instance, time.Now().Unix())
}

func buildMatrixBody(instance string) []byte {
	now := time.Now().Unix()
	var sb strings.Builder
	sb.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	sb.WriteString(fmt.Sprintf(
		`{"metric":{"__name__":"up","job":"fake","instance":%q},"values":[`, instance))
	for i := range 5 {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(fmt.Sprintf(`[%d,"1"]`, now-int64(15*(4-i))))
	}
	sb.WriteString("]}]}}")
	return []byte(sb.String())
}

func buildOversizedMatrix(instance string, targetKB int) []byte {
	now := time.Now().Unix()
	var sb strings.Builder
	sb.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	first := true
	target := targetKB * 1024
	for series := 0; sb.Len() < target; series++ {
		if !first {
			sb.WriteString(",")
		}
		first = false
		sb.WriteString(fmt.Sprintf(
			`{"metric":{"__name__":"up","job":"fake","instance":%q,"shard":"s%d"},"values":[`,
			instance, series))
		for i := range 30 {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString(fmt.Sprintf(`[%d,"%d"]`, now-int64(15*(29-i)), i))
		}
		sb.WriteString("]}")
	}
	sb.WriteString("]}}")
	return []byte(sb.String())
}

func rawQuery(t *testing.T, address, backend, path string, params url.Values) ([]byte, http.Header) {
	t.Helper()
	body, hdr, status := doRaw(t, address, backend, path, params)
	require.Equal(t, http.StatusOK, status, "unexpected status %d: %s", status, body)
	return body, hdr
}

func rawQueryAllowError(t *testing.T, address, backend, path string, params url.Values) ([]byte, http.Header) {
	t.Helper()
	body, hdr, _ := doRaw(t, address, backend, path, params)
	return body, hdr
}

func doRaw(t *testing.T, address, backend, path string, params url.Values) ([]byte, http.Header, int) {
	t.Helper()
	u := "http://" + address + "/" + backend + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	client := &http.Client{
		Transport: &http.Transport{DisableCompression: true},
		Timeout:   30 * time.Second,
	}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gr, err := gzip.NewReader(resp.Body)
		require.NoError(t, err)
		defer gr.Close()
		reader = gr
	}
	b, err := io.ReadAll(reader)
	require.NoError(t, err)
	return b, resp.Header.Clone(), resp.StatusCode
}

func writeScaleConfig(t *testing.T, fakes []*fakeProm,
	listenPort, metricsPort, mgmtPort int,
	albName, labeledAlbName string, labeledN int,
	fgrAlbName, nlmAlbName string) string {
	t.Helper()
	var sb strings.Builder
	fmt.Fprintf(&sb, "frontend:\n  listen_port: %d\n", listenPort)
	fmt.Fprintf(&sb, "metrics:\n  listen_port: %d\n", metricsPort)
	fmt.Fprintf(&sb, "mgmt:\n  listen_port: %d\n", mgmtPort)
	sb.WriteString("logging:\n  log_level: info\n")
	sb.WriteString("caches:\n  mem:\n    provider: memory\n")
	sb.WriteString("backends:\n")
	for i, f := range fakes {
		fmt.Fprintf(&sb, "  prom%d:\n", i)
		sb.WriteString("    provider: prometheus\n")
		fmt.Fprintf(&sb, "    origin_url: %s\n", f.URL())
		sb.WriteString("    cache_name: mem\n")
	}
	for i := range labeledN {
		fmt.Fprintf(&sb, "  prom-lab-%d:\n", i)
		sb.WriteString("    provider: prometheus\n")
		fmt.Fprintf(&sb, "    origin_url: %s\n", fakes[i].URL())
		sb.WriteString("    cache_name: mem\n")
		sb.WriteString("    prometheus:\n")
		sb.WriteString("      labels:\n")
		fmt.Fprintf(&sb, "        region: region-%d\n", i)
	}
	fmt.Fprintf(&sb, "  %s:\n", albName)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	sb.WriteString("      mechanism: tsm\n")
	sb.WriteString("      pool:\n")
	for i := range fakes {
		fmt.Fprintf(&sb, "        - prom%d\n", i)
	}
	fmt.Fprintf(&sb, "  %s:\n", labeledAlbName)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	sb.WriteString("      mechanism: tsm\n")
	sb.WriteString("      pool:\n")
	for i := range labeledN {
		fmt.Fprintf(&sb, "        - prom-lab-%d\n", i)
	}
	for _, entry := range []struct{ name, mech string }{
		{fgrAlbName, "fgr"},
		{nlmAlbName, "nlm"},
	} {
		fmt.Fprintf(&sb, "  %s:\n", entry.name)
		sb.WriteString("    provider: alb\n")
		sb.WriteString("    alb:\n")
		fmt.Fprintf(&sb, "      mechanism: %s\n", entry.mech)
		sb.WriteString("      pool:\n")
		for i := range fakes {
			fmt.Fprintf(&sb, "        - prom%d\n", i)
		}
	}
	path := filepath.Join(t.TempDir(), "alb-tsm-scale.yaml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o644))
	return path
}

func writeRealPromScaleConfig(t *testing.T, listenPort, metricsPort, mgmtPort int,
	promAddr, albName string, numShards int) string {
	t.Helper()
	var sb strings.Builder
	fmt.Fprintf(&sb, "frontend:\n  listen_port: %d\n", listenPort)
	fmt.Fprintf(&sb, "metrics:\n  listen_port: %d\n", metricsPort)
	fmt.Fprintf(&sb, "mgmt:\n  listen_port: %d\n", mgmtPort)
	sb.WriteString("logging:\n  log_level: info\n")
	sb.WriteString("caches:\n  mem:\n    provider: memory\n")
	sb.WriteString("backends:\n")
	for i := range numShards {
		fmt.Fprintf(&sb, "  prom-real-%d:\n", i)
		sb.WriteString("    provider: prometheus\n")
		fmt.Fprintf(&sb, "    origin_url: http://%s\n", promAddr)
		sb.WriteString("    cache_name: mem\n")
		sb.WriteString("    prometheus:\n")
		sb.WriteString("      labels:\n")
		fmt.Fprintf(&sb, "        shard: shard-%02d\n", i)
	}
	fmt.Fprintf(&sb, "  %s:\n", albName)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	sb.WriteString("      mechanism: tsm\n")
	sb.WriteString("      pool:\n")
	for i := range numShards {
		fmt.Fprintf(&sb, "        - prom-real-%d\n", i)
	}
	path := filepath.Join(t.TempDir(), "alb-tsm-real-scale.yaml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o644))
	return path
}
