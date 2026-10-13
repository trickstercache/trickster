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
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	retireTestOrigin   = "http://thanos-query:10904"
	retireTestEndpoint = po.StoreAPIScheme + "://thanos-sidecar:10901"
	retireTestCallers  = 8
	// retireTestWait bounds what a test waits on that, broken, would never happen
	retireTestWait = 5 * time.Second
	// retireTestPause is how long a test watches for something that must not happen yet
	retireTestPause = 50 * time.Millisecond
)

// countStoreAPIBuilds counts the StoreAPI sources built until the test ends, calling hold, when set, before each
func countStoreAPIBuilds(t *testing.T, hold func()) *atomic.Int32 {
	t.Helper()
	var builds atomic.Int32
	prev := newStoreAPI
	newStoreAPI = func(endpoint *url.URL, tlsConfig *tls.Config, paths pathopts.List) (*global.StoreAPI, error) {
		builds.Add(1)
		if hold != nil {
			hold()
		}
		return prev(endpoint, tlsConfig, paths)
	}
	t.Cleanup(func() { newStoreAPI = prev })
	return &builds
}

func newStoreAPIClient(t *testing.T) *Client {
	t.Helper()
	c := newGlobalSourceClient(t, retireTestOrigin, &po.Options{Flavor: po.FlavorThanos, SeriesEndpoint: retireTestEndpoint})
	t.Cleanup(func() { c.Retire(context.Background()) })
	return c
}

// requireClosed requires source's connection to be closed already, as closing it again reports
func requireClosed(t *testing.T, source backends.TSMGlobalSource) {
	t.Helper()
	require.Error(t, source.(io.Closer).Close(), "the source was not closed")
	_, _, err := source.(global.Source).Select(context.Background(), 0, 1, nil,
		labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, presenceMetric))
	require.Error(t, err, "a closed source reads nothing")
}

func TestTSMGlobalSourceBuildsOnceForConcurrentCallers(t *testing.T) {
	building, release := make(chan struct{}, 1), make(chan struct{})
	builds := countStoreAPIBuilds(t, func() {
		building <- struct{}{}
		<-release
	})
	c := newStoreAPIClient(t)
	sources := make([]backends.TSMGlobalSource, retireTestCallers)
	errs := make([]error, retireTestCallers)
	var wg sync.WaitGroup
	for i := range sources {
		wg.Go(func() { sources[i], errs[i] = c.TSMGlobalSource() })
	}
	<-building
	// the callers arriving while the first builds wait for its source rather than building their own
	require.Never(t, func() bool { return builds.Load() > 1 }, retireTestPause, time.Millisecond)
	close(release)
	wg.Wait()
	require.Equal(t, int32(1), builds.Load())
	for i, s := range sources {
		require.NoError(t, errs[i])
		require.Same(t, sources[0], s)
	}
}

func TestClientRetireClosesItsSource(t *testing.T) {
	t.Run("storeapi", func(t *testing.T) {
		c := newStoreAPIClient(t)
		source, err := c.TSMGlobalSource()
		require.NoError(t, err)
		c.Retire(context.Background())
		requireClosed(t, source)
		_, err = c.TSMGlobalSource()
		require.ErrorIs(t, err, errSourceRetired, "a retired client builds no source")
		// retiring again closes nothing twice
		c.Retire(context.Background())
	})
	t.Run("before its source is built", func(t *testing.T) {
		builds := countStoreAPIBuilds(t, nil)
		c := newStoreAPIClient(t)
		c.Retire(context.Background())
		_, err := c.TSMGlobalSource()
		require.ErrorIs(t, err, errSourceRetired)
		require.Zero(t, builds.Load())
	})
	t.Run("remote read", func(t *testing.T) {
		c := newGlobalSourceClient(t, retireTestOrigin, &po.Options{Flavor: po.FlavorMimir})
		_, err := c.TSMGlobalSource()
		require.NoError(t, err)
		backends.Backends{"member": c}.Retire(0)
		_, err = c.TSMGlobalSource()
		require.ErrorIs(t, err, errSourceRetired)
	})
}

func TestGlobalEvaluatorRetireRefusesEvaluations(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	source := &recordingSource{store: members[0].(presenceStore)}
	req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: "m0", Source: source}}}
	e := newGlobalEvaluator(globalTestSettings())
	e.Retire(context.Background())
	w := serveGlobal(e, globalTestRequest(globalTestQueryPath,
		url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}), req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), global.ErrorTypeUnavailable)
	require.Contains(t, w.Body.String(), errGlobalRetired.Error())
	require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl))
	require.Zero(t, source.reads(), "no member is read once the evaluator retires")
}

func TestGlobalEvaluatorRetireCancelsEvaluations(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	source := blockingSource{
		store: members[0].(presenceStore),
		held:  make(chan struct{}, 1), release: make(chan struct{}),
	}
	t.Cleanup(sync.OnceFunc(func() { close(source.release) }))
	req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: "slow", Source: source}}}
	settings := globalTestSettings()
	settings.MaxConcurrentQueries = 1
	e := newGlobalEvaluator(settings)
	instant := url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}
	running := make(chan *httptest.ResponseRecorder, 1)
	waiting := make(chan *httptest.ResponseRecorder, 1)
	go func() { running <- serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), req) }()
	<-source.held
	// another evaluation waits for the only slot, or, should it not have started by then, finds the evaluator retired
	go func() { waiting <- serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), req) }()
	time.Sleep(retireTestPause)
	retired := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		e.Retire(ctx)
		close(retired)
	}()
	// both end as soon as the evaluator retires, though their member and the query timeout would hold them
	for _, answered := range []chan *httptest.ResponseRecorder{running, waiting} {
		select {
		case w := <-answered:
			require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), errGlobalRetired.Error())
		case <-time.After(retireTestWait):
			t.Fatal("an evaluation outlived the evaluator's retirement")
		}
	}
	select {
	case <-retired:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement never ended")
	}
}

// heldRawCache is a memory cache whose raw series block stores signal as they start and wait until unblocked
type heldRawCache struct {
	memoryCache
	stores  atomic.Int32
	storing chan struct{}
	release chan struct{}
	unblock func()
}

type memoryCache interface {
	cache.Cache
	cache.MemoryCache
}

func newHeldRawCache(t *testing.T) *heldRawCache {
	t.Helper()
	mc, ok := globalTestCache(t).(memoryCache)
	require.True(t, ok)
	c := &heldRawCache{memoryCache: mc, storing: make(chan struct{}, 1), release: make(chan struct{})}
	c.unblock = sync.OnceFunc(func() { close(c.release) })
	t.Cleanup(c.unblock)
	return c
}

func (c *heldRawCache) hold(key string) {
	if !strings.Contains(key, globalOriginHost+".raw.") {
		return
	}
	c.stores.Add(1)
	select {
	case c.storing <- struct{}{}:
	default:
	}
	<-c.release
}

func (c *heldRawCache) Store(key string, data []byte, ttl time.Duration) error {
	c.hold(key)
	return c.memoryCache.Store(key, data, ttl)
}

func (c *heldRawCache) StoreReference(key string, data cache.ReferenceObject, ttl time.Duration) error {
	c.hold(key)
	return c.memoryCache.StoreReference(key, data, ttl)
}

func TestGlobalEvaluatorRetireWaitsForRawStores(t *testing.T) {
	settings := globalTestSettings()
	held := newHeldRawCache(t)
	gt := newGlobalCacheTest(t, settings, held)
	gt.source.ID = globalRawIdentity
	w := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 5, 45), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	reads := gt.source.reads()
	require.Positive(t, reads)
	// the evaluation answered while what its read keeps waits to be stored
	select {
	case <-held.storing:
	case <-time.After(retireTestWait):
		t.Fatal("nothing the read kept was stored")
	}
	// a retirement waits no longer than its context for the store
	short, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	retireWithin(t, gt.evaluator, short)
	retired := make(chan struct{})
	go func() {
		gt.evaluator.Retire(context.Background())
		close(retired)
	}()
	select {
	case <-retired:
		t.Fatal("the retirement ended while a store was under way")
	case <-time.After(retireTestPause):
	}
	held.unblock()
	select {
	case <-retired:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement never ended")
	}
	stores := held.stores.Load()
	// a result kept before the evaluator retired is still answered from the cache, but no evaluation, read or store starts
	w = gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 5, 45), nil)
	requireCacheStatus(t, w, status.StatusHit)
	w = gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 50, 55), nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), errGlobalRetired.Error())
	require.Equal(t, reads, gt.source.reads())
	require.Equal(t, stores, held.stores.Load())
}

// retireWithin retires e, failing rather than hanging when the retirement outlasts ctx
func retireWithin(t *testing.T, e *globalEvaluator, ctx context.Context) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		e.Retire(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement outlasted its context")
	}
}

func TestClientRetireWaitsForTheSourceBeingBuilt(t *testing.T) {
	building, release := make(chan struct{}, 1), make(chan struct{})
	builds := countStoreAPIBuilds(t, func() {
		building <- struct{}{}
		<-release
	})
	c := newStoreAPIClient(t)
	type built struct {
		source backends.TSMGlobalSource
		err    error
	}
	first := make(chan built, 1)
	go func() {
		source, err := c.TSMGlobalSource()
		first <- built{source, err}
	}()
	<-building
	retired := make(chan struct{})
	go func() {
		c.Retire(context.Background())
		close(retired)
	}()
	// a retirement under way while the source is built closes that source, which would otherwise outlive it
	select {
	case <-retired:
		t.Fatal("the retirement ended while the source was being built")
	case <-time.After(retireTestPause):
	}
	close(release)
	var b built
	select {
	case b = <-first:
	case <-time.After(retireTestWait):
		t.Fatal("the build never ended")
	}
	require.NoError(t, b.err)
	select {
	case <-retired:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement never ended")
	}
	requireClosed(t, b.source)
	_, err := c.TSMGlobalSource()
	require.ErrorIs(t, err, errSourceRetired)
	require.Equal(t, int32(1), builds.Load())
}

func TestTSMGlobalSourceFindingTheClientRetiredBuildsNone(t *testing.T) {
	builds := countStoreAPIBuilds(t, nil)
	c := newStoreAPIClient(t)
	// a caller finding no source waits on the lock a retirement holds, and finds the client retired once it has it
	c.globalSource.mu.Lock()
	got := make(chan error, 1)
	go func() {
		_, err := c.TSMGlobalSource()
		got <- err
	}()
	time.Sleep(retireTestPause)
	c.globalSource.result.Store(&globalSourceResult{err: errSourceRetired})
	c.globalSource.mu.Unlock()
	select {
	case err := <-got:
		require.ErrorIs(t, err, errSourceRetired)
	case <-time.After(retireTestWait):
		t.Fatal("the caller never returned")
	}
	require.Zero(t, builds.Load(), "a retired client built a source")
}

// stubbornSource is a member whose reads wait until released, whatever ends their context
type stubbornSource struct {
	memberSource
	held, release chan struct{}
}

func (s stubbornSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.held <- struct{}{}
	<-s.release
	return s.memberSource.Select(ctx, mint, maxt, hints, matchers...)
}

func TestGlobalEvaluatorRetireWakesSlotWaiters(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	source := stubbornSource{
		store: members[0].(presenceStore),
		held:  make(chan struct{}, 1), release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(source.release) })
	t.Cleanup(release)
	req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: "stubborn", Source: source}}}
	settings := globalTestSettings()
	settings.MaxConcurrentQueries = 1
	e := newGlobalEvaluator(settings)
	instant := url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}
	running := make(chan *httptest.ResponseRecorder, 1)
	waiting := make(chan *httptest.ResponseRecorder, 1)
	go func() { running <- serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), req) }()
	<-source.held
	go func() { waiting <- serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), req) }()
	time.Sleep(retireTestPause)
	retired := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		e.Retire(ctx)
		close(retired)
	}()
	// the slot's holder ignores the retirement, so only the retirement itself can end the wait for the slot
	select {
	case w := <-waiting:
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), errGlobalRetired.Error())
	case <-time.After(retireTestWait):
		t.Fatal("an evaluation waiting for a slot outlived the evaluator's retirement")
	}
	select {
	case <-running:
		t.Fatal("the slot's holder ended while its member held it")
	default:
	}
	release()
	select {
	case w := <-running:
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), errGlobalRetired.Error())
	case <-time.After(retireTestWait):
		t.Fatal("the slot's holder never ended")
	}
	select {
	case <-retired:
	case <-time.After(retireTestWait):
		t.Fatal("the retirement never ended")
	}
}

func TestGlobalEvaluatorRetirementEndsAPartialEvaluation(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	var req backends.TSMGlobalRequest
	var sources []blockingSource
	for i, store := range members {
		// sources with an identity are read through the raw cache's shared reads, which closing it cancels
		s := blockingSource{
			ID:    "m" + strconv.Itoa(i),
			store: store.(presenceStore),
			held:  make(chan struct{}, 1), release: make(chan struct{}),
		}
		t.Cleanup(sync.OnceFunc(func() { close(s.release) }))
		sources = append(sources, s)
		req.Members = append(req.Members, backends.TSMGlobalMember{Name: s.ID, Source: s})
	}
	settings := globalTestSettings()
	require.True(t, settings.PartialResponse)
	e := newGlobalEvaluator(settings)
	keepAll := time.Duration(0)
	e.raw = global.NewRawCache(globalTestCache(t), global.RawCacheOptions{Name: "retiring", VolatileWindow: &keepAll})
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- serveGlobal(e, globalTestRequest(globalTestQueryPath,
			url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}), req)
	}()
	for _, s := range sources {
		<-s.held
	}
	// the retirement is under way, but the evaluation sees the raw cache close before its own cancellation, which a
	// retirement starts on a goroutine of its own
	retired, cancel := context.WithCancel(context.Background())
	cancel()
	e.retired = retired
	require.True(t, e.raw.Close(context.Background()))
	select {
	case w := <-answered:
		// every member dropped by the retirement is no partial answer, but the retirement's
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), errGlobalRetired.Error())
		require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl))
	case <-time.After(retireTestWait):
		t.Fatal("the evaluation never ended")
	}
}
