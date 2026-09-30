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

package engines

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func BenchmarkDeltaProxyCache(b *testing.B) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		b.Error(err)
	}
	defer closeTestHarness(ts, r)

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	for b.Loop() {
		client.QueryRangeHandler(w, r)
	}
}

func BenchmarkDeltaProxyCacheChunks(b *testing.B) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		b.Error(err)
	}
	rsc.CacheConfig.UseCacheChunking = true
	defer closeTestHarness(ts, r)

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	for b.Loop() {
		client.QueryRangeHandler(w, r)
	}
}

// a Prometheus matrix of series x points, one every step and ending at end
func benchMatrixBody(series, points int, end time.Time, step time.Duration) string {
	var b strings.Builder
	b.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	start := end.Add(-step * time.Duration(points-1))
	for i := range series {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"metric":{"__name__":"up","job":"node","instance":"host-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`:9100"},"values":[`)
		for j := range points {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('[')
			b.WriteString(strconv.FormatInt(start.Add(step*time.Duration(j)).Unix(), 10))
			b.WriteString(`,"`)
			b.WriteString(strconv.Itoa((i * j) % 977))
			b.WriteString(`"]`)
		}
		b.WriteString("]}")
	}
	b.WriteString("]}}")
	return b.String()
}

func BenchmarkDeltaProxyCacheMatrixHit(b *testing.B) {
	// a memory-cache hit on a wide matrix, which is what an origin-sized dashboard panel is
	const series, points = 100, 1000
	step := 60 * time.Second
	end := time.Now().Add(-24 * time.Hour).Truncate(step)
	start := end.Add(-step * time.Duration(points-1))
	logger.SetLogger(logging.NoopLogger())
	client := &TestClient{}
	ts, _, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200,
		benchMatrixBody(series, points, end, step), nil, tu.PrometheusBackendProvider,
		"/prometheus/api/v1/query_range", "error")
	if err != nil {
		b.Fatal(err)
	}
	defer closeTestHarness(ts, r)
	rsc := request.GetResources(r)
	rsc.Tracer = nil
	o := rsc.BackendOptions
	o.FastForwardDisable = true
	o.TimeseriesRetention = 100 * points
	o.MaxObjectSizeBytes = 64 << 20
	tb, _ := backends.NewTimeseriesBackend(o.Name, o, client.RegisterHandlers, nil, rsc.CacheClient, nil)
	client.TimeseriesBackend = tb
	rsc.BackendClient = client
	o.HTTPClient = client.HTTPClient()
	r.URL.Path = "/prometheus/api/v1/query_range"
	r.URL.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=up",
		int(step.Seconds()), start.Unix(), end.Unix())
	w := httptest.NewRecorder()
	client.QueryRangeHandler(w, r)
	if !strings.Contains(w.Header().Get("X-Trickster-Result"), "kmiss") {
		b.Fatalf("the priming request was not a miss: %s", w.Header().Get("X-Trickster-Result"))
	}
	b.ReportAllocs()
	for b.Loop() {
		w = httptest.NewRecorder()
		client.QueryRangeHandler(w, r)
	}
	if !strings.Contains(w.Header().Get("X-Trickster-Result"), "status=hit") {
		b.Fatalf("the benchmarked request was not a hit: %s", w.Header().Get("X-Trickster-Result"))
	}
}
