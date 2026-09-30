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

package prometheus

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

// a response writer that keeps the headers and drops the body, whose buffering is no cost of the
// proxy's
type discardResponse struct {
	h http.Header
}

func newDiscardResponse() *discardResponse {
	return &discardResponse{h: make(http.Header)}
}

func (d *discardResponse) Header() http.Header         { return d.h }
func (d *discardResponse) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardResponse) WriteHeader(int)             {}

// a matrix of series x points, one every step and ending at end
func benchQueryRangeBody(series, points int, end time.Time, step time.Duration) string {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	start := end.Add(-step * time.Duration(points-1))
	for i := range series {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metric":{"__name__":"node_cpu_seconds_total","job":"node","instance":"host-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`:9100"},"values":[`)
		for j := range points {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('[')
			b.WriteString(strconv.FormatInt(start.Add(step*time.Duration(j)).Unix(), 10))
			b.WriteString(`,"`)
			b.WriteString(strconv.FormatFloat(float64(i*j%9973)/7, 'f', -1, 64))
			b.WriteString(`"]`)
		}
		b.WriteString("]}")
	}
	b.WriteString("]}}")
	return b.String()
}

func BenchmarkQueryRangeDeltaProxyCacheHit(b *testing.B) {
	// a memory-cache hit on a wide matrix, through the provider's own models
	const series, points = 100, 1000
	step := time.Minute
	end := time.Now().Add(-24 * time.Hour).Truncate(step)
	start := end.Add(-step * time.Duration(points-1))
	path := fmt.Sprintf("/api/v1/query_range?query=up&start=%d&end=%d&step=%d",
		start.Unix(), end.Unix(), int(step.Seconds()))
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		benchQueryRangeBody(series, points, end, step), nil, providers.Prometheus, path, "error")
	if err != nil {
		b.Fatal(err)
	}
	defer ts.Close()
	logger.SetLogger(logging.NoopLogger())
	rsc := request.GetResources(r)
	rsc.Tracer = nil
	o := rsc.BackendOptions
	o.TimeseriesRetention = 100 * points
	o.MaxObjectSizeBytes = 64 << 20
	o.FastForwardDisable = true
	backendClient, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	o.HTTPClient = backendClient.HTTPClient()
	w := newDiscardResponse()
	client.QueryRangeHandler(w, r)
	if res := w.Header().Get(headers.NameTricksterResult); !strings.Contains(res, "kmiss") {
		b.Fatalf("the priming request was not a miss: %s", res)
	}
	b.ReportAllocs()
	for b.Loop() {
		w = newDiscardResponse()
		client.QueryRangeHandler(w, r)
	}
	if res := w.Header().Get(headers.NameTricksterResult); !strings.Contains(res, "status=hit") {
		b.Fatalf("the benchmarked request was not a hit: %s", res)
	}
}

func BenchmarkQueryRangeDeltaProxyCachePeakHeap(b *testing.B) {
	// peak heap while hits and partial hits, which each store a new entry, run together on one huge
	// entry; new entries, and old ones readers still hold, are what could raise it
	for _, shape := range []struct {
		name           string
		series, points int
	}{
		{"1000x1000", 1000, 1000},
		{"1x1000000", 1, 1000000},
	} {
		b.Run(shape.name, func(b *testing.B) {
			benchPeakHeap(b, shape.series, shape.points)
		})
	}
}

func benchPeakHeap(b *testing.B, series, points int) {
	step := time.Second
	end := time.Now().Add(-24 * time.Hour).Truncate(time.Minute)
	start := end.Add(-step * time.Duration(points-1))
	query := func(end time.Time) string {
		return fmt.Sprintf("/api/v1/query_range?query=up&start=%d&end=%d&step=%d",
			start.Unix(), end.Unix(), int(step.Seconds()))
	}
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		benchQueryRangeBody(series, points, end, step), nil, providers.Prometheus, query(end), "error")
	if err != nil {
		b.Fatal(err)
	}
	defer ts.Close()
	logger.SetLogger(logging.NoopLogger())
	rsc := request.GetResources(r)
	defer rsc.CacheClient.Close()
	rsc.Tracer = nil
	o := rsc.BackendOptions
	o.TimeseriesRetention = timeconv.Duration(100 * points)
	o.MaxObjectSizeBytes = 1 << 30
	o.FastForwardDisable = true
	backendClient, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	o.HTTPClient = backendClient.HTTPClient()
	client.QueryRangeHandler(httptest.NewRecorder(), r)

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak atomic.Uint64
	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				metrics.Read(samples)
				if v := samples[0].Value.Uint64(); v > peak.Load() {
					peak.Store(v)
				}
			}
		}
	}()
	var n atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := n.Add(1)
			rq, _ := request.Clone(r)
			if i%8 == 0 {
				// each partial hit reaches past the entry by its own amount, so each stores anew
				u := *rq.URL
				rq.URL = &u
				p, _ := url.Parse(query(end.Add(step * time.Duration(i))))
				rq.URL.RawQuery = p.RawQuery
			}
			client.QueryRangeHandler(newDiscardResponse(), rq)
		}
	})
	b.StopTimer()
	close(stop)
	<-sampled
	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	if res := w.Header().Get(headers.NameTricksterResult); !strings.Contains(res, "status=hit") {
		b.Fatalf("the entry is not being served from the cache: %s", res)
	}
	b.ReportMetric(float64(peak.Load())/(1<<20), "peak-MB")
	b.ReportMetric(float64(before.HeapAlloc)/(1<<20), "base-MB")
}

func BenchmarkQueryRangeFastForwardHit(b *testing.B) {
	// a refresh that reaches now: the interior is a memory-cache hit, and the live point is an
	// object-cache hit that is decoded and merged in on every request
	const series, points = 100, 1000
	step := time.Minute
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if strings.HasSuffix(r.URL.Path, "/query_range") {
			end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
			w.Write([]byte(benchQueryRangeBody(series, points, time.Unix(end, 0), step)))
			return
		}
		at := q.Get("time")
		var sb strings.Builder
		sb.WriteString(`{"status":"success","data":{"resultType":"vector","result":[`)
		for i := range series {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(`{"metric":{"__name__":"node_cpu_seconds_total","job":"node","instance":"host-`)
			sb.WriteString(strconv.Itoa(i))
			sb.WriteString(`:9100"},"value":[` + at + `,"` + strconv.Itoa(i) + `"]}`)
		}
		sb.WriteString("]}}")
		w.Write([]byte(sb.String()))
	}))
	defer origin.Close()
	now := time.Now()
	start := now.Add(-step * time.Duration(points-1))
	path := fmt.Sprintf("/api/v1/query_range?query=up&start=%d&end=%d&step=%d",
		start.Unix(), now.Unix(), int(step.Seconds()))
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200, "{}", nil,
		providers.Prometheus, path, "error")
	if err != nil {
		b.Fatal(err)
	}
	defer ts.Close()
	logger.SetLogger(logging.NoopLogger())
	rsc := request.GetResources(r)
	defer rsc.CacheClient.Close()
	rsc.Tracer = nil
	o := rsc.BackendOptions
	o.Host = origin.Listener.Addr().String()
	o.MaxObjectSizeBytes = 64 << 20
	backendClient, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	o.HTTPClient = backendClient.HTTPClient()
	for range 2 {
		client.QueryRangeHandler(httptest.NewRecorder(), r)
	}
	w := newDiscardResponse()
	b.ReportAllocs()
	for b.Loop() {
		w = newDiscardResponse()
		client.QueryRangeHandler(w, r)
	}
	if res := w.Header().Get(headers.NameTricksterResult); !strings.Contains(res, "ffstatus=hit") {
		b.Fatalf("the live point was not an object cache hit: %s", res)
	}
}

func BenchmarkQueryRangeDeltaProxyCachePartialHit(b *testing.B) {
	// a memory-cache partial hit on a wide matrix: each request reaches one step past the last, so each
	// merges the fetched bucket into the entry and stores it
	const series, points = 100, 1000
	step := time.Minute
	end := time.Now().Add(-24 * time.Hour).Truncate(step)
	start := end.Add(-step * time.Duration(points-1))
	query := func(end time.Time) string {
		return fmt.Sprintf("/api/v1/query_range?query=up&start=%d&end=%d&step=%d",
			start.Unix(), end.Unix(), int(step.Seconds()))
	}
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	ts, _, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		benchQueryRangeBody(series, points, end, step), nil, providers.Prometheus, query(end), "error")
	if err != nil {
		b.Fatal(err)
	}
	defer ts.Close()
	logger.SetLogger(logging.NoopLogger())
	rsc := request.GetResources(r)
	rsc.Tracer = nil
	o := rsc.BackendOptions
	o.TimeseriesRetention = timeconv.Duration(100 * points)
	o.MaxObjectSizeBytes = 1 << 30
	o.FastForwardDisable = true
	backendClient, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	o.HTTPClient = backendClient.HTTPClient()
	client.QueryRangeHandler(newDiscardResponse(), r)
	b.ReportAllocs()
	i := 0
	var w *discardResponse
	for b.Loop() {
		i++
		rq, _ := request.Clone(r)
		u := *rq.URL
		p, _ := url.Parse(query(end.Add(step * time.Duration(i))))
		u.RawQuery = p.RawQuery
		rq.URL = &u
		w = newDiscardResponse()
		client.QueryRangeHandler(w, rq)
	}
	if res := w.Header().Get(headers.NameTricksterResult); !strings.Contains(res, "status=phit") {
		b.Fatalf("the benchmarked request was not a partial hit: %s", res)
	}
}
