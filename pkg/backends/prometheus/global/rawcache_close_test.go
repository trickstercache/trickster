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
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

// closeTestPause is how long a test watches for something that must not happen yet
const closeTestPause = 50 * time.Millisecond

// heldByteCache stores as rawByteCache does, signaling each store as it starts and holding it until unblocked
type heldByteCache struct {
	rawByteCache
	storing chan struct{}
	release chan struct{}
	unblock func()
}

func newHeldByteCache(t *testing.T) *heldByteCache {
	t.Helper()
	c := &heldByteCache{storing: make(chan struct{}, 1), release: make(chan struct{})}
	c.unblock = sync.OnceFunc(func() { close(c.release) })
	t.Cleanup(c.unblock)
	return c
}

func (c *heldByteCache) Store(key string, data []byte, ttl time.Duration) error {
	c.storing <- struct{}{}
	<-c.release
	return c.rawByteCache.Store(key, data, ttl)
}

// stubbornSource holds each read until release closes, whatever the read's context, then answers as its source does
type stubbornSource struct {
	*chunkedSource
	held    chan struct{}
	release chan struct{}
}

func (s *stubbornSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	s.held <- struct{}{}
	<-s.release
	return s.chunkedSource.Select(context.WithoutCancel(ctx), mint, maxt, hints, matchers...)
}

func (s *chunkedSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reads)
}

// closeWithin closes rc, failing rather than hanging when the close never returns
func closeWithin(t *testing.T, rc *RawCache, ctx context.Context) bool {
	t.Helper()
	var finished bool
	waitWithin(t, func() { finished = rc.Close(ctx) })
	return finished
}

func TestRawCacheCloseRefusesReads(t *testing.T) {
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			src := rawTestSource(t)
			rc := newRawTestCache(newCache(), RawCacheOptions{})
			ctx := teamRequest(rawTestTeamA)
			require.True(t, closeWithin(t, rc.RawCache, context.Background()), "nothing was under way")
			_, _, err := rc.selectAt(t, ctx, src, 0, rawTestHour)
			require.ErrorIs(t, err, errRawCacheClosed)
			// a source the cache can't key is read past its blocks, and is refused just the same
			anonymous := rawTestSource(t)
			anonymous.ID = ""
			_, _, err = rc.selectAt(t, ctx, anonymous, 0, rawTestHour)
			require.ErrorIs(t, err, errRawCacheClosed)
			require.Empty(t, src.takeReads())
			require.Empty(t, anonymous.takeReads())
			require.Empty(t, rc.takeStatuses(), "a refused select reads neither members nor blocks")
			// the flights a select would share refuse a caller too
			_, err = rc.flights.do(ctx, rawTestMember, &selectCharge{}, func(context.Context, shareFunc) fetched {
				t.Fatal("a closed cache's flight ran")
				return fetched{}
			}, nil)
			require.ErrorIs(t, err, errRawCacheClosed)
		})
	}
}

func TestRawCacheCloseCancelsReads(t *testing.T) {
	src := rawTestSource(t)
	// the gate never opens, so the read ends only with its context
	src.gate = make(chan struct{})
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	ctx := teamRequest(rawTestTeamA)
	matchers := []*labels.Matcher{testMetricMatcher}
	k, ok := rc.key(ctx, src, matchers)
	require.True(t, ok)
	var err error
	selected := make(chan struct{})
	go func() {
		defer close(selected)
		_, _, err = rc.Select(ctx, Member{Name: rawTestMember, Source: src}, 0, rawTestHour, nil, matchers)
	}()
	require.Eventually(t, func() bool { return src.readCount() == 1 }, storeTestWait, time.Millisecond)
	closeCtx, cancel := context.WithTimeout(context.Background(), storeTestWait)
	defer cancel()
	// the read is canceled though its caller still waits, and nothing it held was stored, so the close finishes at once
	require.True(t, closeWithin(t, rc.RawCache, closeCtx))
	waitWithin(t, func() { <-selected })
	require.ErrorIs(t, err, context.Canceled)
	_, ok = rc.engine.Retrieve(blockKey(k.key, 0))
	require.False(t, ok)
	require.False(t, indexed(rc.RawCache, k))
}

func TestRawCacheCloseWaitsForStores(t *testing.T) {
	src := rawTestSource(t)
	held := newHeldByteCache(t)
	rc := newRawTestCache(held, RawCacheOptions{})
	ctx := teamRequest(rawTestTeamA)
	matchers := []*labels.Matcher{testMetricMatcher}
	k, ok := rc.key(ctx, src, matchers)
	require.True(t, ok)
	want := wantPoints(t, src, 0, rawTestHour)
	var series []storage.Series
	var err error
	// the caller is answered while what the read keeps waits to be stored
	waitWithin(t, func() {
		series, _, err = rc.Select(ctx, Member{Name: rawTestMember, Source: src}, 0, rawTestHour, nil, matchers)
	})
	require.NoError(t, err)
	require.Equal(t, want, pointsOf(t, series))
	waitWithin(t, func() { <-held.storing })
	// a close waits no longer than its context for the store under way
	short, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	require.False(t, closeWithin(t, rc.RawCache, short))
	closed := make(chan bool, 1)
	go func() { closed <- rc.Close(context.Background()) }()
	select {
	case <-closed:
		t.Fatal("the close returned while a store was under way")
	case <-time.After(closeTestPause):
	}
	held.unblock()
	select {
	case finished := <-closed:
		require.True(t, finished)
	case <-time.After(storeTestWait):
		t.Fatal("the close never returned")
	}
	// the store under way as the cache closed finished, so what it kept is whole
	_, ok = rc.engine.Retrieve(blockKey(k.key, 0))
	require.True(t, ok)
	require.True(t, indexed(rc.RawCache, k))
}

func TestRawCacheStoresNothingReadOnceClosed(t *testing.T) {
	src := &stubbornSource{chunkedSource: rawTestSource(t), held: make(chan struct{}, 1), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(src.release) })
	t.Cleanup(release)
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
	ctx := teamRequest(rawTestTeamA)
	matchers := []*labels.Matcher{testMetricMatcher}
	k, ok := rc.key(ctx, src, matchers)
	require.True(t, ok)
	selected := make(chan error, 1)
	go func() {
		_, _, err := rc.Select(ctx, Member{Name: rawTestMember, Source: src}, 0, rawTestHour, nil, matchers)
		selected <- err
	}()
	waitWithin(t, func() { <-src.held })
	// a read that ignores its cancellation is still under way when the close gives up waiting for it
	short, cancel := context.WithTimeout(context.Background(), closeTestPause)
	defer cancel()
	require.False(t, closeWithin(t, rc.RawCache, short))
	release()
	waitWithin(t, func() { <-selected })
	waitWithin(t, rc.Wait)
	// what it then returned is never stored, as the closed cache's store may have been released
	_, ok = rc.engine.Retrieve(blockKey(k.key, 0))
	require.False(t, ok)
	require.False(t, indexed(rc.RawCache, k))
}

func TestStoreAPICloseReleasesItsConnection(t *testing.T) {
	podA := []string{testPod, "a", labels.MetricName, testMetric}
	store := &testStore{frames: [][]byte{
		encodeTestSeries(podA, spanned(encodeTestChunk(storeChunkXOR, xorData(t, testPoint{t: 1, f: 1}), true), 1, 1)),
	}}
	source := store.serve(t)
	// a source makes no connection until it is first read
	idle := runtime.NumGoroutine()
	series, _, err := source.Select(testContext(t, &Budget{}), 0, 20, nil, testMetricMatcher)
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Greater(t, runtime.NumGoroutine(), idle, "the read connected")
	require.NoError(t, source.Close())
	// both ends of the closed connection release their goroutines
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= idle }, storeTestWait, time.Millisecond)
	_, _, err = source.Select(testContext(t, &Budget{}), 0, 20, nil, testMetricMatcher)
	require.ErrorIs(t, err, errStoreAPI)
	require.Equal(t, 1, store.requestCount(), "a closed source reads nothing")
}
