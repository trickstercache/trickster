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

package global

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	cachemanager "github.com/trickstercache/trickster/v2/pkg/cache/manager"
	cachememory "github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	rawTestStep     = int64(15_000)
	rawTestMinute   = int64(time.Minute / time.Millisecond)
	rawTestHour     = int64(time.Hour / time.Millisecond)
	rawTestVolatile = 5 * time.Minute
	rawTestChunk    = 120
	rawTestMember   = "member-a"
	rawTestIdentity = "identity-a"
	rawTestALB      = "alb"
	rawTestTeamA    = "team-a"
	rawTestTeamB    = "team-b"
	rawTestEnd      = 10 * rawTestHour
)

// chunkedSource answers as a member does over remote read, with every chunk a read overlaps, whole
type chunkedSource struct {
	SourceIdentity
	paths    po.List
	series   []*chunkSeries
	err      error
	warnings annotations.Annotations
	// gate, when set, holds every read until it closes or the read's context ends
	gate chan struct{}

	mu    sync.Mutex
	reads [][2]int64
	sent  []http.Header
}

func (*chunkedSource) SeriesAPI() string { return prop.SeriesAPIRemoteRead }

func (s *chunkedSource) sentHeaders(in *http.Request) http.Header {
	return forwardedHeaders(s.paths, in)
}

func (s *chunkedSource) Select(ctx context.Context, mint, maxt int64, _ *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.mu.Lock()
	s.reads = append(s.reads, [2]int64{mint, maxt})
	s.sent = append(s.sent, s.sentHeaders(requestFrom(ctx)))
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, nil, s.err
	}
	var out []storage.Series
	for _, cs := range s.series {
		if slices.ContainsFunc(matchers, func(m *labels.Matcher) bool { return !m.Matches(cs.lset.Get(m.Name)) }) {
			continue
		}
		part := &chunkSeries{lset: cs.lset}
		for i, sp := range cs.spans {
			if sp.maxt >= mint && sp.mint <= maxt {
				part.chunks, part.spans = append(part.chunks, cs.chunks[i]), append(part.spans, sp)
			}
		}
		if len(part.chunks) > 0 {
			out = append(out, part)
		}
	}
	return out, s.warnings, nil
}

func (s *chunkedSource) takeReads() [][2]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	reads := s.reads
	s.reads = nil
	// concurrent reads of a select's gaps arrive in any order
	slices.SortFunc(reads, func(a, b [2]int64) int { return int(a[0] - b[0]) })
	return reads
}

// chunked encodes points as a member's series, in chunks of 120 samples as Prometheus cuts them
func chunked(t testing.TB, lset labels.Labels, points []testPoint) *chunkSeries {
	t.Helper()
	cs := &chunkSeries{lset: lset}
	for from := 0; from < len(points); from += rawTestChunk {
		to := min(from+rawTestChunk, len(points))
		c := chunkenc.NewXORChunk()
		app, err := c.Appender()
		require.NoError(t, err)
		for _, p := range points[from:to] {
			app.Append(0, p.t, p.f)
		}
		cs.chunks = append(cs.chunks, c)
		cs.spans = append(cs.spans, span{mint: points[from].t, maxt: points[to-1].t})
	}
	return cs
}

// counter returns a counter scraped every 15s from offset until rawTestEnd, rising by one each scrape and
// resetting at reset, with no samples during the gap
func counter(offset, reset int64, gap [2]int64) []testPoint {
	var out []testPoint
	v := 0.0
	for ts := offset; ts <= rawTestEnd; ts += rawTestStep {
		if ts == reset {
			v = 0
		}
		v++
		if ts >= gap[0] && ts <= gap[1] {
			continue
		}
		out = append(out, testPoint{t: ts, f: v})
	}
	return out
}

func rawTestSource(t *testing.T) *chunkedSource {
	t.Helper()
	return &chunkedSource{ID: rawTestIdentity, series: []*chunkSeries{
		chunked(t, podLabels("a"), counter(0, 3*rawTestHour, [2]int64{})),
		chunked(t, podLabels("b"), counter(5000, -1, [2]int64{6 * rawTestHour, 6*rawTestHour + 10*rawTestMinute})),
	}}
}

type rawTestCache struct {
	*RawCache
	mu       sync.Mutex
	now      time.Time
	statuses []status.LookupStatus
}

func newRawTestCache(c cache.Cache, opts RawCacheOptions) *rawTestCache {
	rc := &rawTestCache{now: time.UnixMilli(rawTestEnd)}
	opts.Name, opts.TTL = rawTestALB, time.Hour
	if opts.VolatileWindow == nil {
		opts.VolatileWindow = new(rawTestVolatile)
	}
	opts.Observe = func(s status.LookupStatus) {
		rc.mu.Lock()
		rc.statuses = append(rc.statuses, s)
		rc.mu.Unlock()
	}
	rc.RawCache = NewRawCache(c, opts)
	rc.RawCache.now = func() time.Time {
		rc.mu.Lock()
		defer rc.mu.Unlock()
		return rc.now
	}
	return rc
}

func (rc *rawTestCache) setNow(ms int64) {
	rc.mu.Lock()
	rc.now = time.UnixMilli(ms)
	rc.mu.Unlock()
}

func (rc *rawTestCache) takeStatuses() []status.LookupStatus {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := rc.statuses
	rc.statuses = nil
	return out
}

// selectAt reads src's series of the test metric from mint to maxt for a request carrying header
func (rc *rawTestCache) selectAt(t *testing.T, ctx context.Context, src Source, mint, maxt int64,
) ([]storage.Series, annotations.Annotations, error) {
	t.Helper()
	series, warnings, err := rc.Select(ctx, Member{Name: rawTestMember, Source: src}, mint, maxt,
		&storage.SelectHints{Start: mint, End: maxt},
		[]*labels.Matcher{labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric)})
	// what a read keeps is stored as it is delivered, so the test waits for it before the next read
	rc.Wait()
	return series, warnings, err
}

// teamRequest returns a context carrying a query request from team, which keys raw series by its tenant
func teamRequest(team string) context.Context {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/query_range", nil)
	if team != "" {
		r.Header.Set(testTenantHeader, team)
	}
	return WithKeyHeaders(WithRequest(context.Background(), r), []string{testTenantHeader})
}

// pointsOf reads every float sample of series, by labels, leaving out a series without one

func pointsOf(t *testing.T, series []storage.Series) map[string][]testPoint {
	t.Helper()
	out := make(map[string][]testPoint, len(series))
	for _, s := range series {
		it := s.Iterator(nil)
		var points []testPoint
		for vt := it.Next(); vt != chunkenc.ValNone; vt = it.Next() {
			require.Equal(t, chunkenc.ValFloat, vt)
			ts, v := it.At()
			points = append(points, testPoint{t: ts, f: v})
		}
		require.NoError(t, it.Err())
		if len(points) > 0 {
			out[s.Labels().String()] = points
		}
	}
	return out
}

// wantPoints returns src's samples from mint to maxt, as an exact read returns them
func wantPoints(t *testing.T, src *chunkedSource, mint, maxt int64) map[string][]testPoint {
	t.Helper()
	all := make([]storage.Series, len(src.series))
	for i, s := range src.series {
		all[i] = s
	}
	return pointsOf(t, bound(all, mint, maxt))
}

type rawByteCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (*rawByteCache) Connect() error                       { return nil }
func (*rawByteCache) Close() error                         { return nil }
func (*rawByteCache) Configuration() *cacheoptions.Options { return &cacheoptions.Options{} }

func (c *rawByteCache) Remove(keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range keys {
		delete(c.data, k)
	}
	return nil
}

func (c *rawByteCache) Store(key string, data []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = make(map[string][]byte)
	}
	c.data[key] = slices.Clone(data)
	return nil
}

func (c *rawByteCache) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.data[key]
	if !ok {
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	return slices.Clone(data), status.LookupStatusHit, nil
}

// rawTestCaches are the two ways a cache holds blocks: as shared references, and encoded
func rawTestCaches(t *testing.T) map[string]func() cache.Cache {
	t.Helper()
	return map[string]func() cache.Cache{
		"memory": func() cache.Cache {
			conf := cacheoptions.New()
			conf.Name, conf.Provider = rawTestALB, cacheproviders.Memory
			c := cachemanager.NewCache(cachememory.New(conf.Name, conf), cachemanager.CacheOptions{}, conf)
			require.NoError(t, c.Connect())
			t.Cleanup(func() { _ = c.Close() })
			return c
		},
		"bytes": func() cache.Cache { return &rawByteCache{} },
	}
}

func TestRawCacheReadsOnlyWhatItLacks(t *testing.T) {
	volatile := rawTestVolatile.Milliseconds()
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			src := rawTestSource(t)
			rc := newRawTestCache(newCache(), RawCacheOptions{})
			ctx := teamRequest(rawTestTeamA)
			read := func(mint, maxt int64) {
				t.Helper()
				series, _, err := rc.selectAt(t, ctx, src, mint, maxt)
				require.NoError(t, err)
				require.Equal(t, wantPoints(t, src, mint, maxt), pointsOf(t, series))
			}

			// a cold read reads the whole range, and keeps what's older than the volatile window
			read(4*rawTestHour+7, rawTestEnd)
			require.Equal(t, [][2]int64{{4*rawTestHour + 7, rawTestEnd}}, src.takeReads())
			// the same read again reads only the volatile window
			read(4*rawTestHour+7, rawTestEnd)
			require.Equal(t, [][2]int64{{rawTestEnd - volatile + 1, rawTestEnd}}, src.takeReads())
			// a minute later, the window slides: what became old enough is read once, with the volatile window
			rc.setNow(rawTestEnd + rawTestMinute)
			read(4*rawTestHour+rawTestMinute, rawTestEnd+rawTestMinute)
			require.Equal(t, [][2]int64{{rawTestEnd - volatile + 1, rawTestEnd + rawTestMinute}}, src.takeReads())
			read(4*rawTestHour+rawTestMinute, rawTestEnd+rawTestMinute)
			require.Equal(t, [][2]int64{{rawTestEnd + rawTestMinute - volatile + 1, rawTestEnd + rawTestMinute}},
				src.takeReads())
			// reaching further back reads only the older part, beside the volatile window
			read(2*rawTestHour, rawTestEnd+rawTestMinute)
			require.Equal(t, [][2]int64{
				{2 * rawTestHour, 4*rawTestHour + 6}, {rawTestEnd + rawTestMinute - volatile + 1, rawTestEnd + rawTestMinute},
			}, src.takeReads())
			// a historical range, wholly cached, reads nothing
			read(3*rawTestHour, 5*rawTestHour)
			require.Empty(t, src.takeReads())
			require.Equal(t, []status.LookupStatus{
				status.LookupStatusKeyMiss, status.LookupStatusHit, status.LookupStatusPartialHit, status.LookupStatusHit,
				status.LookupStatusPartialHit, status.LookupStatusHit,
			}, rc.takeStatuses())
		})
	}
}

func TestRawCacheKeysByIdentity(t *testing.T) {
	src := rawTestSource(t)
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	const mint, maxt = 4 * rawTestHour, 6 * rawTestHour
	step := 0
	cold := func(ctx context.Context, src Source, want int) {
		t.Helper()
		step++
		_, _, err := rc.selectAt(t, ctx, src, mint, maxt)
		require.NoError(t, err)
		require.Len(t, src.(*chunkedSource).takeReads(), want, "read %d", step)
	}
	cold(teamRequest(rawTestTeamA), src, 1)
	cold(teamRequest(rawTestTeamA), src, 0)
	// another tenant, or none, never reads team A's series
	cold(teamRequest(rawTestTeamB), src, 1)
	cold(teamRequest(""), src, 1)
	// nor does another credential
	authorized := teamRequest(rawTestTeamA)
	requestFrom(authorized).Header.Set(headers.NameAuthorization, "Bearer other")
	cold(authorized, src, 1)
	// a header the member's own configuration keys by partitions too
	keyed := rawTestSource(t)
	keyed.Headers = []string{testConfigHeader}
	cold(teamRequest(rawTestTeamA), keyed, 1)
	configured := teamRequest(rawTestTeamA)
	requestFrom(configured).Header.Set(testConfigHeader, testConfigValue)
	cold(configured, keyed, 1)
	// another member's identity is another key, and a source without one is never cached
	other := rawTestSource(t)
	other.ID = "identity-b"
	cold(teamRequest(rawTestTeamA), other, 1)
	anonymous := rawTestSource(t)
	anonymous.ID = ""
	cold(teamRequest(rawTestTeamA), anonymous, 1)
	cold(teamRequest(rawTestTeamA), anonymous, 1)

	// a member configured to send its own tenant answers every client alike, so they share its series
	fixed := rawTestSource(t)
	fixed.ID = "identity-fixed"
	pc := po.New()
	pc.Path = testQueryPath
	pc.RequestHeaders = map[string]string{testTenantHeader: rawTestTeamA}
	fixed.paths = po.List{pc}
	cold(teamRequest(rawTestTeamA), fixed, 1)
	cold(teamRequest(rawTestTeamB), fixed, 0)

	// matchers in another order are the same key
	k1, ok := rc.key(teamRequest(rawTestTeamA), src, []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, testPod, "a"), labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric),
	})
	require.True(t, ok)
	k2, _ := rc.key(teamRequest(rawTestTeamA), src, []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric), labels.MustNewMatcher(labels.MatchEqual, testPod, "a"),
	})
	require.Equal(t, k1.key, k2.key)
	require.Equal(t, testMetric, k1.metric)
	k3, _ := rc.key(teamRequest(rawTestTeamA), src, []*labels.Matcher{
		labels.MustNewMatcher(labels.MatchRegexp, testPod, "a"), labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric),
	})
	require.NotEqual(t, k1.key, k3.key)
	require.Equal(t, k1.scope, k3.scope, "the scope is the identity alone")
}

func TestRawCacheKeepsOnlyCompleteAnswers(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 6 * rawTestHour
	ctx := teamRequest(rawTestTeamA)
	t.Run("incomplete", func(t *testing.T) {
		src := rawTestSource(t)
		src.warnings = annotations.New().Add(errors.New("store unavailable"))
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		for range 2 {
			_, warnings, err := rc.selectAt(t, ctx, src, mint, maxt)
			require.NoError(t, err)
			require.Len(t, warnings, 1)
			require.Equal(t, [][2]int64{{mint, maxt}}, src.takeReads())
		}
	})
	t.Run("failed", func(t *testing.T) {
		src := rawTestSource(t)
		src.err = errRemoteRead
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		_, _, err := rc.selectAt(t, ctx, src, mint, maxt)
		require.ErrorIs(t, err, errRemoteRead)
		src.err = nil
		_, _, err = rc.selectAt(t, ctx, src, mint, maxt)
		require.NoError(t, err)
		require.Equal(t, [][2]int64{{mint, maxt}, {mint, maxt}}, src.takeReads())
	})
	t.Run("panicked", func(t *testing.T) {
		prev := logger.Logger()
		logger.SetLogger(logging.NoopLogger())
		t.Cleanup(func() { logger.SetLogger(prev) })
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		_, _, err := rc.selectAt(t, ctx, &panicSource{MemorySource{ID: rawTestIdentity}},
			mint, maxt)
		require.ErrorIs(t, err, errSourcePanic)
	})
}

func TestRawCacheCountsWhatItServes(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 6 * rawTestHour
	src := rawTestSource(t)
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, mint, maxt)
	require.NoError(t, err)
	// a cached answer meets an evaluation's limits as the member's own would
	for _, budget := range []*Budget{{MaxSamples: 100}, {MaxSeries: 1}} {
		_, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), src, mint, maxt)
		require.ErrorIs(t, err, ErrFetchLimit)
	}
	budget := &Budget{}
	_, _, err = rc.selectAt(t, WithBudget(teamRequest(rawTestTeamA), budget), src, mint, maxt)
	require.NoError(t, err)
	series, samples := budget.Fetched()
	require.Equal(t, int64(2), series)
	require.GreaterOrEqual(t, samples, int64(2*2*rawTestHour/rawTestStep))
	// a read too large for any evaluation stops while it is decoded
	limited := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxSamples: 100})
	_, _, err = limited.selectAt(t, teamRequest(rawTestTeamA), &countingRead{src}, mint, maxt)
	require.ErrorIs(t, err, ErrFetchLimit)
}

// countingRead counts what its source answers against the read's budget, as decoders do
type countingRead struct {
	*chunkedSource
}

func (c *countingRead) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	series, warnings, err := c.chunkedSource.Select(ctx, mint, maxt, hints, matchers...)
	for _, s := range series {
		var samples int64
		for _, c := range s.(*chunkSeries).chunks {
			samples += int64(c.(chunkenc.Chunk).NumSamples())
		}
		if err := budgetFrom(ctx).add(1, samples); err != nil {
			return nil, nil, err
		}
	}
	return series, warnings, err
}

func TestRawCacheReadsLateSamplesInTheVolatileWindow(t *testing.T) {
	const mint = 4 * rawTestHour
	src := rawTestSource(t)
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, mint, rawTestEnd)
	require.NoError(t, err)
	// samples of the last minutes change after the first read, as a lagging replica's arrive
	late := counter(0, 3*rawTestHour, [2]int64{})
	for i := range late {
		if late[i].t > rawTestEnd-rawTestVolatile.Milliseconds() {
			late[i].f += 1000
		}
	}
	src.series[0] = chunked(t, podLabels("a"), late)
	series, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, mint, rawTestEnd)
	require.NoError(t, err)
	require.Equal(t, wantPoints(t, src, mint, rawTestEnd), pointsOf(t, series))
}

func TestRawCacheReadsAgainWhatTheWindowNowHolds(t *testing.T) {
	const mint = 4 * rawTestHour
	c := &rawByteCache{}
	src := rawTestSource(t)
	_, _, err := newRawTestCache(c, RawCacheOptions{}).selectAt(t, teamRequest(rawTestTeamA), src, mint, rawTestEnd)
	require.NoError(t, err)
	src.takeReads()
	// after a reload widens the volatile window, what it now holds is read again rather than served
	wider := newRawTestCache(c, RawCacheOptions{VolatileWindow: new(time.Hour)})
	series, _, err := wider.selectAt(t, teamRequest(rawTestTeamA), src, mint, rawTestEnd)
	require.NoError(t, err)
	require.Equal(t, [][2]int64{{rawTestEnd - rawTestHour + 1, rawTestEnd}}, src.takeReads())
	require.Equal(t, wantPoints(t, src, mint, rawTestEnd), pointsOf(t, series))
}

func TestRawCacheUsesEachMembersVolatileWindow(t *testing.T) {
	const mint = 4 * rawTestHour
	ctx := teamRequest(rawTestTeamA)
	// one member's samples may change for 5 minutes, as a mimir or thanos member's may, and the other's not at all
	flavored, plain := rawTestSource(t), rawTestSource(t)
	flavored.Volatile = rawTestVolatile
	plain.ID = rawTestIdentity + "-plain"
	read := func(rc *rawTestCache, src *chunkedSource) [][2]int64 {
		t.Helper()
		_, _, err := rc.selectAt(t, ctx, src, mint, rawTestEnd)
		require.NoError(t, err)
		return src.takeReads()
	}
	t.Run("each member's own", func(t *testing.T) {
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		rc.opts.VolatileWindow = nil
		read(rc, flavored)
		read(rc, plain)
		require.Equal(t, [][2]int64{{rawTestEnd - rawTestVolatile.Milliseconds() + 1, rawTestEnd}}, read(rc, flavored))
		require.Empty(t, read(rc, plain))
	})
	t.Run("the ALB's, for every member", func(t *testing.T) {
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{VolatileWindow: new(time.Minute)})
		read(rc, flavored)
		read(rc, plain)
		for _, src := range []*chunkedSource{flavored, plain} {
			require.Equal(t, [][2]int64{{rawTestEnd - rawTestMinute + 1, rawTestEnd}}, read(rc, src))
		}
	})
}

func TestRawCacheSkipsWhatItCannotKeep(t *testing.T) {
	ctx := teamRequest(rawTestTeamA)
	for name, tc := range map[string]struct {
		window time.Duration
		bounds [2]int64
	}{
		"before the epoch":      {0, [2]int64{-2 * rawTestHour, -rawTestHour}},
		"older than its window": {4 * time.Hour, [2]int64{0, 3 * rawTestHour}},
		"start after the end":   {0, [2]int64{rawTestHour, 0}},
		"all volatile samples":  {0, [2]int64{rawTestEnd - rawTestMinute, rawTestEnd}},
	} {
		t.Run(name, func(t *testing.T) {
			src := rawTestSource(t)
			rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{Window: tc.window})
			for range 2 {
				series, _, err := rc.selectAt(t, ctx, src, tc.bounds[0], tc.bounds[1])
				require.NoError(t, err)
				require.Equal(t, wantPoints(t, src, tc.bounds[0], tc.bounds[1]), pointsOf(t, series))
				require.Len(t, src.takeReads(), 1)
			}
			require.Equal(t, []status.LookupStatus{status.LookupStatusProxyOnly, status.LookupStatusProxyOnly},
				rc.takeStatuses())
		})
	}
}

func TestRawCacheKeepsOnlyItsWindow(t *testing.T) {
	matchers := []*labels.Matcher{labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric)}
	ctx := teamRequest(rawTestTeamA)
	reader := func(t *testing.T, rc *rawTestCache, src *chunkedSource) func(mint, maxt int64, want ...[2]int64) {
		return func(mint, maxt int64, want ...[2]int64) {
			t.Helper()
			series, _, err := rc.selectAt(t, ctx, src, mint, maxt)
			require.NoError(t, err)
			require.Equal(t, wantPoints(t, src, mint, maxt), pointsOf(t, series))
			require.Equal(t, want, src.takeReads())
		}
	}
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			src := rawTestSource(t)
			// at 10h, a 4h window keeps whole blocks from the one holding 6h, which starts there
			rc := newRawTestCache(newCache(), RawCacheOptions{Window: 4 * time.Hour})
			read := reader(t, rc, src)
			key, ok := rc.key(ctx, src, matchers)
			require.True(t, ok)

			read(2*rawTestHour, 9*rawTestHour, [2]int64{2 * rawTestHour, 9 * rawTestHour})
			for b := range blockIndex(9*rawTestHour) + 1 {
				entry, ok := rc.engine.Retrieve(blockKey(key.key, b))
				require.Equal(t, b >= 3, ok && !entry.Marker, "block %d", b)
			}
			// what's older than the window is read again by every read, and the rest comes from the cache
			read(2*rawTestHour, 9*rawTestHour, [2]int64{2 * rawTestHour, 6*rawTestHour - 1})
			read(6*rawTestHour, 9*rawTestHour)
			read(2*rawTestHour, 5*rawTestHour, [2]int64{2 * rawTestHour, 5 * rawTestHour})
			// two hours later the window has passed the block from 6h, which is read again and not looked up
			rc.setNow(rawTestEnd + 2*rawTestHour)
			read(6*rawTestHour, 9*rawTestHour, [2]int64{6 * rawTestHour, 8*rawTestHour - 1})
			require.Equal(t, []status.LookupStatus{
				status.LookupStatusKeyMiss, status.LookupStatusPartialHit, status.LookupStatusHit,
				status.LookupStatusProxyOnly, status.LookupStatusPartialHit,
			}, rc.takeStatuses())
		})
	}
	t.Run("without a window, any age since the epoch", func(t *testing.T) {
		src := rawTestSource(t)
		read := reader(t, newRawTestCache(&rawByteCache{}, RawCacheOptions{}), src)
		read(-rawTestHour, 9*rawTestHour, [2]int64{-rawTestHour, 9 * rawTestHour})
		read(-rawTestHour, 9*rawTestHour, [2]int64{-rawTestHour, -1})
	})
}

func TestRawCacheSharesConcurrentReads(t *testing.T) {
	const mint, maxt = 4 * rawTestHour, 6 * rawTestHour
	key := func(rc *rawTestCache) string {
		k, _ := rc.key(teamRequest(rawTestTeamA), rawTestSource(t), []*labels.Matcher{
			labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric)})
		return k.key + "/" + "14400000/21600000"
	}
	waiters := func(rc *rawTestCache, want int) {
		require.Eventually(t, func() bool {
			rc.flights.mu.Lock()
			f := rc.flights.flights[key(rc)]
			rc.flights.mu.Unlock()
			if f == nil {
				return false
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return len(f.callers) == want
		}, 5*time.Second, time.Millisecond)
	}
	t.Run("one read for every waiting caller", func(t *testing.T) {
		src := rawTestSource(t)
		src.gate = make(chan struct{})
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		ctxA, cancelA := context.WithCancel(teamRequest(rawTestTeamA))
		var wg sync.WaitGroup
		var errA, errB error
		var seriesB []storage.Series
		wg.Go(func() { _, _, errA = rc.selectAt(t, ctxA, src, mint, maxt) })
		wg.Go(func() { seriesB, _, errB = rc.selectAt(t, teamRequest(rawTestTeamA), src, mint, maxt) })
		waiters(rc, 2)
		// the caller that started the read leaving doesn't end it for the other
		cancelA()
		waiters(rc, 1)
		close(src.gate)
		wg.Wait()
		require.ErrorIs(t, errA, context.Canceled)
		require.NoError(t, errB)
		require.Equal(t, wantPoints(t, src, mint, maxt), pointsOf(t, seriesB))
		require.Len(t, src.takeReads(), 1)
	})
	t.Run("ends when every caller has gone", func(t *testing.T) {
		src := rawTestSource(t)
		src.gate = make(chan struct{})
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		ctx, cancel := context.WithCancel(teamRequest(rawTestTeamA))
		done := make(chan error)
		go func() {
			_, _, err := rc.selectAt(t, ctx, src, mint, maxt)
			done <- err
		}()
		waiters(rc, 1)
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		// the abandoned read ended and kept nothing, so the next caller reads again
		src.gate = nil
		_, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, mint, maxt)
		require.NoError(t, err)
		require.Len(t, src.takeReads(), 2)
	})
	t.Run("a canceled caller doesn't start a read", func(t *testing.T) {
		src := rawTestSource(t)
		rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
		ctx, cancel := context.WithCancel(teamRequest(rawTestTeamA))
		cancel()
		_, _, err := rc.selectAt(t, ctx, src, mint, maxt)
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, src.takeReads())
	})
}

// replicaSource holds two replicas of each pod scraped 5s apart, with a gap and a reset on each, as
// an HA pair's member does
func replicaSource(t *testing.T) *chunkedSource {
	t.Helper()
	src := &chunkedSource{ID: rawTestIdentity}
	for _, pod := range []string{"a", "b"} {
		src.series = append(src.series,
			chunked(t, podLabels(pod, testReplicaLabel, "1"),
				counter(0, 3*rawTestHour, [2]int64{5 * rawTestHour, 5*rawTestHour + 20*rawTestMinute})),
			chunked(t, podLabels(pod, testReplicaLabel, "2"),
				counter(5000, 7*rawTestHour, [2]int64{8 * rawTestHour, 8*rawTestHour + 15*rawTestMinute})))
	}
	return src
}

func evalRangeQuery(t *testing.T, q storage.Queryable, query string, start, end int64) promql.Matrix {
	t.Helper()
	engine := promql.NewEngine(promql.EngineOpts{MaxSamples: 10_000_000, Timeout: time.Minute,
		LookbackDelta: 5 * time.Minute})
	qry, err := engine.NewRangeQuery(teamRequest(rawTestTeamA), q, nil, query, time.UnixMilli(start),
		time.UnixMilli(end), time.Minute)
	require.NoError(t, err)
	defer qry.Close()
	res := qry.Exec(teamRequest(rawTestTeamA))
	require.NoError(t, res.Err)
	m, err := res.Matrix()
	require.NoError(t, err)
	return copyMatrix(m)
}

// copyMatrix copies a result out of the engine, which reuses a query's points once it is closed
func copyMatrix(m promql.Matrix) promql.Matrix {
	out := make(promql.Matrix, len(m))
	for i, s := range m {
		out[i] = promql.Series{Metric: s.Metric, Floats: slices.Clone(s.Floats), Histograms: slices.Clone(s.Histograms)}
		for j, h := range out[i].Histograms {
			out[i].Histograms[j].H = h.H.Copy()
		}
	}
	return out
}

func TestRawCacheAgreesWithReadingMembers(t *testing.T) {
	// the narrower selectors after the first are answered from its blocks, filtered
	queries := []string{
		"up", "rate(up[5m])", "increase(up[10m])", "resets(up[30m])", "max_over_time(up[1h])",
		"sum(rate(up[5m]))", `rate(up{pod="a"}[5m])`, `increase(up{pod="b",replica="2"}[10m])`,
		`sum(rate(up{pod=~"a|b"}[5m]))`,
	}
	// cold, the same again, sliding, reaching further back, and historical, across replica gaps and resets
	windows := []struct{ now, start, end int64 }{
		{rawTestEnd, 4 * rawTestHour, rawTestEnd},
		{rawTestEnd, 4 * rawTestHour, rawTestEnd},
		{rawTestEnd, 4*rawTestHour + 13*rawTestMinute, rawTestEnd},
		{rawTestEnd, 2*rawTestHour + 7*rawTestMinute, rawTestEnd - 20*rawTestMinute},
		{rawTestEnd, rawTestHour, 9 * rawTestHour},
	}
	for _, dedup := range []string{merge.TSMDeduplicationPenalty, merge.TSMDeduplicationChain} {
		for name, newCache := range rawTestCaches(t) {
			t.Run(dedup+"/"+name, func(t *testing.T) {
				src := replicaSource(t)
				members := []Member{{Name: rawTestMember, Source: src}}
				var subsumed atomic.Int64
				rc := newRawTestCache(newCache(), RawCacheOptions{Subsumed: func() { subsumed.Add(1) }})
				cached := NewQueryable(members, []string{testReplicaLabel}, dedup, false, nil).WithRawCache(rc.RawCache)
				direct := NewQueryable(members, []string{testReplicaLabel}, dedup, false, nil)
				for _, w := range windows {
					rc.setNow(w.now)
					for _, query := range queries {
						want := evalRangeQuery(t, direct, query, w.start, w.end)
						require.NotEmpty(t, want, query)
						require.Equal(t, want, evalRangeQuery(t, cached, query, w.start, w.end), query)
						rc.Wait()
					}
				}
				// the comparisons above read cached blocks, not only the member
				statuses := rc.takeStatuses()
				require.Contains(t, statuses, status.LookupStatusHit)
				require.Contains(t, statuses, status.LookupStatusPartialHit)
				require.Positive(t, subsumed.Load())
			})
		}
	}
}

func TestRawBlockCodec(t *testing.T) {
	src := rawTestSource(t)
	series := make([]storage.Series, len(src.series))
	for i, s := range src.series {
		series[i] = s
	}
	normalized, err := normalize(series, rawTestHour+1, 3*rawTestHour)
	require.NoError(t, err)
	hist := chunkenc.NewHistogramChunk()
	app, err := hist.Appender()
	require.NoError(t, err)
	_, _, _, err = app.AppendHistogram(nil, 0, 1, testHistogram(), true)
	require.NoError(t, err)
	normalized = append(normalized, rawSeries{lset: podLabels("c"), chunks: []rawChunk{{span: span{1, 1}, chk: hist}}})
	block := &rawBlock{series: normalized}

	codec := rawBlockCodec{}
	data, err := codec.Marshal(block)
	require.NoError(t, err)
	decoded, err := codec.Unmarshal(data)
	require.NoError(t, err)
	require.Len(t, decoded.series, len(block.series))
	for i, s := range decoded.series {
		require.Equal(t, block.series[i].lset, s.lset)
		require.Len(t, s.chunks, len(block.series[i].chunks))
		for j, c := range s.chunks {
			want := block.series[i].chunks[j]
			require.Equal(t, want.span, c.span)
			require.Equal(t, want.chk.Encoding(), c.chk.Encoding())
			require.Equal(t, want.chk.Bytes(), c.chk.Bytes())
		}
	}
	require.Equal(t, codec.Size(block), codec.Size(decoded))
	require.Zero(t, codec.Size(nil))
	_, err = codec.Marshal(nil)
	require.ErrorIs(t, err, errRawBlockCodec)

	// what AppendMarshal couldn't have written is refused
	overlapping := &rawBlock{series: []rawSeries{{lset: podLabels("a"), chunks: []rawChunk{
		{span: span{1, 10}, chk: hist}, {span: span{5, 20}, chk: hist},
	}}}}
	unsorted := &rawBlock{series: []rawSeries{{lset: podLabels("b")}, {lset: podLabels("a")}}}
	backwards := &rawBlock{series: []rawSeries{{lset: podLabels("a"), chunks: []rawChunk{{span: span{10, 1}, chk: hist}}}}}
	short := &rawBlock{series: []rawSeries{{lset: podLabels("a"), chunks: []rawChunk{
		{span: span{1, 1}, chk: shortChunk{chunkenc.NewXORChunk()}},
	}}}}
	for name, bad := range map[string][]byte{
		"empty":       nil,
		"version":     append([]byte{rawBlockCodecVersion + 1}, data[1:]...),
		"truncated":   data[:len(data)/2],
		"trailing":    append(slices.Clone(data), 0),
		"huge count":  {rawBlockCodecVersion, 0xff, 0xff, 0xff, 0xff, 0x0f},
		"overlapping": mustMarshal(t, overlapping),
		"unsorted":    mustMarshal(t, unsorted),
		"backwards":   mustMarshal(t, backwards),
		"short chunk": mustMarshal(t, short),
	} {
		_, err := codec.Unmarshal(bad)
		require.ErrorIs(t, err, errRawBlockCodec, name)
	}
}

// shortChunk is a chunk whose data can't hold its header, as a damaged entry's would
type shortChunk struct {
	chunkenc.Chunk
}

func (shortChunk) Bytes() []byte { return []byte{0} }

func mustMarshal(t *testing.T, b *rawBlock) []byte {
	t.Helper()
	data, err := rawBlockCodec{}.Marshal(b)
	require.NoError(t, err)
	return data
}

// benchSource returns a member holding 200 series of a counter sampled every 15s until rawTestEnd
func benchSource(b *testing.B) *chunkedSource {
	src := &chunkedSource{ID: rawTestIdentity}
	points := counter(0, -1, [2]int64{})
	for i := range 200 {
		src.series = append(src.series, chunked(b, podLabels(strconv.Itoa(i)), points))
	}
	return src
}

func BenchmarkRawRead(b *testing.B) {
	// reading 6h of 200 series from a warm raw cache, which reads only the volatile window from the member,
	// against reading them all from the member; then one series, alone and from the whole metric's blocks
	const mint = rawTestEnd - 6*rawTestHour
	metric := labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric)
	onePod := labels.MustNewMatcher(labels.MatchEqual, testPod, "7")
	read := func(b *testing.B, q *Queryable, matchers ...*labels.Matcher) {
		ctx := teamRequest(rawTestTeamA)
		qr, err := q.Querier(mint, rawTestEnd)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			set := qr.Select(ctx, true, &storage.SelectHints{Start: mint, End: rawTestEnd}, matchers...)
			var it chunkenc.Iterator
			for set.Next() {
				it = set.At().Iterator(it)
				for it.Next() != chunkenc.ValNone {
				}
			}
			if set.Err() != nil {
				b.Fatal(set.Err())
			}
		}
	}
	src := benchSource(b)
	members := []Member{{Name: rawTestMember, Source: src}}
	warm := func(b *testing.B) *Queryable {
		conf := cacheoptions.New()
		conf.Name, conf.Provider = rawTestALB, cacheproviders.Memory
		c := cachemanager.NewCache(cachememory.New(conf.Name, conf), cachemanager.CacheOptions{}, conf)
		if err := c.Connect(); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = c.Close() })
		rc := newRawTestCache(c, RawCacheOptions{})
		if _, _, err := rc.Select(teamRequest(rawTestTeamA), members[0], mint, rawTestEnd, nil,
			[]*labels.Matcher{metric}); err != nil {
			b.Fatal(err)
		}
		rc.Wait()
		return NewQueryable(members, nil, merge.TSMDeduplicationChain, false, nil).WithRawCache(rc.RawCache)
	}
	b.Run("member", func(b *testing.B) {
		read(b, NewQueryable(members, nil, merge.TSMDeduplicationChain, false, nil), metric)
	})
	b.Run("cached", func(b *testing.B) {
		read(b, warm(b), metric)
	})
	b.Run("member/one", func(b *testing.B) {
		read(b, NewQueryable(members, nil, merge.TSMDeduplicationChain, false, nil), metric, onePod)
	})
	b.Run("subsumed/one", func(b *testing.B) {
		read(b, warm(b), metric, onePod)
	})
}
