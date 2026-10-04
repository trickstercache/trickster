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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	globalCacheALB    = "global-alb"
	globalCacheMember = "m0"
	globalCacheTenant = "X-Scope-OrgID"
)

// recordingSource counts the reads a member serves, and the time range of each
type recordingSource struct {
	memberSource
	mu     sync.Mutex
	ranges [][2]int64
}

func (s *recordingSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.mu.Lock()
	s.ranges = append(s.ranges, [2]int64{mint, maxt})
	s.mu.Unlock()
	return s.memberSource.Select(ctx, mint, maxt, hints, matchers...)
}

func (s *recordingSource) reads() [][2]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][2]int64(nil), s.ranges...)
}

type globalCacheTest struct {
	evaluator backends.TSMGlobalEvaluator
	source    *recordingSource
	req       backends.TSMGlobalRequest
	mu        sync.Mutex
	// baseMillis is the minute, three hours ago, that the member's samples start at; caches keep only recent data
	baseMillis int64
}

// recentStore holds two pods' series sampled each minute for an hour from baseMillis
func recentStore(baseMillis int64) presenceStore {
	var store presenceStore
	for _, pod := range []string{presencePodA, presencePodB} {
		samples := make([]chunks.Sample, 60)
		for i := range samples {
			samples[i] = presenceSample{t: baseMillis + int64(i)*presenceMinute, f: 1}
		}
		store = append(store, storage.NewListSeries(labels.FromStrings(labels.MetricName, presenceMetric,
			presencePodLabel, pod), samples))
	}
	return store
}

func (gt *globalCacheTest) seconds(minute int) string {
	return strconv.FormatInt(gt.baseMillis/1000+int64(minute)*60, 10)
}

func (gt *globalCacheTest) rangeValues(start, end int) url.Values {
	return url.Values{
		upQuery: {globalTestQuery}, upStart: {gt.seconds(start)}, upEnd: {gt.seconds(end)},
		upStep: {"60"},
	}
}

// newGlobalCacheTest builds an evaluator keeping results in a memory cache, over one member whose
// reads are recorded; unavailable adds a member the pool holds out of service
func newGlobalCacheTest(t *testing.T) *globalCacheTest {
	t.Helper()
	conf, err := config.Load([]string{
		"-origin-url", "http://127.0.0.1:1", "-provider", providers.Prometheus,
		"-log-level", "error",
	})
	require.NoError(t, err)
	caches := cr.LoadCachesFromConfig(conf)
	c := caches["default"]
	t.Cleanup(func() { _ = c.Close() })
	alb := conf.Backends["default"].Clone()
	alb.Name = globalCacheALB
	alb.ALBOptions = ao.New()
	alb.ALBOptions.Pool = ao.Members(globalCacheMember)
	base := time.Now().Truncate(time.Minute).Add(-3 * time.Hour).UnixMilli()
	gt := &globalCacheTest{baseMillis: base, source: &recordingSource{store: recentStore(base)}}
	gt.req = backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: globalCacheMember, Source: gt.source}}}
	gt.evaluator, err = (&Client{}).NewTSMGlobalEvaluator(globalTestSettings(), backends.TSMGlobalCache{
		Cache: c, Backend: alb, Members: func() backends.TSMGlobalRequest {
			gt.mu.Lock()
			defer gt.mu.Unlock()
			return gt.req
		},
	})
	require.NoError(t, err)
	return gt
}

func (gt *globalCacheTest) setUnavailable(down bool) {
	gt.mu.Lock()
	defer gt.mu.Unlock()
	gt.req.Unavailable = nil
	if down {
		gt.req.Unavailable = []backends.TSMGlobalUnavailable{{Name: "down"}}
	}
}

func (gt *globalCacheTest) serve(t *testing.T, path string, values url.Values, tenant string) *httptest.ResponseRecorder {
	t.Helper()
	r := globalTestRequest(path, values)
	if tenant != "" {
		r.Header.Set(globalCacheTenant, tenant)
	}
	w := httptest.NewRecorder()
	gt.evaluator.ServeTSMGlobal(w, r, backends.TSMGlobalRequest{})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w
}

// resultValues decodes a response's result, ignoring its formatting
func resultValues(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Data struct {
			Result json.RawMessage `json:"result"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	var result any
	require.NoError(t, json.Unmarshal(resp.Data.Result, &result))
	out, err := json.Marshal(result)
	require.NoError(t, err)
	return string(out)
}

func requireCacheStatus(t *testing.T, w *httptest.ResponseRecorder, want status.Label) {
	t.Helper()
	require.Contains(t, w.Header().Get(headers.NameTricksterResult), "status="+string(want))
}

func TestGlobalEvaluationCache(t *testing.T) {
	gt := newGlobalCacheTest(t)
	first := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), "")
	requireCacheStatus(t, first, status.StatusKeyMiss)
	require.Len(t, gt.source.reads(), 1)
	uncached := serveGlobal(newGlobalEvaluator(globalTestSettings()),
		globalTestRequest(globalTestRangePath, gt.rangeValues(0, 20)), backends.TSMGlobalRequest{
			Members: []backends.TSMGlobalMember{{Name: globalCacheMember, Source: gt.source.memberSource}},
		})
	require.Equal(t, resultValues(t, uncached.Body.Bytes()), resultValues(t, first.Body.Bytes()))

	again := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), "")
	requireCacheStatus(t, again, status.StatusHit)
	require.Len(t, gt.source.reads(), 1, "a hit evaluates nothing")
	require.Equal(t, resultValues(t, first.Body.Bytes()), resultValues(t, again.Body.Bytes()))

	extended := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 25), "")
	requireCacheStatus(t, extended, status.StatusPartialHit)
	reads := gt.source.reads()
	require.Len(t, reads, 2)
	require.Greater(t, reads[1][0], reads[0][0], "only what the cache lacks is evaluated")

	tenant := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), "tenant-b")
	requireCacheStatus(t, tenant, status.StatusKeyMiss)
	require.Len(t, gt.source.reads(), 3, "another tenant's results are kept apart")
}

func TestGlobalEvaluationCacheSkipsPartialResults(t *testing.T) {
	gt := newGlobalCacheTest(t)
	gt.setUnavailable(true)
	for i := range 2 {
		w := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), "")
		requireCacheStatus(t, w, status.StatusProxyOnly)
		require.Contains(t, w.Body.String(), "member unavailable", "attempt %d", i)
	}
	reads := len(gt.source.reads())
	gt.setUnavailable(false)
	w := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), "")
	requireCacheStatus(t, w, status.StatusKeyMiss)
	require.NotContains(t, w.Body.String(), "member unavailable")
	require.Greater(t, len(gt.source.reads()), reads)
	requireCacheStatus(t, gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), ""), status.StatusHit)
}

func TestGlobalEvaluationCacheInstant(t *testing.T) {
	gt := newGlobalCacheTest(t)
	instant := url.Values{upQuery: {globalTestQuery}, upTime: {gt.seconds(10)}}
	w := gt.serve(t, globalTestQueryPath, instant, "")
	require.Equal(t, `[{"metric":{},"value":[`+gt.seconds(10)+`,"2"]}]`, resultValues(t, w.Body.Bytes()))
	reads := len(gt.source.reads())
	gt.serve(t, globalTestQueryPath, instant, "")
	require.Len(t, gt.source.reads(), reads, "the repeated instant query is answered from the cache")

	gt.setUnavailable(true)
	partial := url.Values{upQuery: {globalTestQuery}, upTime: {gt.seconds(12)}}
	gt.serve(t, globalTestQueryPath, partial, "")
	reads = len(gt.source.reads())
	gt.serve(t, globalTestQueryPath, partial, "")
	require.Greater(t, len(gt.source.reads()), reads, "a partial instant result is not cached")
}

func TestCachedGlobalUnknownPath(t *testing.T) {
	gt := newGlobalCacheTest(t)
	w := httptest.NewRecorder()
	gt.evaluator.ServeTSMGlobal(w, httptest.NewRequest(http.MethodGet, APIPath+mnSeries, nil),
		backends.TSMGlobalRequest{})
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestGlobalEvaluationCacheKeepsCompleteResults(t *testing.T) {
	gt := newGlobalCacheTest(t)
	requireCacheStatus(t, gt.serve(t, globalTestRangePath, gt.rangeValues(0, 20), ""), status.StatusKeyMiss)
	gt.setUnavailable(true)
	partial := gt.serve(t, globalTestRangePath, gt.rangeValues(0, 25), "")
	requireCacheStatus(t, partial, status.StatusProxyOnly)
	require.Contains(t, partial.Body.String(), "member unavailable")
	gt.setUnavailable(false)
	// the complete results cached before the member left are still there to extend
	requireCacheStatus(t, gt.serve(t, globalTestRangePath, gt.rangeValues(0, 25), ""), status.StatusPartialHit)
}

func TestInProcessTransport(t *testing.T) {
	silent := inProcessTransport{handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	resp, err := silent.RoundTrip(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Zero(t, resp.ContentLength)

	writer := inProcessTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
		w.WriteHeader(http.StatusTeapot)
	})}
	resp, err = writer.RoundTrip(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the status is fixed by the first write")
	require.Equal(t, int64(2), resp.ContentLength)
}

func TestGlobalCacheKeyPrefix(t *testing.T) {
	e := newGlobalEvaluator(globalTestSettings())
	bare := e.cacheKeyPrefix(nil)
	alb := newGlobalCacheTest(t)
	require.NotEqual(t, bare, alb.evaluator.(*globalEvaluator).cached.client.Configuration().CacheKeyPrefix)
	chain := globalTestSettings()
	chain.Deduplication = "penalty"
	require.NotEqual(t, bare, newGlobalEvaluator(chain).cacheKeyPrefix(nil), "settings change the key")
}
