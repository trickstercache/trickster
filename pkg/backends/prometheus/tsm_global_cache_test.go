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
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/klauspost/compress/snappy"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	globalCacheALB      = "global-alb"
	globalCacheMember   = "m0"
	globalCacheTenant   = "X-Scope-OrgID"
	globalTeamHeader    = "X-Team"
	globalTeamA         = "team-a"
	globalTeamB         = "team-b"
	globalAliasPrefix   = "/prometheus"
	globalNamespace     = "pool-1"
	globalOtherNS       = "pool-2"
	globalSecondMember  = "m1"
	globalCountQuery    = "count(x)"
	globalTenantA       = "tenant-a"
	globalReplicaLabel  = "replica"
	globalCounterMetric = "c"
	globalNegativeCache = "errors"
	globalRawIdentity   = "member-identity"
)

// recordingSource counts the reads a member serves, and the time range of each
type recordingSource struct {
	memberSource
	mu     sync.Mutex
	ranges [][2]int64
	// fail, when set, answers a read with an error, and is cleared once it has
	fail error
}

func (s *recordingSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.mu.Lock()
	s.ranges = append(s.ranges, [2]int64{mint, maxt})
	err := s.fail
	s.fail = nil
	s.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	return s.memberSource.Select(ctx, mint, maxt, hints, matchers...)
}

func (s *recordingSource) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ranges)
}

func (s *recordingSource) failNext(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

type globalCacheTest struct {
	evaluator *globalEvaluator
	source    *recordingSource
	cache     cache.Cache
	mu        sync.Mutex
	req       backends.TSMGlobalRequest
	// baseMillis is the minute, three hours ago, that the member's samples start at
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

// globalTestCache returns a memory cache, closed when the test ends
func globalTestCache(t *testing.T) cache.Cache {
	t.Helper()
	conf, err := config.Load([]string{
		"-origin-url", "http://127.0.0.1:1", "-provider", providers.Prometheus,
		"-log-level", "error",
	})
	require.NoError(t, err)
	c := cr.LoadCachesFromConfig(conf)["default"]
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// globalTestALB returns the options of a global ALB over one member, with paths configured on it
func globalTestALB(t *testing.T, paths ...*pathopts.Options) *bo.Options {
	t.Helper()
	conf, err := config.Load([]string{
		"-origin-url", "http://127.0.0.1:1", "-provider", providers.Prometheus,
		"-log-level", "error",
	})
	require.NoError(t, err)
	alb := conf.Backends["default"].Clone()
	alb.Name = globalCacheALB
	alb.ALBOptions = ao.New()
	alb.ALBOptions.Pool = ao.Members(globalCacheMember)
	alb.Paths = paths
	return alb
}

// newGlobalCacheTest builds an evaluator keeping results in c, a new cache when nil, over one member
// whose reads are recorded
func newGlobalCacheTest(t *testing.T, settings merge.TSMGlobalSettings, c cache.Cache,
	paths ...*pathopts.Options,
) *globalCacheTest {
	t.Helper()
	if c == nil {
		c = globalTestCache(t)
	}
	base := time.Now().Truncate(time.Minute).Add(-3 * time.Hour).UnixMilli()
	gt := &globalCacheTest{baseMillis: base, cache: c, source: &recordingSource{
		store: recentStore(base),
	}}
	gt.req = backends.TSMGlobalRequest{
		Members:   []backends.TSMGlobalMember{{Name: globalCacheMember, Source: gt.source}},
		Namespace: globalNamespace,
	}
	ev, err := (&Client{}).NewTSMGlobalEvaluator(settings, backends.TSMGlobalCache{
		Cache: c, Backend: globalTestALB(t, paths...), Members: func() backends.TSMGlobalRequest {
			gt.mu.Lock()
			defer gt.mu.Unlock()
			return gt.req
		},
	})
	require.NoError(t, err)
	gt.evaluator = ev.(*globalEvaluator)
	return gt
}

func (gt *globalCacheTest) seconds(minute int) string {
	return strconv.FormatInt(gt.baseMillis/1000+int64(minute)*60, 10)
}

func (gt *globalCacheTest) rangeValues(query string, start, end int) url.Values {
	return url.Values{upQuery: {query}, upStart: {gt.seconds(start)}, upEnd: {gt.seconds(end)}, upStep: {"60"}}
}

func (gt *globalCacheTest) instantValues(query string, at int) url.Values {
	return url.Values{upQuery: {query}, upTime: {gt.seconds(at)}}
}

func (gt *globalCacheTest) request() backends.TSMGlobalRequest {
	gt.mu.Lock()
	defer gt.mu.Unlock()
	return gt.req
}

func (gt *globalCacheTest) setRequest(change func(*backends.TSMGlobalRequest)) {
	gt.mu.Lock()
	defer gt.mu.Unlock()
	change(&gt.req)
}

// globalHTTPRequest returns a request for path with values in its query, or as a form body for POST
func globalHTTPRequest(method, path string, values url.Values, header http.Header) *http.Request {
	var r *http.Request
	if method == http.MethodPost {
		r = httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	} else {
		r = httptest.NewRequest(method, path+"?"+values.Encode(), nil)
	}
	for k, values := range header {
		for _, v := range values {
			r.Header.Add(k, v)
		}
	}
	return r
}

func (gt *globalCacheTest) do(t *testing.T, method, path string, values url.Values, header http.Header,
) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	gt.evaluator.ServeTSMGlobal(w, globalHTTPRequest(method, path, values, header), gt.request())
	return w
}

// uncached answers the request as the evaluator does without a cache
func (gt *globalCacheTest) uncached(t *testing.T, settings merge.TSMGlobalSettings, path string,
	values url.Values,
) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	newGlobalEvaluator(settings).ServeTSMGlobal(w, globalHTTPRequest(http.MethodGet, path, values, nil),
		backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{
			Name:   globalCacheMember,
			Source: gt.source.memberSource,
		}}})
	return w
}

// resultValues decodes a response's result, ignoring its formatting
func resultValues(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Data struct {
			Result json.RawMessage `json:"result"`
		} `json:"data"`
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal(body, &resp), string(body))
	var result any
	require.NoError(t, json.Unmarshal(resp.Data.Result, &result))
	out, err := json.Marshal(map[string]any{"result": result, "warnings": resp.Warnings})
	require.NoError(t, err)
	return string(out)
}

func requireCacheStatus(t *testing.T, w *httptest.ResponseRecorder, want status.Label) {
	t.Helper()
	require.Contains(t, w.Header().Get(headers.NameTricksterResult), "status="+string(want), w.Body.String())
}

func TestGlobalEvaluationCacheKeepsWholeResults(t *testing.T) {
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	first := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
	requireCacheStatus(t, first, status.StatusKeyMiss)
	require.Equal(t, 1, gt.source.reads())
	require.Equal(t, resultValues(t, gt.uncached(t, globalTestSettings(), globalTestRangePath,
		gt.rangeValues(globalTestQuery, 0, 20)).Body.Bytes()), resultValues(t, first.Body.Bytes()))

	again := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
	requireCacheStatus(t, again, status.StatusHit)
	require.Equal(t, 1, gt.source.reads(), "a hit evaluates nothing")
	require.Equal(t, resultValues(t, first.Body.Bytes()), resultValues(t, again.Body.Bytes()))

	// an overlapping range is a whole evaluation of its own, never one assembled from cached parts
	extended := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 25), nil)
	requireCacheStatus(t, extended, status.StatusKeyMiss)
	require.Equal(t, 2, gt.source.reads())

	tenant := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20),
		http.Header{globalCacheTenant: {globalTenantA}})
	requireCacheStatus(t, tenant, status.StatusKeyMiss)
	require.Equal(t, 3, gt.source.reads(), "another tenant's results are kept apart")

	instant := gt.do(t, http.MethodPost, globalTestQueryPath, gt.instantValues(globalTestQuery, 10), nil)
	requireCacheStatus(t, instant, status.StatusKeyMiss)
	requireCacheStatus(t, gt.do(t, http.MethodPost, globalTestQueryPath, gt.instantValues(globalTestQuery, 10), nil),
		status.StatusHit)
	require.Equal(t, `{"result":[{"metric":{},"value":[`+gt.seconds(10)+`,"2"]}],"warnings":null}`,
		resultValues(t, instant.Body.Bytes()))
}

func TestGlobalEvaluationCacheSkipsPartialResults(t *testing.T) {
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	gt.setRequest(func(req *backends.TSMGlobalRequest) {
		req.Unavailable = []backends.TSMGlobalUnavailable{{Name: "down"}}
	})
	for i := range 2 {
		w := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
		require.Equal(t, http.StatusOK, w.Code)
		require.Contains(t, w.Body.String(), "member unavailable", "attempt %d", i)
		requireCacheStatus(t, w, status.StatusKeyMiss)
	}
	require.Equal(t, 2, gt.source.reads(), "a partial result is never cached")
	gt.setRequest(func(req *backends.TSMGlobalRequest) { req.Unavailable = nil })
	requireCacheStatus(t, gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil),
		status.StatusKeyMiss)
	requireCacheStatus(t, gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil),
		status.StatusHit)
}

func TestGlobalEvaluationCacheLimit(t *testing.T) {
	limited := func(values url.Values, limit string) url.Values {
		out := url.Values{}
		maps.Copy(out, values)
		out.Set(upLimit, limit)
		return out
	}
	for _, path := range []string{globalTestQueryPath, globalTestRangePath} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, limitedFirst := range []bool{true, false} {
				name := path + "/" + method + "/limited first " + strconv.FormatBool(limitedFirst)
				t.Run(name, func(t *testing.T) {
					gt := newGlobalCacheTest(t, globalTestSettings(), nil)
					values := gt.instantValues(presenceMetric, 10)
					if path == globalTestRangePath {
						values = gt.rangeValues(presenceMetric, 0, 20)
					}
					want := func(limit string) string {
						v := values
						if limit != "" {
							v = limited(values, limit)
						}
						return resultValues(t, gt.uncached(t, globalTestSettings(), path, v).Body.Bytes())
					}
					order := []string{"1", "", "2", "1", ""}
					if !limitedFirst {
						order = []string{"", "1", "2", "", "1"}
					}
					for _, limit := range order {
						v := values
						if limit != "" {
							v = limited(values, limit)
						}
						w := gt.do(t, method, path, v, nil)
						require.Equal(t, http.StatusOK, w.Code, w.Body.String())
						require.Equal(t, want(limit), resultValues(t, w.Body.Bytes()), "limit %q", limit)
					}
					require.Contains(t, want("1"), globalLimitWarning)
					require.NotContains(t, want(""), globalLimitWarning)
				})
			}
		}
	}
}

// boundedSource serves only the samples of its series from mint to maxt, as a member does
type boundedSource struct {
	global.SourceIdentity
	series []storage.Series
}

func (boundedSource) SeriesAPI() string { return po.SeriesAPIRemoteRead }

func (s boundedSource) Select(_ context.Context, mint, maxt int64, _ *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	var out []storage.Series
	for _, series := range s.series {
		lset := series.Labels()
		if !matchAll(matchers, lset) {
			continue
		}
		var samples []chunks.Sample
		it := series.Iterator(nil)
		for it.Next() != 0 {
			if at, f := it.At(); at >= mint && at <= maxt {
				samples = append(samples, presenceSample{t: at, f: f})
			}
		}
		out = append(out, storage.NewListSeries(lset, samples))
	}
	return out, nil, nil
}

func matchAll(matchers []*labels.Matcher, lset labels.Labels) bool {
	for _, m := range matchers {
		if !m.Matches(lset.Get(m.Name)) {
			return false
		}
	}
	return true
}

// replicaCounter returns a counter replica sampled every interval seconds from offset, rising by step
// and resetting at resetAt minutes, with a gap between gap[0] and gap[1] minutes
func replicaCounter(baseMillis int64, replica string, offset, interval int64, step float64, resetAt int,
	gap [2]int,
) storage.Series {
	var samples []chunks.Sample
	var v float64
	for ms := offset * 1000; ms < 60*presenceMinute; ms += interval * 1000 {
		minute := int(ms / presenceMinute)
		v += step
		if minute == resetAt {
			v = step
		}
		if minute >= gap[0] && minute < gap[1] {
			continue
		}
		samples = append(samples, presenceSample{t: baseMillis + ms, f: v})
	}
	return storage.NewListSeries(labels.FromStrings(labels.MetricName, globalCounterMetric, presencePodLabel,
		presencePodA, globalReplicaLabel, replica), samples)
}

func TestGlobalEvaluationCacheWholeRangesUnderPenalty(t *testing.T) {
	settings := globalTestSettings()
	settings.Deduplication = merge.TSMDeduplicationPenalty
	settings.ReplicaLabels = []string{globalReplicaLabel}
	gt := newGlobalCacheTest(t, settings, nil)
	// interleaved scrapes, replicas that disagree, a reset on one and a gap on each
	member := boundedSource{ID: globalRawIdentity, series: []storage.Series{
		replicaCounter(gt.baseMillis, "1", 0, 15, 1, 25, [2]int{12, 16}),
		replicaCounter(gt.baseMillis, "2", 7, 15, 2, 40, [2]int{30, 33}),
	}}
	gt.setRequest(func(req *backends.TSMGlobalRequest) {
		req.Members = []backends.TSMGlobalMember{{Name: globalCacheMember, Source: member}}
	})
	cold := func(values url.Values) string {
		w := httptest.NewRecorder()
		newGlobalEvaluator(settings).ServeTSMGlobal(w, globalHTTPRequest(http.MethodGet, globalTestRangePath, values, nil),
			backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: globalCacheMember, Source: member}}})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return resultValues(t, w.Body.Bytes())
	}
	for _, query := range []string{"rate(c[5m])", "increase(c[3m])", "c"} {
		// a cold range, the same range cut in two, then warm overlapping and extended ranges
		for _, span := range [][2]int{{5, 45}, {5, 25}, {25, 45}, {5, 45}, {15, 55}, {5, 55}} {
			values := gt.rangeValues(query, span[0], span[1])
			w := gt.do(t, http.MethodGet, globalTestRangePath, values, nil)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, cold(values), resultValues(t, w.Body.Bytes()), "%s %v", query, span)
		}
	}
}

// rawReads answers a range query, as an uncached evaluation would, and returns the member reads it took
func (gt *globalCacheTest) rawReads(t *testing.T, settings merge.TSMGlobalSettings, start, end int) [][2]int64 {
	t.Helper()
	return gt.rawReadsOf(t, settings, globalTestQuery, start, end)
}

func (gt *globalCacheTest) rawReadsOf(t *testing.T, settings merge.TSMGlobalSettings, query string, start, end int,
) [][2]int64 {
	t.Helper()
	gt.source.mu.Lock()
	gt.source.ranges = nil
	gt.source.mu.Unlock()
	values := gt.rangeValues(query, start, end)
	w := gt.do(t, http.MethodGet, globalTestRangePath, values, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, resultValues(t, gt.uncached(t, settings, globalTestRangePath, values).Body.Bytes()),
		resultValues(t, w.Body.Bytes()))
	gt.evaluator.raw.Wait()
	gt.source.mu.Lock()
	defer gt.source.mu.Unlock()
	return slices.Clone(gt.source.ranges)
}

func globalRawReads(settings merge.TSMGlobalSettings, s status.LookupStatus) float64 {
	return testutil.ToFloat64(metrics.ALBGlobalRawReads.WithLabelValues(settings.Name, s.String()))
}

func TestGlobalEvaluationRawCacheReadsOnlyWhatItLacks(t *testing.T) {
	settings := globalTestSettings()
	gt := newGlobalCacheTest(t, settings, nil)
	gt.source.ID = globalRawIdentity
	misses := globalRawReads(settings, status.LookupStatusKeyMiss)
	hits := globalRawReads(settings, status.LookupStatusPartialHit)
	require.NotEmpty(t, gt.rawReads(t, settings, 5, 45))
	require.Equal(t, misses+1, globalRawReads(settings, status.LookupStatusKeyMiss))
	// a later range reads only the samples past the earlier one, and a narrower one reads nothing
	later := gt.rawReads(t, settings, 10, 50)
	require.Len(t, later, 1)
	require.Equal(t, gt.baseMillis+45*presenceMinute+1, later[0][0])
	require.Equal(t, hits+1, globalRawReads(settings, status.LookupStatusPartialHit))
	require.Empty(t, gt.rawReads(t, settings, 20, 30))
}

func TestGlobalEvaluationRawCacheSubsumes(t *testing.T) {
	settings := globalTestSettings()
	gt := newGlobalCacheTest(t, settings, nil)
	gt.source.ID = globalRawIdentity
	subsumed := func() float64 {
		return testutil.ToFloat64(metrics.ALBGlobalRawSubsumed.WithLabelValues(settings.Name))
	}
	before := subsumed()
	require.NotEmpty(t, gt.rawReadsOf(t, settings, globalTestQuery, 5, 45))
	// a query selecting one pod of the metric is answered from the whole metric's blocks
	narrow := "sum(" + presenceMetric + "{" + presencePodLabel + `="` + presencePodA + `"})`
	require.Empty(t, gt.rawReadsOf(t, settings, narrow, 10, 40))
	require.Equal(t, before+1, subsumed())
}

func TestGlobalEvaluationRawCacheWindows(t *testing.T) {
	// each range is five minutes past the last, so a read that kept nothing reads all of the next one
	whole := func(t *testing.T, gt *globalCacheTest, settings merge.TSMGlobalSettings) {
		t.Helper()
		first := gt.rawReads(t, settings, 5, 45)
		require.Len(t, first, 1)
		shift := 5 * presenceMinute
		require.Equal(t, [][2]int64{{first[0][0] + shift, first[0][1] + shift}}, gt.rawReads(t, settings, 10, 50))
	}
	volatile := func(t *testing.T, settings merge.TSMGlobalSettings, window time.Duration) *globalCacheTest {
		t.Helper()
		gt := newGlobalCacheTest(t, settings, nil)
		gt.source.ID = globalRawIdentity
		alb := globalTestALB(t)
		alb.VolatileWindow = timeconv.Duration(window)
		ev, err := (&Client{}).NewTSMGlobalEvaluator(settings, backends.TSMGlobalCache{
			Cache: gt.cache, Backend: alb, Members: gt.request,
		})
		require.NoError(t, err)
		gt.evaluator = ev.(*globalEvaluator)
		return gt
	}
	t.Run("the ALB's volatile window", func(t *testing.T) {
		// samples are 3h old at most, all within a 4h volatile window, so none is kept
		settings := globalTestSettings()
		whole(t, volatile(t, settings, 4*time.Hour), settings)
	})
	t.Run("a volatile window shorter than the lookback", func(t *testing.T) {
		// samples up to a minute ago are kept, so a range ending two minutes ago is cached whole, whatever
		// window the member has
		settings := globalTestSettings()
		gt := volatile(t, settings, time.Minute)
		gt.source.Volatile = po.SeriesVolatileWindow
		gt.source.store = recentStore(gt.baseMillis + 2*time.Hour.Milliseconds())
		require.NotEmpty(t, gt.rawReads(t, settings, 170, 178))
		require.Empty(t, gt.rawReads(t, settings, 171, 178))
	})
	t.Run("each member's own volatile window", func(t *testing.T) {
		// without the ALB's, a mimir or thanos member's last 5 minutes are read again, and another member's aren't
		for window, again := range map[time.Duration]bool{po.SeriesVolatileWindow: true, 0: false} {
			settings := globalTestSettings()
			gt := newGlobalCacheTest(t, settings, nil)
			gt.source.ID, gt.source.Volatile = globalRawIdentity, window
			gt.source.store = recentStore(gt.baseMillis + 2*time.Hour.Milliseconds())
			first := gt.rawReads(t, settings, 170, 178)
			require.Len(t, first, 1)
			reads := gt.rawReads(t, settings, 171, 178)
			if !again {
				require.Empty(t, reads)
				continue
			}
			require.Len(t, reads, 1)
			// now is within a minute past base+180m, so the window starts within a minute past base+175m
			require.GreaterOrEqual(t, reads[0][0], gt.baseMillis+175*presenceMinute, "only the window is read")
			require.Equal(t, first[0][1], reads[0][1])
		}
	})
	t.Run("blocks of any size", func(t *testing.T) {
		// blocks are kept whole, as the delta cache keeps its objects, whatever the ALB's object size limit
		settings := globalTestSettings()
		gt := newGlobalCacheTest(t, settings, nil)
		gt.source.ID = globalRawIdentity
		alb := globalTestALB(t)
		alb.MaxObjectSizeBytes = 64
		ev, err := (&Client{}).NewTSMGlobalEvaluator(settings, backends.TSMGlobalCache{
			Cache: gt.cache, Backend: alb, Members: gt.request,
		})
		require.NoError(t, err)
		gt.evaluator = ev.(*globalEvaluator)
		require.NotEmpty(t, gt.rawReads(t, settings, 5, 45))
		require.Empty(t, gt.rawReads(t, settings, 10, 40))
	})
	t.Run("the raw cache window", func(t *testing.T) {
		// samples 2h15m old or more are older than any block a 10m window keeps
		settings := globalTestSettings()
		settings.RawCacheWindow = 10 * time.Minute
		gt := newGlobalCacheTest(t, settings, nil)
		gt.source.ID = globalRawIdentity
		proxied := globalRawReads(settings, status.LookupStatusProxyOnly)
		whole(t, gt, settings)
		require.Equal(t, proxied+2, globalRawReads(settings, status.LookupStatusProxyOnly))
	})
}

func TestGlobalEvaluationCacheNamespaces(t *testing.T) {
	shared := globalTestCache(t)
	first := newGlobalCacheTest(t, globalTestSettings(), shared)
	values := first.rangeValues(globalTestQuery, 0, 20)
	requireCacheStatus(t, first.do(t, http.MethodGet, globalTestRangePath, values, nil), status.StatusKeyMiss)

	// a reload repoints the same-named member at another origin, so the pool's namespace changes
	reloaded := newGlobalCacheTest(t, globalTestSettings(), shared)
	reloaded.baseMillis = first.baseMillis
	reloaded.source.store = recentStore(first.baseMillis)[:1]
	reloaded.setRequest(func(req *backends.TSMGlobalRequest) { req.Namespace = globalOtherNS })
	w := reloaded.do(t, http.MethodGet, globalTestRangePath, values, nil)
	requireCacheStatus(t, w, status.StatusKeyMiss)
	require.Contains(t, w.Body.String(), `"1"`, "the reloaded member's own data is evaluated")
	require.NotContains(t, w.Body.String(), `"2"`, "the previous configuration's result is not served")

	// a member a discovery adds changes the namespace; removing it again restores the first one's results
	first.setRequest(func(req *backends.TSMGlobalRequest) { req.Namespace = globalOtherNS + "-added" })
	requireCacheStatus(t, first.do(t, http.MethodGet, globalTestRangePath, values, nil), status.StatusKeyMiss)
	first.setRequest(func(req *backends.TSMGlobalRequest) {
		req.Namespace = globalNamespace
		req.Unavailable = []backends.TSMGlobalUnavailable{{Name: globalSecondMember}}
	})
	requireCacheStatus(t, first.do(t, http.MethodGet, globalTestRangePath, values, nil), status.StatusHit)
	require.Equal(t, 2, first.source.reads(), "an outage of a configured member keeps complete results reusable")
}

func TestGlobalEvaluationCacheEvaluatesItsKeysPool(t *testing.T) {
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	shardA := memberSource{store: recentStore(gt.baseMillis)[:1]}
	shardB := memberSource{store: recentStore(gt.baseMillis)[1:]}
	pool := func(namespace string, sources ...memberSource) backends.TSMGlobalRequest {
		req := backends.TSMGlobalRequest{Namespace: namespace}
		for i, source := range sources {
			req.Members = append(req.Members, backends.TSMGlobalMember{Name: "m" + strconv.Itoa(i), Source: source})
		}
		return req
	}
	values := gt.rangeValues(globalCountQuery, 0, 20)
	query := func(req backends.TSMGlobalRequest, want status.Label) string {
		t.Helper()
		w := httptest.NewRecorder()
		gt.evaluator.ServeTSMGlobal(w, globalHTTPRequest(http.MethodGet, globalTestRangePath, values, nil), req)
		requireCacheStatus(t, w, want)
		return resultValues(t, w.Body.Bytes())
	}
	onlyA, both := pool(globalNamespace, shardA), pool(globalOtherNS, shardA, shardB)
	// a discovery adds shard B after a request took its snapshot, so a miss would see the larger pool
	gt.setRequest(func(req *backends.TSMGlobalRequest) { *req = both })
	one := query(onlyA, status.StatusKeyMiss)
	require.Contains(t, one, `"1"`)
	require.NotContains(t, one, `"2"`, "the result keyed for one shard holds one shard")
	two := query(both, status.StatusKeyMiss)
	require.Contains(t, two, `"2"`)
	// removing shard B again restores the first namespace's own result, never the larger pool's
	gt.setRequest(func(req *backends.TSMGlobalRequest) { *req = onlyA })
	require.Equal(t, one, query(onlyA, status.StatusHit))
	require.Equal(t, two, query(both, status.StatusHit))

	// a same-named member repointed elsewhere reads where its snapshot pointed
	repointed := pool(globalOtherNS+"-repointed", shardB)
	require.Contains(t, query(repointed, status.StatusKeyMiss), `"1"`)
	gt.setRequest(func(req *backends.TSMGlobalRequest) { *req = repointed })
	require.Equal(t, one, query(onlyA, status.StatusHit))

	// a miss no client owns, as a revalidation, reads the current pool, and keeps it only under its own key
	revalidate := func(keyed backends.TSMGlobalRequest) string {
		rt := gt.evaluator.cached.routesFor(keyed)
		r := request.SetResources(globalHTTPRequest(http.MethodGet, globalTestRangePath, values, nil),
			request.NewResources(rt.options, rt.rng, nil, nil, nil, nil))
		w := httptest.NewRecorder()
		gt.evaluator.serveOrigin(w, r)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		return w.Header().Get(headers.NameCacheControl)
	}
	require.Equal(t, headers.ValueNoStore, revalidate(onlyA))
	require.NotEqual(t, headers.ValueNoStore, revalidate(repointed))
}

// routed returns r as the router hands it to the ALB, with the ALB path that matched it
func routed(r *http.Request, matched *pathopts.Options) *http.Request {
	return request.SetResources(r, &request.Resources{PathConfig: matched})
}

func TestGlobalEvaluationCacheAliasRouteIdentity(t *testing.T) {
	route := func(path string, match matching.PathMatchType, name matching.PathMatchName, keyed bool) *pathopts.Options {
		pc := pathopts.New()
		pc.Path, pc.MatchType, pc.MatchTypeName = path, match, name
		pc.Methods = []string{http.MethodGet, http.MethodPost}
		if keyed {
			pc.CacheKeyHeaders = []string{globalTeamHeader}
		}
		return pc
	}
	// only the alias routes key by the team; the route the canonical path arrives on doesn't
	exact := route(globalAliasPrefix+globalTestRangePath, matching.PathMatchTypeExact, matching.PathMatchNameExact, true)
	prefix := route(globalAliasPrefix+"2/", matching.PathMatchTypePrefix, matching.PathMatchNamePrefix, true)
	canonical := route("/", matching.PathMatchTypePrefix, matching.PathMatchNamePrefix, false)
	newTest := func(t *testing.T) *globalCacheTest {
		gt := newGlobalCacheTest(t, globalTestSettings(), nil, exact, prefix)
		source := teamRemoteRead(t).(*global.RemoteRead)
		// a member with an identity, so its raw series are cached too
		source.ID = globalRawIdentity
		gt.setRequest(func(req *backends.TSMGlobalRequest) {
			req.Members = []backends.TSMGlobalMember{{Name: globalCacheMember, Source: source}}
		})
		return gt
	}
	team := func(name string) http.Header { return http.Header{globalTeamHeader: {name}} }
	ask := func(t *testing.T, gt *globalCacheTest, method, path string, matched *pathopts.Options, name string,
		want status.Label,
	) {
		t.Helper()
		values := gt.rangeValues(globalTestQuery, 0, 5)
		w := httptest.NewRecorder()
		gt.evaluator.ServeTSMGlobal(w, routed(globalHTTPRequest(method, path, values, team(name)), matched), gt.request())
		requireCacheStatus(t, w, want)
		value := `"1"`
		if name == globalTeamB {
			value = `"2"`
		}
		require.Contains(t, w.Body.String(), value, "%s %s %s", method, path, name)
	}
	for _, alias := range []struct {
		path    string
		matched *pathopts.Options
	}{{globalAliasPrefix + globalTestRangePath, exact}, {globalAliasPrefix + "2" + globalTestRangePath, prefix}} {
		t.Run(alias.matched.Path, func(t *testing.T) {
			gt := newTest(t)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				for _, name := range []string{globalTeamA, globalTeamB} {
					ask(t, gt, method, alias.path, alias.matched, name, status.StatusKeyMiss)
				}
				for _, name := range []string{globalTeamA, globalTeamB} {
					ask(t, gt, method, alias.path, alias.matched, name, status.StatusHit)
				}
			}
			// a canonical request and an alias request never answer each other, in either order
			canonicalFirst := newTest(t)
			ask(t, canonicalFirst, http.MethodGet, globalTestRangePath, canonical, globalTeamA, status.StatusKeyMiss)
			ask(t, canonicalFirst, http.MethodGet, alias.path, alias.matched, globalTeamB, status.StatusKeyMiss)
			aliasFirst := newTest(t)
			ask(t, aliasFirst, http.MethodGet, alias.path, alias.matched, globalTeamA, status.StatusKeyMiss)
			ask(t, aliasFirst, http.MethodGet, globalTestRangePath, canonical, globalTeamB, status.StatusKeyMiss)
		})
	}
}

func TestGlobalEvaluationCacheFailuresAreNotCached(t *testing.T) {
	tests := []struct {
		name     string
		settings func(*merge.TSMGlobalSettings)
		prepare  func(t *testing.T, gt *globalCacheTest) url.Values
		status   int
		errType  string
		// transient failures end, after which the query is answered and kept
		transient bool
	}{
		{name: "strict member failure", prepare: func(_ *testing.T, gt *globalCacheTest) url.Values {
			gt.source.failNext(errGlobalTestMember)
			values := gt.rangeValues(globalTestQuery, 0, 20)
			values.Set(partialResponseParam, "false")
			return values
		}, status: http.StatusServiceUnavailable, errType: global.ErrorTypeUnavailable, transient: true},
		{
			name: "fetch limit", settings: func(s *merge.TSMGlobalSettings) { s.MaxFetchedSamples = 10 },
			prepare: func(t *testing.T, gt *globalCacheTest) url.Values {
				// the fetch budget is counted as a member's answer is decoded
				source := teamRemoteRead(t)
				gt.setRequest(func(req *backends.TSMGlobalRequest) {
					req.Members = []backends.TSMGlobalMember{{Name: globalCacheMember, Source: source}}
				})
				return gt.rangeValues(globalTestQuery, 0, 20)
			}, status: http.StatusUnprocessableEntity, errType: global.ErrorTypeExecution,
		},
		{
			name: "engine limit", settings: func(s *merge.TSMGlobalSettings) { s.MaxSamples = 1 },
			prepare: func(_ *testing.T, gt *globalCacheTest) url.Values {
				return gt.rangeValues(presenceMetric, 0, 20)
			}, status: http.StatusUnprocessableEntity, errType: global.ErrorTypeExecution,
		},
		{name: "timeout", prepare: func(_ *testing.T, gt *globalCacheTest) url.Values {
			values := gt.rangeValues(globalTestQuery, 0, 20)
			values.Set(timeoutParam, "0.000000001")
			return values
		}, status: http.StatusServiceUnavailable, errType: global.ErrorTypeTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := globalTestSettings()
			if test.settings != nil {
				test.settings(&settings)
			}
			gt := newGlobalCacheTest(t, settings, nil)
			// a negative cache the alb names keeps no failed evaluation either
			alb := globalTestALB(t)
			alb.NegativeCacheName, alb.NegativeCache = globalNegativeCache, negative.Lookup{test.status: time.Hour}
			ev, err := (&Client{}).NewTSMGlobalEvaluator(settings, backends.TSMGlobalCache{
				Cache: gt.cache, Backend: alb, Members: gt.request,
			})
			require.NoError(t, err)
			gt.evaluator = ev.(*globalEvaluator)
			values := test.prepare(t, gt)
			w := gt.do(t, http.MethodGet, globalTestRangePath, values, nil)
			require.Equal(t, test.status, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), test.errType)
			w = gt.do(t, http.MethodGet, globalTestRangePath, values, nil)
			requireCacheStatus(t, w, status.StatusKeyMiss)
			if !test.transient {
				require.Equal(t, test.status, w.Code, "the query is evaluated, and fails, again")
				return
			}
			requireCacheStatus(t, w, status.StatusKeyMiss)
			requireCacheStatus(t, gt.do(t, http.MethodGet, globalTestRangePath, values, nil), status.StatusHit)
		})
	}
}

func TestGlobalEvaluationValidatesBeforeReading(t *testing.T) {
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	with := func(values url.Values, kv ...string) url.Values {
		out := url.Values{}
		maps.Copy(out, values)
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	rng := gt.rangeValues(globalTestQuery, 0, 20)
	invalid := map[string]url.Values{
		"zero duration step":     with(rng, upStep, "0s"),
		"zero minutes step":      with(rng, upStep, "0m"),
		"negative duration step": with(rng, upStep, "-1m"),
		"sub-millisecond step":   with(rng, upStep, "0.0005"),
		"fractional step":        with(rng, upStep, "0.0015"),
		"too many points":        with(rng, upStep, "0.001"),
		"zero timeout":           with(rng, timeoutParam, "0s"),
		"negative timeout":       with(rng, timeoutParam, "-1s"),
		"negative limit":         with(rng, upLimit, "-1"),
		"word limit":             with(rng, upLimit, "all"),
		"partial response":       with(rng, partialResponseParam, "sometimes"),
		"instant time":           with(gt.instantValues(globalTestQuery, 1), upTime, "soon"),
	}
	for name, values := range invalid {
		path := globalTestRangePath
		if values.Has(upTime) {
			path = globalTestQueryPath
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			cached := gt.do(t, method, path, values, nil)
			require.Equal(t, http.StatusBadRequest, cached.Code, "%s %s: %s", name, method, cached.Body.String())
			require.Contains(t, cached.Body.String(), global.ErrorTypeBadData, name)
			w := httptest.NewRecorder()
			newGlobalEvaluator(globalTestSettings()).ServeTSMGlobal(w, globalHTTPRequest(method, path, values, nil),
				backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{
					Name:   globalCacheMember,
					Source: gt.source,
				}}})
			require.Equal(t, http.StatusBadRequest, w.Code, "%s %s uncached: %s", name, method, w.Body.String())
		}
	}
	require.Zero(t, gt.source.reads(), "invalid requests read nothing")
}

// cancelingSource holds every read until its context ends, reporting each read that starts
type cancelingSource struct {
	global.SourceIdentity
	started  chan struct{}
	canceled atomic.Int32
}

func (*cancelingSource) SeriesAPI() string { return po.SeriesAPIRemoteRead }

func (s *cancelingSource) Select(ctx context.Context, _, _ int64, _ *storage.SelectHints,
	_ ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	s.canceled.Add(1)
	return nil, nil, ctx.Err()
}

func TestGlobalEvaluationCacheCancellation(t *testing.T) {
	settings := globalTestSettings()
	settings.MaxConcurrentQueries = 1
	settings.QueryTimeout = time.Hour
	gt := newGlobalCacheTest(t, settings, nil)
	blocking := &cancelingSource{started: make(chan struct{}, 1)}
	gt.setRequest(func(req *backends.TSMGlobalRequest) {
		req.Members = []backends.TSMGlobalMember{{Name: globalCacheMember, Source: blocking}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w := httptest.NewRecorder()
		r := globalHTTPRequest(http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
		gt.evaluator.ServeTSMGlobal(w, r.WithContext(ctx), gt.request())
	}()
	<-blocking.started
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the canceled client's evaluation kept reading")
	}
	require.Equal(t, int32(1), blocking.canceled.Load(), "the client's cancellation reaches the raw read")

	// the slot the canceled evaluation held is free again
	gt.setRequest(func(req *backends.TSMGlobalRequest) {
		req.Members = []backends.TSMGlobalMember{{Name: globalCacheMember, Source: gt.source}}
	})
	requireCacheStatus(t, gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil),
		status.StatusKeyMiss)
}

func TestGlobalEvaluationCacheRetriesAnotherClientsCancellation(t *testing.T) {
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	cg := gt.evaluator.cached
	dispatch := cg.dispatch
	var calls atomic.Int32
	// the evaluation a request joins answers with a cancellation when the client that started it goes
	// away, every time here, as an unlucky request could see each time it joins one
	cg.dispatch = func(w http.ResponseWriter, r *http.Request, isRange bool) {
		if calls.Add(1) < maxJoinAttempts {
			global.WriteError(w, statusClientClosedRequest, global.ErrorTypeCanceled, context.Canceled, nil)
			return
		}
		dispatch(w, r, isRange)
	}
	w := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, int32(maxJoinAttempts), calls.Load(), "the request ran again rather than answer with "+
		"another's cancellation")
	require.NotContains(t, w.Body.String(), global.ErrorTypeCanceled)

	calls.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := globalHTTPRequest(http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 30), nil)
	w = httptest.NewRecorder()
	gt.evaluator.ServeTSMGlobal(w, r.WithContext(ctx), gt.request())
	require.Equal(t, statusClientClosedRequest, w.Code, "a request its own client canceled isn't retried")
	require.Equal(t, int32(1), calls.Load())

	calls.Store(-maxJoinAttempts)
	w = gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 40), nil)
	require.Equal(t, statusClientClosedRequest, w.Code, "retries end, answering with what the last attempt got")
	require.Equal(t, int32(0), calls.Load())
}

// teamRemoteRead serves a SAMPLES remote-read answer whose one series holds the value its team header picks
func teamRemoteRead(t *testing.T) backends.TSMGlobalSource {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		data, err := snappy.Decode(nil, body)
		require.NoError(t, err)
		var req prompb.ReadRequest
		require.NoError(t, req.Unmarshal(data))
		value := 1.0
		if r.Header.Get(globalTeamHeader) == globalTeamB {
			value = 2
		}
		q := req.Queries[0]
		var samples []prompb.Sample
		for ms := q.StartTimestampMs - q.StartTimestampMs%presenceMinute; ms <= q.EndTimestampMs; ms += presenceMinute {
			samples = append(samples, prompb.Sample{Timestamp: ms, Value: value})
		}
		resp := prompb.ReadResponse{Results: []*prompb.QueryResult{{Timeseries: []*prompb.TimeSeries{{
			Labels:  []prompb.Label{{Name: labels.MetricName, Value: presenceMetric}},
			Samples: samples,
		}}}}}
		out, err := resp.Marshal()
		require.NoError(t, err)
		_, _ = w.Write(snappy.Encode(nil, out))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return global.NewRemoteRead(u, srv.Client(), nil)
}

func TestGlobalEvaluationCacheConfiguredIdentityHeaders(t *testing.T) {
	configured := func(path string) *pathopts.Options {
		pc := pathopts.New()
		pc.Path, pc.MatchType, pc.MatchTypeName = path, matching.PathMatchTypeExact, matching.PathMatchNameExact
		pc.Methods = []string{http.MethodGet, http.MethodPost}
		pc.CacheKeyHeaders = []string{globalTeamHeader}
		return pc
	}
	tests := []struct {
		name  string
		paths []*pathopts.Options
		// members' configurations key their own answers by the header
		memberKeyed bool
	}{
		{name: "on the alb's paths", paths: []*pathopts.Options{
			configured(globalTestQueryPath),
			configured(globalTestRangePath),
		}},
		{name: "on the members' paths", memberKeyed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gt := newGlobalCacheTest(t, globalTestSettings(), nil, test.paths...)
			source := teamRemoteRead(t)
			gt.setRequest(func(req *backends.TSMGlobalRequest) {
				req.Members = []backends.TSMGlobalMember{{Name: globalCacheMember, Source: source}}
				if test.memberKeyed {
					req.KeyHeaders = []string{globalTeamHeader}
				}
			})
			team := func(name string) http.Header { return http.Header{globalTeamHeader: {name}} }
			values := gt.rangeValues(globalTestQuery, 0, 5)
			valueOf := func(w *httptest.ResponseRecorder) string {
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				switch {
				case strings.Contains(w.Body.String(), `"1"`):
					return globalTeamA
				case strings.Contains(w.Body.String(), `"2"`):
					return globalTeamB
				}
				return ""
			}
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				for _, name := range []string{globalTeamA, globalTeamB} {
					w := gt.do(t, method, globalTestRangePath, values, team(name))
					requireCacheStatus(t, w, status.StatusKeyMiss)
					require.Equal(t, name, valueOf(w), method)
				}
				for _, name := range []string{globalTeamA, globalTeamB} {
					w := gt.do(t, method, globalAliasPrefix+globalTestRangePath, values, team(name))
					requireCacheStatus(t, w, status.StatusHit)
					require.Equal(t, name, valueOf(w), "%s alias", method)
				}
			}
		})
	}
}

func TestJoinWriter(t *testing.T) {
	live := context.Background()
	w := httptest.NewRecorder()
	jw := &joinWriter{w: w, ctx: live, header: make(http.Header), retry: true}
	jw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	jw.WriteHeader(statusClientClosedRequest)
	_, _ = jw.Write([]byte("canceled"))
	require.True(t, jw.dropped, "another request's cancellation is dropped")
	require.Empty(t, w.Body.String())
	require.Empty(t, w.Header().Get(headers.NameContentType))

	w = httptest.NewRecorder()
	jw = &joinWriter{w: w, ctx: live, header: make(http.Header)}
	jw.WriteHeader(statusClientClosedRequest)
	require.False(t, jw.dropped, "the last attempt passes whatever it gets")
	require.Equal(t, statusClientClosedRequest, w.Code)

	w = httptest.NewRecorder()
	jw = &joinWriter{w: w, ctx: live, header: make(http.Header), retry: true}
	jw.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
	_, _ = jw.Write([]byte("ok"))
	jw.WriteHeader(http.StatusTeapot)
	require.Equal(t, http.StatusOK, w.Code, "the first write fixes the status")
	require.Equal(t, "ok", w.Body.String())
	require.Equal(t, headers.ValueApplicationJSON, w.Header().Get(headers.NameContentType))
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
	prefix := newGlobalEvaluator(globalTestSettings()).cacheKeyPrefix()
	penalty := globalTestSettings()
	penalty.Deduplication = merge.TSMDeduplicationPenalty
	require.NotEqual(t, prefix, newGlobalEvaluator(penalty).cacheKeyPrefix(), "settings change the key")
	labelled := globalTestSettings()
	labelled.ReplicaLabels = []string{globalReplicaLabel}
	require.NotEqual(t, prefix, newGlobalEvaluator(labelled).cacheKeyPrefix())

	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	cg := gt.evaluator.cached
	ns := cg.routesFor(backends.TSMGlobalRequest{Namespace: globalNamespace})
	require.Same(t, ns, cg.routesFor(backends.TSMGlobalRequest{Namespace: globalNamespace}), "kept while unchanged")
	require.Equal(t, prefix+"."+globalNamespace, ns.options.CacheKeyPrefix)
	other := cg.routesFor(backends.TSMGlobalRequest{Namespace: globalOtherNS, KeyHeaders: []string{globalTeamHeader}})
	require.Equal(t, prefix+"."+globalOtherNS, other.options.CacheKeyPrefix)
	require.Contains(t, other.rng.CacheKeyHeaders, globalTeamHeader)
	require.NotContains(t, cg.rng.CacheKeyHeaders, globalTeamHeader, "the shared routes are unchanged")
}

func TestGlobalCacheControl(t *testing.T) {
	e := newGlobalEvaluator(globalTestSettings())
	e.ttl = time.Hour
	now := time.Now()
	old := globalQuery{isRange: true, end: now.Add(-time.Hour), step: time.Minute}
	recent := globalQuery{isRange: true, end: now.Add(-time.Minute), step: time.Minute}
	require.Equal(t, sharedMaxAge(time.Hour), e.cacheControl(old, now))
	require.Equal(t, sharedMaxAge(instantSharedMaxAge), e.cacheControl(recent, now))
	require.Equal(t, sharedMaxAge(instantSharedMaxAge), e.cacheControl(globalQuery{}, now), "an instant at now")
	require.Equal(t, sharedMaxAge(time.Hour), e.cacheControl(globalQuery{at: now.Add(-time.Hour)}, now))
	e.volatileWindow = 2 * time.Hour
	require.Equal(t, sharedMaxAge(instantSharedMaxAge), e.cacheControl(old, now), "a volatile window widens recency")
	e.volatileWindow, e.volatilePoints = 0, 90
	require.Equal(t, sharedMaxAge(instantSharedMaxAge), e.cacheControl(old, now), "volatile points count steps")
	e.ttl = 10 * time.Second
	require.Equal(t, sharedMaxAge(10*time.Second), e.cacheControl(recent, now), "never past the ttl")
	e.ttl = 0
	require.Equal(t, sharedMaxAge(instantSharedMaxAge), e.cacheControl(old, now))
}
