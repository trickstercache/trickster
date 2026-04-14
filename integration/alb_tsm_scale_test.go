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

// TestALB_TSM_Scale exercises ALB+TSM against many in-process fake Prometheus
// backends with configurable fault behaviors, covering the high-fanout shape
// that the 2-backend TestALB harness does not.
func TestALB_TSM_Scale(t *testing.T) {
	const (
		listenPort         = 8590
		metricsPort        = 8591
		mgmtPort           = 8592
		listenAddr         = "127.0.0.1:8590"
		backendName        = "alb-tsm-scale"
		labeledBackendName = "alb-tsm-scale-labeled"
		numBackends        = 50
		numLabeledBackends = 10
	)

	fakes := make([]*fakeProm, numBackends)
	for i := range fakes {
		fakes[i] = newFakeProm(t, fmt.Sprintf("prom-%02d", i))
	}

	cfgPath := writeScaleConfig(t, fakes, listenPort, metricsPort, mgmtPort,
		backendName, labeledBackendName, numLabeledBackends)

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
		// A panic in TSM merge tears down subsequent sub-tests, so only
		// assert the daemon keeps serving; response shape is not contracted.
		resetAll()
		fakes[0].setBehavior(behaviorBadShape())
		body, hdr := rawQueryAllowError(t, listenAddr, backendName, "/api/v1/query", instantParams())
		t.Logf("%d bytes, %s", len(body), hdr.Get("X-Trickster-Result"))
	})

	t.Run("truncating_upstream_does_not_poison_cache", func(t *testing.T) {
		resetAll()
		params := rangeParams()
		fakes[0].setBehavior(behaviorTruncate())
		_, _ = rawQueryAllowError(t, listenAddr, backendName, "/api/v1/query_range", params)

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
			wg.Add(1)
			go func() {
				defer wg.Done()
				body, _, sc := doRaw(t, listenAddr, backendName, "/api/v1/query_range", params)
				if sc != http.StatusOK {
					errCh <- fmt.Errorf("status %d: %s", sc, body)
				}
			}()
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
		// Perfect collapse is numBackends; allow 3x for waiters arriving
		// after the executor completes.
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
func behaviorBadShape() *promBehavior              { return &promBehavior{mode: "badshape"} }
func behaviorTruncate() *promBehavior              { return &promBehavior{mode: "truncate"} }
func behaviorSlow(d time.Duration) *promBehavior   { return &promBehavior{mode: "ok", delay: d} }

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
		// Promise full Content-Length, write partial, hijack + close.
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

func buildVectorBody(instance string) []byte {
	return []byte(fmt.Sprintf(
		`{"status":"success","data":{"resultType":"vector","result":[`+
			`{"metric":{"__name__":"up","job":"fake","instance":%q},`+
			`"value":[%d,"1"]}]}}`,
		instance, time.Now().Unix()))
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
	albName, labeledAlbName string, labeledN int) string {
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
	for i := 0; i < labeledN; i++ {
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
	for i := 0; i < labeledN; i++ {
		fmt.Fprintf(&sb, "        - prom-lab-%d\n", i)
	}
	path := filepath.Join(t.TempDir(), "alb-tsm-scale.yaml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o644))
	return path
}
