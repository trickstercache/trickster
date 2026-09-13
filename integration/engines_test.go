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
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type engineFixture struct {
	tricksterAddr string
	metricsAddr   string
	client        *http.Client
}

func engineSetup(t *testing.T, path string, handler http.HandlerFunc) *engineFixture {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status/buildinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"version":"test"}}`))
	})
	mux.HandleFunc(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	h := staticConfigHarness(t, "testdata/configs/engines.yaml")
	rewriteGeneratedConfig(t, h.ConfigPath, "http://127.0.0.1:18520", srv.URL)
	h.start(t)

	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &engineFixture{
		tricksterAddr: h.BaseAddr,
		metricsAddr:   h.MetricsAddr,
		client:        &http.Client{Transport: transport, Timeout: 15 * time.Second},
	}
}

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

func doEngineRequest(client *http.Client, requestURL string) (int, []byte, http.Header, error) {
	resp, err := client.Get(requestURL)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header.Clone(), err
}

type engineResult struct {
	status int
	body   []byte
	header http.Header
	err    error
}

// doEngineRangeBurst holds the origin until every client has written its request to Trickster.
func doEngineRangeBurst(t *testing.T, f *engineFixture, params url.Values, n int, release func()) []engineResult {
	t.Helper()
	start := make(chan struct{})
	written := make(chan struct{}, n)
	results := make(chan engineResult, n)
	u := "http://" + f.tricksterAddr + "/prom-fake/api/v1/query_range?" + params.Encode()

	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, u, nil)
			if err != nil {
				results <- engineResult{err: err}
				return
			}
			var signaled sync.Once
			trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
				signaled.Do(func() { written <- struct{}{} })
			}}
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
			<-start
			resp, err := f.client.Do(req)
			if err != nil {
				results <- engineResult{err: err}
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			results <- engineResult{status: resp.StatusCode, body: body,
				header: resp.Header.Clone(), err: err}
		}()
	}
	close(start)

	allWritten := true
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for range n {
		select {
		case <-written:
		case <-timer.C:
			allWritten = false
		}
		if !allWritten {
			break
		}
	}
	release()
	wg.Wait()
	close(results)
	require.True(t, allWritten, "not all requests were written to Trickster before timeout")
	collected := make([]engineResult, 0, n)
	for result := range results {
		collected = append(collected, result)
	}
	return collected
}

func TestEngines_PCF_Collapse(t *testing.T) {
	params, start, end, step := engRangeParams(fmt.Sprintf("%d_944", time.Now().UnixNano()))
	var counter atomic.Int32
	release := make(chan struct{})
	f := engineSetup(t, "/api/v1/query_range", func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, engValidRangeBody(start, end, step))
	})

	const n = 20
	results := doEngineRangeBurst(t, f, params, n, func() { close(release) })
	for _, r := range results {
		require.NoError(t, r.err)
		require.Equal(t, http.StatusOK, r.status)
		require.Contains(t, string(r.body), `"status":"success"`)
		require.Contains(t, string(r.body), `"resultType":"matrix"`)
	}
	require.Equal(t, int32(1), counter.Load(),
		"all %d concurrent requests must collapse onto a single origin fetch", n)
}

func TestEngines_Singleflight_ErrorPropagation(t *testing.T) {
	params, _, _, _ := engRangeParams(fmt.Sprintf("%d_939", time.Now().UnixNano()))
	var counter atomic.Int32
	const errBody = `{"status":"error","errorType":"internal","error":"origin failure"}`
	release := make(chan struct{})
	f := engineSetup(t, "/api/v1/query_range", func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, errBody)
	})

	const n = 10
	results := doEngineRangeBurst(t, f, params, n, func() { close(release) })

	for _, r := range results {
		require.NoError(t, r.err)
		require.Equal(t, http.StatusServiceUnavailable, r.status)
		require.NotEmpty(t, string(r.body),
			"collapsed waiter must see the upstream error body, not empty")
		require.Contains(t, string(r.body), "origin failure",
			"collapsed waiter must see the origin's error detail")
		require.Equal(t, errBody, string(r.body),
			"collapsed waiter body must match the origin response byte-for-byte")
	}
	require.Equal(t, int32(1), counter.Load(),
		"origin must be contacted exactly once for error responses")
}

func TestEngines_Collapse_MetricsReport(t *testing.T) {
	params, start, end, step := engRangeParams(fmt.Sprintf("%d_933", time.Now().UnixNano()))
	var counter atomic.Int32
	release := make(chan struct{})
	f := engineSetup(t, "/api/v1/query_range", func(w http.ResponseWriter, _ *http.Request) {
		counter.Add(1)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, engValidRangeBody(start, end, step))
	})

	before := readProxyHitCount(t, f.metricsAddr)

	const n = 20
	results := doEngineRangeBurst(t, f, params, n, func() { close(release) })
	var misses, proxyHits int
	for _, r := range results {
		require.NoError(t, r.err)
		assert.Equal(t, http.StatusOK, r.status)
		s := parseTricksterResult(r.header.Get(headers.NameTricksterResult))["status"]
		switch s {
		case cachestatus.LookupStatusKeyMiss.String():
			misses++
		case cachestatus.LookupStatusProxyHit.String():
			proxyHits++
		}
	}
	require.Equal(t, int32(1), counter.Load(), "collapse must hit origin exactly once")
	require.Equal(t, 1, misses, "exactly one request must execute the cache miss")
	require.Equal(t, n-1, proxyHits, "all other requests must join the in-flight request")

	// Metrics are incremented after the response is flushed. Window is
	// generous because CI runners under -race + cgroup CPU limits routinely
	// take an order of magnitude longer than local runs to flush 20 shared
	// singleflight responses and scrape /metrics.
	var after float64
	require.Eventually(t, func() bool {
		after = readProxyHitCount(t, f.metricsAddr)
		return after-before >= float64(n-1)
	}, 15*time.Second, 25*time.Millisecond,
		"proxy-hit metric did not reach expected delta (before=%v)", before)

	require.InDelta(t, float64(n-1), after-before, 0.0001,
		"expected exactly %d proxy-hit increments, got %v", n-1, after-before)
}

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

func doEngineInstant(t *testing.T, f *engineFixture, params url.Values) (int, []byte, http.Header) {
	t.Helper()
	u := "http://" + f.tricksterAddr + "/prom-fake/api/v1/query?" + params.Encode()
	sc, b, h, err := doEngineRequest(f.client, u)
	require.NoError(t, err)
	return sc, b, h
}

func TestEngines_LargeResponse(t *testing.T) {
	const nResults = 500
	body := engValidVectorBody(nResults)
	require.Greater(t, len(body), 32*1024)

	f := engineSetup(t, "/api/v1/query", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})

	params := url.Values{"query": {fmt.Sprintf("fake + 0*%d", time.Now().UnixNano())}}
	sc, got, _ := doEngineInstant(t, f, params)
	require.Equal(t, http.StatusOK, sc)
	require.Greater(t, len(got), 32*1024)

	var pr promResponse
	require.NoError(t, json.Unmarshal(got, &pr))
	require.Equal(t, "success", pr.Status)

	var qd promQueryData
	require.NoError(t, json.Unmarshal(pr.Data, &qd))
	require.Equal(t, "vector", qd.ResultType)

	var results []json.RawMessage
	require.NoError(t, json.Unmarshal(qd.Result, &results))
	require.Len(t, results, nResults)
}

func readProxyHitCount(t *testing.T, metricsAddr string) float64 {
	t.Helper()
	resp, err := http.Get("http://" + metricsAddr + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var total float64
	for line := range strings.SplitSeq(string(b), "\n") {
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
