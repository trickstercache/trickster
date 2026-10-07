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

package resolution

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/dns/resolver"

	"github.com/stretchr/testify/require"
)

const (
	testName    = "_app._tcp.example.com"
	testMinTTL  = 5 * time.Second
	testMaxTTL  = 60 * time.Second
	testNegTTL  = 3 * time.Second
	testValue   = "v1"
	testValue2  = "v2"
	testRecTTL  = 30 * time.Second
	shortRecTTL = time.Second
)

var errServFail = errors.New("SERVFAIL")

// fakeFetch answers with whatever value, ttl and error it currently holds
type fakeFetch struct {
	mtx   sync.Mutex
	val   string
	ttl   time.Duration
	err   error
	calls atomic.Int64
	gate  chan struct{}
}

func (f *fakeFetch) set(val string, ttl time.Duration, err error) {
	f.mtx.Lock()
	f.val, f.ttl, f.err = val, ttl, err
	f.mtx.Unlock()
}

func (f *fakeFetch) fetch(context.Context, string) (string, time.Duration, error) {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.val, f.ttl, f.err
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newTestCache(f *fakeFetch) (*ttlCache[string], *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c := newTTLCache(f.fetch, testMinTTL, testMaxTTL, testNegTTL)
	c.now = clk.now
	return c, clk
}

func requireGet(t *testing.T, c *ttlCache[string], want string, wantRes lookupResult) {
	t.Helper()
	v, res, err := c.get(t.Context(), testName)
	require.NoError(t, err)
	require.Equal(t, want, v)
	require.Equal(t, lookupResultNames[wantRes], lookupResultNames[res])
}

func TestCacheHitMissAndClamp(t *testing.T) {
	f := &fakeFetch{}
	f.set(testValue, testRecTTL, nil)
	c, clk := newTestCache(f)
	requireGet(t, c, testValue, resultMiss)
	requireGet(t, c, testValue, resultHit)
	require.Equal(t, int64(1), f.calls.Load())

	clk.advance(testRecTTL)
	f.set(testValue2, shortRecTTL, nil)
	requireGet(t, c, testValue2, resultMiss)
	// a 1s record TTL is raised to the 5s floor
	clk.advance(shortRecTTL)
	requireGet(t, c, testValue2, resultHit)
	clk.advance(testMinTTL)
	f.set(testValue2, time.Hour, nil)
	requireGet(t, c, testValue2, resultMiss)
	// an hour-long record TTL is cut to the 60s ceiling
	clk.advance(testMaxTTL)
	requireGet(t, c, testValue2, resultMiss)
	require.Equal(t, int64(4), f.calls.Load())
}

func TestCacheStaleUntilMaxTTL(t *testing.T) {
	f := &fakeFetch{}
	f.set(testValue, testRecTTL, nil)
	c, clk := newTestCache(f)
	requireGet(t, c, testValue, resultMiss)

	// the TTL runs out and the resolver then fails: the last good answer keeps serving
	clk.advance(testRecTTL)
	f.set("", 0, errServFail)
	requireGet(t, c, testValue, resultStale)
	// and is not re-queried until min_ttl passes
	requireGet(t, c, testValue, resultHit)
	calls := f.calls.Load()
	clk.advance(testMinTTL)
	requireGet(t, c, testValue, resultStale)
	require.Equal(t, calls+1, f.calls.Load())

	// until max_ttl after the good lookup, when the failure surfaces
	clk.advance(testMaxTTL - testRecTTL - testMinTTL)
	_, res, err := c.get(t.Context(), testName)
	require.ErrorIs(t, err, errServFail)
	require.Equal(t, resultError, res)
	// a failure with nothing to serve is held for negative_ttl
	_, res, err = c.get(t.Context(), testName)
	require.ErrorIs(t, err, errServFail)
	require.Equal(t, resultNegative, res)

	clk.advance(testNegTTL)
	f.set(testValue2, testRecTTL, nil)
	requireGet(t, c, testValue2, resultMiss)
}

func TestCacheNegative(t *testing.T) {
	f := &fakeFetch{}
	f.set(testValue, testRecTTL, nil)
	c, clk := newTestCache(f)
	requireGet(t, c, testValue, resultMiss)

	// NXDOMAIN is an answer, not a failure: it replaces the last good answer
	clk.advance(testRecTTL)
	f.set("", 0, resolver.ErrNotFound)
	_, res, err := c.get(t.Context(), testName)
	require.ErrorIs(t, err, resolver.ErrNotFound)
	require.Equal(t, resultNegative, res)
	_, res, _ = c.get(t.Context(), testName)
	require.Equal(t, resultNegative, res)
	require.Equal(t, int64(2), f.calls.Load())

	clk.advance(testNegTTL)
	f.set(testValue2, testRecTTL, nil)
	requireGet(t, c, testValue2, resultMiss)
}

func TestCacheSharedLookup(t *testing.T) {
	f := &fakeFetch{gate: make(chan struct{})}
	f.set(testValue, testRecTTL, nil)
	c, _ := newTestCache(f)
	const callers = 8
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			v, _, err := c.get(context.Background(), testName)
			require.NoError(t, err)
			require.Equal(t, testValue, v)
		})
	}
	require.Eventually(t, func() bool { return f.calls.Load() == 1 },
		time.Second, time.Millisecond)
	close(f.gate)
	wg.Wait()
	require.Equal(t, int64(1), f.calls.Load(), "concurrent misses share one lookup")
}

func TestCacheCallerCancel(t *testing.T) {
	f := &fakeFetch{gate: make(chan struct{})}
	c, _ := newTestCache(f)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, res, err := c.get(ctx, testName)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, resultError, res)
	close(f.gate)
}

func TestCachePrimeAndEvict(t *testing.T) {
	f := &fakeFetch{}
	f.set(testValue2, testRecTTL, nil)
	c, clk := newTestCache(f)
	c.prime(testName, testValue, testRecTTL)
	requireGet(t, c, testValue, resultHit)
	// a fresh entry is not replaced by a prime
	c.prime(testName, testValue2, testRecTTL)
	requireGet(t, c, testValue, resultHit)
	require.Zero(t, f.calls.Load())

	for i := range maxCacheEntries {
		c.prime(strconv.Itoa(i), testValue, testRecTTL)
	}
	require.Len(t, c.entries, maxCacheEntries, "a full cache evicts one live entry per insert")

	// once entries can no longer serve, a full cache sweeps them all
	clk.advance(testMaxTTL)
	c.prime(testName+"-new", testValue, testRecTTL)
	require.Len(t, c.entries, 1)
}
