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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestALB_TSM_Scale exercises the ALB+TSM mechanism end-to-end against many
// in-process fake Prometheus backends with configurable fault behaviors.
// It complements TestALB (2 backends, real Prometheus) by covering the
// high-fanout scenarios reported by users running ALB+TSM with ~50 backends,
// and pins down regression coverage for issues #937, #976, and #977 at the
// integration layer rather than only in unit tests.
func TestALB_TSM_Scale(t *testing.T) {
	const (
		listenPort  = 8590
		metricsPort = 8591
		mgmtPort    = 8592
		listenAddr  = "127.0.0.1:8590"
		backendName = "alb-tsm-scale"
		numBackends = 50
	)

	fakes := make([]*fakeProm, numBackends)
	for i := range fakes {
		fakes[i] = newFakeProm(t, fmt.Sprintf("prom-%02d", i))
	}

	cfgPath := writeScaleConfig(t, fakes, listenPort, metricsPort, mgmtPort, backendName)

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

	// 1. 50-backend range query: large fanout, then served from cache.
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
		require.GreaterOrEqual(t, len(series), numBackends,
			"TSM merge must surface at least one series per pool member")
		t.Logf("50 backends merge: %d series, X-Trickster-Result=%q", len(series),
			hdr.Get("X-Trickster-Result"))

		// Repeat: should be a cache hit on the merged document.
		_, hdr2 := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", params)
		t.Logf("50 backends merge (repeat): %s", hdr2.Get("X-Trickster-Result"))
	})

	// 2. 50-backend instant query (regression #937 at scale).
	t.Run("50_backends_instant_query", func(t *testing.T) {
		resetAll()
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query", instantParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType,
			"TSM instant merge must emit vector envelope, not matrix (#937)")
		require.NotEmpty(t, qd.Result)
		t.Logf("50 backends instant: %s", hdr.Get("X-Trickster-Result"))
	})

	// 3. Oversized merged response > 32KB (regression #976).
	// CaptureResponseWriter.Write returned cumulative length pre-fix,
	// causing io.Copy to abort the second 32KB chunk silently.
	t.Run("oversized_responses_not_truncated", func(t *testing.T) {
		resetAll()
		// 10 backends, each emitting ~8KB of distinct series → ~80KB merged.
		const oversizedN = 10
		for i, f := range fakes {
			if i < oversizedN {
				f.setBehavior(behaviorOversized(80))
			} else {
				f.setBehavior(behaviorEmpty())
			}
		}
		body, hdr := rawQuery(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Greater(t, len(body), 64*1024,
			"merged response should exceed 64KB; truncation past 32KB indicates #976 regression")
		// Response must be valid JSON end-to-end (truncation breaks JSON).
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr),
			"merged body must be parseable JSON; partial-write truncation breaks parsing (#976)")
		require.Equal(t, "success", pr.Status)
		t.Logf("oversized merge: %d bytes, X-Trickster-Result=%q", len(body),
			hdr.Get("X-Trickster-Result"))
	})

	// 4. One backend returns 500: TSM merge should still succeed with the rest.
	t.Run("backend_5xx_partial_success", func(t *testing.T) {
		resetAll()
		fakes[0].setBehavior(behaviorStatus(http.StatusInternalServerError))
		pr, hdr := queryTricksterProm(t, listenAddr, backendName, "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series, "TSM should merge surviving N-1 backends despite one 500")
		t.Logf("partial-success: %d series, X-Trickster-Result=%q", len(series),
			hdr.Get("X-Trickster-Result"))
	})

	// 5. Mismatched vector shape: one backend returns matrix on instant query.
	// Verifies ALB+TSM does not panic on the heterogenous-shape path (#937 hardening).
	t.Run("mismatched_vector_shape", func(t *testing.T) {
		resetAll()
		fakes[0].setBehavior(behaviorBadShape())
		// Don't assert success — just that the request returns without crashing
		// the daemon. A panic in TSM merge would tear down subsequent tests.
		body, hdr := rawQueryAllowError(t, listenAddr, backendName, "/api/v1/query", instantParams())
		t.Logf("mismatched shape: %d bytes, X-Trickster-Result=%q", len(body),
			hdr.Get("X-Trickster-Result"))
	})

	// 6. Truncating upstream + cache: cache must not be poisoned (#977).
	// First request hits a truncating backend; if the bug were live the
	// truncated bytes would be cached as a "complete" document. After
	// restoring the backend, a second request must return full data.
	//
	// FOLLOW-UP: on origin/main this scenario triggers a nil-pointer panic
	// at deltaproxycache.go:429 (cts.Clone on nil cts) when the upstream
	// body read fails mid-stream. PR #977 addresses the cache-poisoning
	// half but the panic on the read-error path is a separate defect.
	// Re-enable this sub-test once that panic is fixed.
	t.Run("truncating_upstream_does_not_poison_cache", func(t *testing.T) {
		resetAll()
		params := rangeParams() // unique query → fresh cache key
		fakes[0].setBehavior(behaviorTruncate())

		// Best-effort: pull whatever we can from the poisoned-or-not first hit.
		_, _ = rawQueryAllowError(t, listenAddr, backendName, "/api/v1/query_range", params)

		// Restore and re-query with the same params: must NOT serve a
		// truncated cache hit; response must be a complete merged matrix.
		resetAll()
		body, hdr := rawQuery(t, listenAddr, backendName, "/api/v1/query_range", params)
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr),
			"second request must return complete JSON; truncated cache entry would break parsing (#977)")
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		var series []json.RawMessage
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series, "post-recovery query must surface a non-empty merged matrix")
		t.Logf("post-truncation recovery: %d series, X-Trickster-Result=%q", len(series),
			hdr.Get("X-Trickster-Result"))
	})
}

// --- fake Prometheus server -------------------------------------------------

type fakeProm struct {
	srv      *httptest.Server
	label    string // unique instance label so TSM merge produces N distinct series
	behavior atomic.Pointer[promBehavior]
}

type promBehavior struct {
	mode      string // "ok", "empty", "oversized", "status", "badshape", "truncate"
	status    int
	seriesKB  int // for "oversized": approximate body size per response
	delay     time.Duration
}

func behaviorOK() *promBehavior        { return &promBehavior{mode: "ok"} }
func behaviorEmpty() *promBehavior     { return &promBehavior{mode: "empty"} }
func behaviorOversized(kb int) *promBehavior {
	return &promBehavior{mode: "oversized", seriesKB: kb}
}
func behaviorStatus(code int) *promBehavior {
	return &promBehavior{mode: "status", status: code}
}
func behaviorBadShape() *promBehavior { return &promBehavior{mode: "badshape"} }
func behaviorTruncate() *promBehavior { return &promBehavior{mode: "truncate"} }

func newFakeProm(t *testing.T, label string) *fakeProm {
	t.Helper()
	f := &fakeProm{label: label}
	f.behavior.Store(behaviorOK())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query_range", f.handleRange)
	mux.HandleFunc("/api/v1/query", f.handleInstant)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProm) URL() string { return f.srv.URL }

func (f *fakeProm) setBehavior(b *promBehavior) { f.behavior.Store(b) }

func (f *fakeProm) handleRange(w http.ResponseWriter, _ *http.Request) {
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
		// Promise a full Content-Length, then close mid-stream.
		full := buildMatrixBody(f.label)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(full)))
		// Write only ~30% then drop the connection by hijacking and closing.
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
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buildMatrixBody(f.label))
}

func (f *fakeProm) handleInstant(w http.ResponseWriter, _ *http.Request) {
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
		// Return matrix on instant endpoint to exercise heterogenous merge.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buildMatrixBody(f.label))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(buildVectorBody(f.label))
}

// buildVectorBody returns a Prometheus instant-query response with a single
// series labeled by the given instance.
func buildVectorBody(instance string) []byte {
	body := fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"__name__":"up","job":"fake","instance":%q},`+
			`"value":[%d,"1"]}]}}`,
		instance, time.Now().Unix())
	return []byte(body)
}

// buildMatrixBody returns a Prometheus range-query response with a single
// time-series labeled by the given instance and ~5 datapoints.
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

// buildOversizedMatrix returns a range-query response padded out to roughly
// targetKB kilobytes by emitting many distinct series under the given instance.
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

// --- raw HTTP helpers (queryTricksterProm requires 200; some tests don't) ----

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

// --- synthesized YAML config ------------------------------------------------

func writeScaleConfig(t *testing.T, fakes []*fakeProm,
	listenPort, metricsPort, mgmtPort int, albName string) string {
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
	fmt.Fprintf(&sb, "  %s:\n", albName)
	sb.WriteString("    provider: alb\n")
	sb.WriteString("    alb:\n")
	sb.WriteString("      mechanism: tsm\n")
	sb.WriteString("      pool:\n")
	for i := range fakes {
		fmt.Fprintf(&sb, "        - prom%d\n", i)
	}
	path := filepath.Join(t.TempDir(), "alb-tsm-scale.yaml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o644))
	return path
}
