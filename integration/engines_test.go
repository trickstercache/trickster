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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	engTricksterAddr = "127.0.0.1:8520"
	engMetricsAddr   = "127.0.0.1:8521"
	engOriginAddr    = "127.0.0.1:18520"
)

// engineFakeOrigin wraps an httptest.Server bound to a fixed address so the
// TestEngines_* configs can point at a known port.
type engineFakeOrigin struct {
	srv     *httptest.Server
	handler func(http.ResponseWriter, *http.Request)
	mu      sync.Mutex
}

func (o *engineFakeOrigin) setHandler(h func(http.ResponseWriter, *http.Request)) {
	o.mu.Lock()
	o.handler = h
	o.mu.Unlock()
}

// Package-level state shared by every TestEngines_* test so that a single
// trickster process and a single fake origin are reused. This avoids port
// conflicts when multiple top-level tests run in sequence inside one binary.
var (
	engSetupOnce sync.Once
	engOrigin    *engineFakeOrigin
)

// engineSetup lazily boots the shared trickster+origin pair. The context
// behind daemon.Start is not cancelled for the life of the test binary; Go
// tears the process down at exit.
func engineSetup(t *testing.T) *engineFakeOrigin {
	t.Helper()
	engSetupOnce.Do(func() {
		o := &engineFakeOrigin{}
		ln, err := net.Listen("tcp", engOriginAddr)
		if err != nil {
			t.Fatalf("bind fake origin: %v", err)
		}
		srv := &httptest.Server{
			Listener: ln,
			Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				o.mu.Lock()
				h := o.handler
				o.mu.Unlock()
				if h == nil {
					http.Error(w, "no handler", http.StatusServiceUnavailable)
					return
				}
				h(w, r)
			})},
		}
		srv.Start()
		o.srv = srv
		engOrigin = o

		// Start trickster with a background context that lives for the whole
		// test binary. No t.Cleanup: we want this to survive across
		// TestEngines_* functions.
		ctx := context.Background()
		go startTrickster(t, ctx, expectedStartError{},
			"-config", "testdata/configs/engines.yaml")
		waitForTrickster(t, engMetricsAddr)
	})
	return engOrigin
}

// validRangeBody returns a minimally-valid Prometheus matrix response whose
// samples span [start,end] on the given step.
func engValidRangeBody(start, end, step int64) string {
	var vals strings.Builder
	vals.WriteByte('[')
	first := true
	for ts := start; ts <= end; ts += step {
		if !first {
			vals.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&vals, `[%d,"1"]`, ts)
	}
	vals.WriteByte(']')
	return `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","job":"fake"},"values":` + vals.String() + `}]}}`
}

// engRangeParams builds a set of range query params pinned to a fresh "now"
// per call. Each test passes a unique query suffix so the cache key is unique
// across subtests in the same binary run.
func engRangeParams(querySuffix string) (url.Values, int64, int64, int64) {
	const step int64 = 15
	end := time.Now().Unix()
	end = end - (end % step)
	start := end - 5*60
	q := "up"
	if querySuffix != "" {
		q = "up + 0*" + querySuffix
	}
	return url.Values{
		"query": {q},
		"start": {strconv.FormatInt(start, 10)},
		"end":   {strconv.FormatInt(end, 10)},
		"step":  {strconv.FormatInt(step, 10)},
	}, start, end, step
}

func doEngineRange(t *testing.T, params url.Values) (int, []byte, http.Header) {
	t.Helper()
	u := "http://" + engTricksterAddr + "/prom-fake/api/v1/query_range?" + params.Encode()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, b, resp.Header.Clone()
}

// TestEngines_PCF_Collapse validates that many concurrent identical range
// queries collapse onto a single origin fetch.
//
// regression: #944
//
// (Issue #944 was filed against the progressive-collapse-forwarder deadlock.
// DeltaProxyCache now uses a singleflight group to provide the same
// end-to-end collapse guarantee for range queries; this test asserts that
// behavior through the public HTTP surface.)
func TestEngines_PCF_Collapse(t *testing.T) {
	origin := engineSetup(t)

	params, start, end, step := engRangeParams(fmt.Sprintf("%d_944", time.Now().UnixNano()))
	var counter atomic.Int32
	origin.setHandler(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		// Hold the origin response open so concurrent requests pile up on
		// the singleflight executor before it finishes.
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, engValidRangeBody(start, end, step))
	})

	const n = 20
	type result struct {
		status int
		body   []byte
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			sc, b, _ := doEngineRange(t, params)
			results <- result{sc, b}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		require.Equal(t, http.StatusOK, r.status)
		require.Contains(t, string(r.body), `"status":"success"`)
		require.Contains(t, string(r.body), `"resultType":"matrix"`)
	}
	require.Equal(t, int32(1), counter.Load(),
		"all %d concurrent requests must collapse onto a single origin fetch", n)
}

// TestEngines_Singleflight_ErrorPropagation validates that when a singleflight
// executor receives a non-2xx response, every waiter sees the same status
// code and body.
//
// regression: #939
func TestEngines_Singleflight_ErrorPropagation(t *testing.T) {
	origin := engineSetup(t)

	params, _, _, _ := engRangeParams(fmt.Sprintf("%d_939", time.Now().UnixNano()))
	var counter atomic.Int32
	const errBody = `{"status":"error","errorType":"internal","error":"origin failure"}`
	origin.setHandler(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, errBody)
	})

	const n = 10
	type result struct {
		status int
		body   string
	}
	results := make(chan result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			sc, b, _ := doEngineRange(t, params)
			results <- result{sc, string(b)}
		}()
	}
	wg.Wait()
	close(results)

	// All collapsed waiters must agree on the error status code AND on the
	// exact body the origin served. Before the DPC error-body fix, the
	// upstream body was dropped in fetchTimeseries (HTTPDocument.Body left
	// nil) and every waiter got an empty string. Post-fix they must all
	// see the origin's structured error JSON verbatim.
	for r := range results {
		require.Equal(t, http.StatusServiceUnavailable, r.status)
		require.NotEmpty(t, r.body,
			"collapsed waiter must see the upstream error body, not empty")
		require.Contains(t, r.body, "origin failure",
			"collapsed waiter must see the origin's error detail")
		require.Equal(t, errBody, r.body,
			"collapsed waiter body must match the origin response byte-for-byte")
	}
	require.Equal(t, int32(1), counter.Load(),
		"origin must be contacted exactly once for error responses")
}

// TestEngines_Collapse_MetricsReport validates that the proxy-hit counter
// advances by exactly N-1 when N concurrent requests collapse onto a single
// origin fetch.
//
// regression: #933
func TestEngines_Collapse_MetricsReport(t *testing.T) {
	origin := engineSetup(t)

	params, start, end, step := engRangeParams(fmt.Sprintf("%d_933", time.Now().UnixNano()))
	var counter atomic.Int32
	origin.setHandler(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, engValidRangeBody(start, end, step))
	})

	before := readProxyHitCount(t)

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			sc, _, _ := doEngineRange(t, params)
			assert.Equal(t, http.StatusOK, sc)
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), counter.Load(), "collapse must hit origin exactly once")

	// Metrics are incremented after the response is flushed; allow a brief
	// window for the deferred metric updates to land.
	var after float64
	require.Eventually(t, func() bool {
		after = readProxyHitCount(t)
		return after-before >= float64(n-1)
	}, 3*time.Second, 50*time.Millisecond,
		"proxy-hit metric did not reach expected delta (before=%v)", before)

	require.InDelta(t, float64(n-1), after-before, 0.0001,
		"expected exactly %d proxy-hit increments, got %v", n-1, after-before)
}

// engValidVectorBody returns a minimally-valid Prometheus instant vector
// response with n result entries. Each entry is ~80 bytes, so n=500
// produces ~40KB — well over io.Copy's 32KB internal buffer.
func engValidVectorBody(n int) string {
	var buf strings.Builder
	buf.WriteString(`{"status":"success","data":{"resultType":"vector","result":[`)
	for i := range n {
		if i > 0 {
			buf.WriteByte(',')
		}
		fmt.Fprintf(&buf, `{"metric":{"__name__":"fake","instance":"inst-%04d"},"value":[1700000000,"1"]}`, i)
	}
	buf.WriteString(`]}}`)
	return buf.String()
}

func doEngineInstant(t *testing.T, params url.Values) (int, []byte, http.Header) {
	t.Helper()
	u := "http://" + engTricksterAddr + "/prom-fake/api/v1/query?" + params.Encode()
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Get(u)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, b, resp.Header.Clone()
}

// TestEngines_LargeResponse verifies that a Prometheus instant query
// response larger than 32KB is delivered intact through the proxy.
//
// CaptureResponseWriter.Write had a bug where it returned the cumulative
// byte count instead of the per-call count, violating the io.Writer
// contract. Go's io.Copy checks nr < nw after each Write; on the second
// 32KB chunk, the cumulative return value triggers errInvalidWrite and
// silently truncates the response. This test catches that regression.
func TestEngines_LargeResponse(t *testing.T) {
	origin := engineSetup(t)

	const nResults = 500 // ~40KB response
	body := engValidVectorBody(nResults)
	require.Greater(t, len(body), 32*1024, "test body must exceed 32KB to exercise the bug")

	origin.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})

	// Use a unique query to avoid cache collisions with other tests.
	params := url.Values{"query": {fmt.Sprintf("fake + 0*%d", time.Now().UnixNano())}}
	sc, got, _ := doEngineInstant(t, params)
	require.Equal(t, http.StatusOK, sc)
	require.Greater(t, len(got), 32*1024,
		"response must exceed 32KB — if truncated, CaptureResponseWriter.Write is returning cumulative len")

	var pr promResponse
	require.NoError(t, json.Unmarshal(got, &pr),
		"response must be valid JSON — truncation causes unexpected EOF")
	require.Equal(t, "success", pr.Status)

	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	require.Equal(t, "vector", qd.ResultType)

	var results []json.RawMessage
	require.NoError(t, json.Unmarshal(qd.Result, &results))
	require.Len(t, results, nResults, "all vector results must survive the proxy round-trip")
}

// readProxyHitCount returns the current sum of
// trickster_proxy_requests_total{cache_status="proxy-hit"} samples from the
// /metrics endpoint. A minimal line parser keeps this test self-contained.
func readProxyHitCount(t *testing.T) float64 {
	t.Helper()
	resp, err := http.Get("http://" + engMetricsAddr + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var total float64
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		if !strings.HasPrefix(line, "trickster_proxy_requests_total{") {
			continue
		}
		if !strings.Contains(line, `cache_status="proxy-hit"`) {
			continue
		}
		idx := strings.LastIndex(line, "}")
		if idx < 0 || idx+1 >= len(line) {
			continue
		}
		rest := strings.TrimSpace(line[idx+1:])
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		total += v
	}
	return total
}
