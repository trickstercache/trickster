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
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines/nativedelta"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
)

const (
	sizeTestBlockStart = 4 * rawTestHour
	// sizeTestNarrowEnd ends a read keeping the test block's first half hour
	sizeTestNarrowEnd = sizeTestBlockStart + 30*rawTestMinute
	sizeTestBlockEnd  = 6*rawTestHour - 1
	// sizeTestLarge is a limit no test block reaches
	sizeTestLarge     = 1 << 20
	sizeTestRefused   = "tsm_global_raw_max_object_size"
	sizeTestOversized = "tsm_global_raw_oversized_cached_object"
)

var sizeTestMetric = labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, testMetric)

// sizeTestKey returns rc's key of src's test metric series for team A, also matching more
func sizeTestKey(t *testing.T, rc *rawTestCache, src Source, more ...*labels.Matcher) rawKey {
	t.Helper()
	k, ok := rc.key(teamRequest(rawTestTeamA), src, append([]*labels.Matcher{sizeTestMetric}, more...))
	require.True(t, ok)
	return k
}

// sizeTestBlock returns rc's key of the block holding at, of src's test metric series for team A
func sizeTestBlock(t *testing.T, rc *rawTestCache, src Source, at int64) string {
	t.Helper()
	return blockKey(sizeTestKey(t, rc, src).key, blockIndex(at))
}

// cachedSize returns the size the cache accounts for the block at bk, which it must hold
func cachedSize(t *testing.T, rc *rawTestCache, bk string) int64 {
	t.Helper()
	entry, ok := rc.engine.Retrieve(bk)
	require.True(t, ok, "block %s is cached", bk)
	return int64(entry.Size())
}

// sizeTestRead answers a select of src exactly, as an uncached read does, and returns the member reads it took
func sizeTestRead(t *testing.T, rc *rawTestCache, src *chunkedSource, mint, maxt int64) [][2]int64 {
	t.Helper()
	series, _, err := rc.selectAt(t, teamRequest(rawTestTeamA), src, mint, maxt)
	require.NoError(t, err)
	require.Equal(t, wantPoints(t, src, mint, maxt), pointsOf(t, series))
	return src.takeReads()
}

// sizeTestSizes returns the sizes the test block takes in a cache of newCache once a narrow read keeps part of
// it, and once it's filled
func sizeTestSizes(t *testing.T, newCache func() cache.Cache) (narrow, filled int64) {
	t.Helper()
	src := rawTestSource(t)
	rc := newRawTestCache(newCache(), RawCacheOptions{})
	bk := sizeTestBlock(t, rc, src, sizeTestBlockStart)
	sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestNarrowEnd)
	narrow = cachedSize(t, rc, bk)
	sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestBlockEnd)
	filled = cachedSize(t, rc, bk)
	require.Less(t, narrow, filled)
	return narrow, filled
}

// failureLog collects the cache failures a raw cache observes
type failureLog struct {
	mu      sync.Mutex
	reasons []string
}

func (f *failureLog) observe(reason string) {
	f.mu.Lock()
	f.reasons = append(f.reasons, reason)
	f.mu.Unlock()
}

func (f *failureLog) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.reasons
	f.reasons = nil
	return out
}

// countingCache counts each key's retrievals from the cache it wraps
type countingCache struct {
	cache.Cache
	mu        sync.Mutex
	retrieved map[string]int
}

func (c *countingCache) count(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retrieved == nil {
		c.retrieved = make(map[string]int)
	}
	c.retrieved[key]++
}

// take returns how many times key was retrieved since last asked
func (c *countingCache) take(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.retrieved[key]
	delete(c.retrieved, key)
	return n
}

func (c *countingCache) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	c.count(key)
	return c.Cache.Retrieve(key)
}

// countingMemoryCache counts the retrievals of a memory cache, which hands out references
type countingMemoryCache struct {
	*countingCache
	memory cache.MemoryCache
}

func (c *countingMemoryCache) RetrieveReference(key string) (any, status.LookupStatus, error) {
	c.count(key)
	return c.memory.RetrieveReference(key)
}

func (c *countingMemoryCache) StoreReference(key string, data cache.ReferenceObject, ttl time.Duration) error {
	return c.memory.StoreReference(key, data, ttl)
}

// counting returns c, counting its retrievals by key
func counting(c cache.Cache) (cache.Cache, *countingCache) {
	cc := &countingCache{Cache: c}
	if memory, ok := c.(cache.MemoryCache); ok {
		return &countingMemoryCache{countingCache: cc, memory: memory}, cc
	}
	return cc, cc
}

func TestRawCacheObjectSizeLimit(t *testing.T) {
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			narrow, filled := sizeTestSizes(t, newCache)
			limit := (narrow + filled) / 2

			t.Run("a block past the limit isn't kept", func(t *testing.T) {
				src := rawTestSource(t)
				var failures failureLog
				rc := newRawTestCache(newCache(), RawCacheOptions{MaxObjectSize: limit, ObserveCacheFailure: failures.observe})
				bk := sizeTestBlock(t, rc, src, sizeTestBlockStart)
				require.Equal(t, [][2]int64{{sizeTestBlockStart, sizeTestNarrowEnd}},
					sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestNarrowEnd))
				require.Equal(t, narrow, cachedSize(t, rc, bk), "a block within the limit is kept")
				// filling the block would take it past the limit, so the rest is read each time and the block keeps what it held
				for range 2 {
					require.Equal(t, [][2]int64{{sizeTestNarrowEnd + 1, sizeTestBlockEnd}},
						sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestBlockEnd))
				}
				entry, ok := rc.engine.Retrieve(bk)
				require.True(t, ok)
				require.Equal(t, timeseries.ExtentList{extent(sizeTestBlockStart, sizeTestNarrowEnd)}, entry.Extents)
				require.Equal(t, []status.LookupStatus{
					status.LookupStatusKeyMiss, status.LookupStatusPartialHit, status.LookupStatusPartialHit,
				}, rc.takeStatuses())
				require.Equal(t, []string{sizeTestRefused}, failures.take(), "a refusal is observed, and not tried again")
			})
			t.Run("a cached block past the limit is dropped", func(t *testing.T) {
				// a block under the cache's own key that is past its limit, as one a cache measuring otherwise kept, is refused
				src := rawTestSource(t)
				shared := newCache()
				unlimited := newRawTestCache(shared, RawCacheOptions{})
				sizeTestRead(t, unlimited, src, sizeTestBlockStart, sizeTestBlockEnd)
				whole, ok := unlimited.engine.Retrieve(sizeTestBlock(t, unlimited, src, sizeTestBlockStart))
				require.True(t, ok)
				var failures failureLog
				rc := newRawTestCache(shared, RawCacheOptions{MaxObjectSize: limit, ObserveCacheFailure: failures.observe})
				bk := sizeTestBlock(t, rc, src, sizeTestBlockStart)
				unlimited.engine.Store(bk, whole)
				for range 2 {
					require.Equal(t, [][2]int64{{sizeTestBlockStart, sizeTestBlockEnd}},
						sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestBlockEnd))
				}
				_, ok = unlimited.engine.Retrieve(bk)
				require.False(t, ok, "the oversized block is removed from the cache")
				require.Equal(t, []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusKeyMiss},
					rc.takeStatuses())
				require.Equal(t, []string{sizeTestOversized, sizeTestRefused}, failures.take())
				require.Equal(t, filled, cachedSize(t, unlimited, sizeTestBlock(t, unlimited, src, sizeTestBlockStart)),
					"the unlimited cache's own block is kept")
			})
		})
	}
}

func TestRawCacheRemembersRefusedBlocks(t *testing.T) {
	const within = sizeTestNarrowEnd + 10*rawTestMinute
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			narrow, filled := sizeTestSizes(t, newCache)
			src := rawTestSource(t)
			c, counts := counting(newCache())
			var failures failureLog
			rc := newRawTestCache(c, RawCacheOptions{
				MaxObjectSize: (narrow + filled) / 2, ObserveCacheFailure: failures.observe,
			})
			bk := sizeTestBlock(t, rc, src, sizeTestBlockStart)
			// wide reads the whole block, of which the cache lacks from after from, and returns its retrievals of the block
			wide := func(from int64) int {
				t.Helper()
				require.Equal(t, [][2]int64{{from + 1, sizeTestBlockEnd}},
					sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestBlockEnd))
				return counts.take(bk)
			}
			sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestNarrowEnd)
			counts.take(bk)

			// the first read past the limit retrieves the block to answer and to merge into it, and is refused
			require.Equal(t, 2, wide(sizeTestNarrowEnd))
			require.Equal(t, []string{sizeTestRefused}, failures.take())
			// later reads of those samples retrieve it only to answer, and aren't refused again
			for range 2 {
				require.Equal(t, 1, wide(sizeTestNarrowEnd))
				require.Empty(t, failures.take())
			}
			// fewer samples may fit, so they're tried and kept
			require.Equal(t, [][2]int64{{sizeTestNarrowEnd + 1, within}},
				sizeTestRead(t, rc, src, sizeTestBlockStart, within))
			require.Equal(t, 2, counts.take(bk))
			entry, ok := rc.engine.Retrieve(bk)
			require.True(t, ok)
			require.Equal(t, timeseries.ExtentList{extent(sizeTestBlockStart, within)}, entry.Extents)
			counts.take(bk)
			require.Empty(t, failures.take())
			// the rest of the block is then refused once more, and remembered
			require.Equal(t, 2, wide(within))
			require.Equal(t, []string{sizeTestRefused}, failures.take())
			require.Equal(t, 1, wide(within))
			// once the TTL passes, the block is tried again
			rc.setNow(rawTestEnd + time.Hour.Milliseconds() + 1)
			require.Equal(t, 2, wide(within))
			require.Equal(t, []string{sizeTestRefused}, failures.take())
		})
	}
}

func TestRawCacheRefusalsAreBounded(t *testing.T) {
	rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{MaxObjectSize: 1})
	const stripes = 2
	keys := make(map[int][]string, stripes)
	for i := 0; len(keys[0]) <= rawStripeRefusals || len(keys[1]) <= rawStripeRefusals; i++ {
		bk := blockKey(rawKeyPrefix+"bounded", int64(i))
		if s := stripe(bk); s < stripes {
			keys[s] = append(keys[s], bk)
		}
	}
	refuse := func(s int, bk string) {
		rc.locks[s].Lock()
		rc.refuse(s, bk, 0, 1, nil)
		rc.locks[s].Unlock()
	}
	// a full stripe forgets an expired refusal first
	for _, bk := range keys[0][:rawStripeRefusals] {
		refuse(0, bk)
	}
	rc.setNow(rawTestEnd + time.Hour.Milliseconds() + 1)
	refuse(0, keys[0][0])
	refuse(0, keys[0][rawStripeRefusals])
	require.Len(t, rc.refused[0], 2, "every expired refusal is forgotten")
	// and otherwise any one, so it never grows past its bound
	for _, bk := range keys[1] {
		refuse(1, bk)
		require.LessOrEqual(t, len(rc.refused[1]), rawStripeRefusals)
	}
	require.Len(t, rc.refused[1], rawStripeRefusals)
	require.Contains(t, rc.refused[1], keys[1][len(keys[1])-1], "the latest refusal is remembered")
}

func TestRawCacheIndexesOnlyKeptBlocks(t *testing.T) {
	ctx := teamRequest(rawTestTeamA)
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			t.Run("no block kept", func(t *testing.T) {
				src := rawTestSource(t)
				c, counts := counting(newCache())
				rc := newRawTestCache(c, RawCacheOptions{MaxObjectSize: 16})
				broad, narrower := sizeTestKey(t, rc, src), sizeTestKey(t, rc, src, podA)
				bk := blockKey(broad.key, blockIndex(sizeTestBlockStart))
				sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestBlockEnd)
				_, ok := rc.engine.Retrieve(bk)
				require.False(t, ok)
				require.False(t, indexed(rc.RawCache, broad), "a selector none of whose blocks is kept isn't indexed")
				require.Empty(t, rc.index.candidates(narrower, rc.RawCache.now()))
				counts.take(bk)
				for range 3 {
					series := rc.selectMatching(t, ctx, src, sizeTestBlockStart, sizeTestBlockEnd, sizeTestMetric, podA)
					require.Equal(t, wantMatching(t, src, sizeTestBlockStart, sizeTestBlockEnd, sizeTestMetric, podA),
						pointsOf(t, series))
				}
				require.Zero(t, counts.take(bk), "a narrower selector never looks for the broader one's blocks")
			})
			narrow, filled := sizeTestSizes(t, newCache)
			// a read's half hour of one block fits the limit, while the whole test block doesn't, whichever comes first
			for order, r := range map[string]struct{ mint, maxt, fits int64 }{
				"kept then refused": {mint: sizeTestBlockStart - 30*rawTestMinute, maxt: sizeTestBlockEnd, fits: sizeTestBlockStart - 1},
				"refused then kept": {mint: sizeTestBlockStart, maxt: sizeTestBlockEnd + 30*rawTestMinute, fits: sizeTestBlockEnd + 1},
			} {
				t.Run(order, func(t *testing.T) {
					src := rawTestSource(t)
					rc := newRawTestCache(newCache(), RawCacheOptions{MaxObjectSize: (narrow + filled) / 2})
					k := sizeTestKey(t, rc, src)
					sizeTestRead(t, rc, src, r.mint, r.maxt)
					require.True(t, indexed(rc.RawCache, k), "a selector with a block kept is indexed")
					_, ok := rc.engine.Retrieve(blockKey(k.key, blockIndex(r.fits)))
					require.True(t, ok, "the block within the limit is kept")
					_, ok = rc.engine.Retrieve(blockKey(k.key, blockIndex(sizeTestBlockStart)))
					require.False(t, ok, "the block past the limit isn't")
				})
			}
		})
	}
}

func TestRawCacheKeysBlocksByLimit(t *testing.T) {
	whole := [][2]int64{{sizeTestBlockStart, sizeTestBlockEnd}}
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			_, filled := sizeTestSizes(t, newCache)
			shared := newCache()
			// sources of one identity, so caches of one limit share their blocks, with reads counted apart
			largeSrc, smallSrc := rawTestSource(t), rawTestSource(t)
			large := newRawTestCache(shared, RawCacheOptions{MaxObjectSize: sizeTestLarge})
			small := newRawTestCache(shared, RawCacheOptions{MaxObjectSize: filled / 2})
			unlimited := newRawTestCache(shared, RawCacheOptions{})
			require.NotEqual(t, sizeTestKey(t, large, largeSrc).key, sizeTestKey(t, small, smallSrc).key)
			require.NotEqual(t, sizeTestKey(t, large, largeSrc).key, sizeTestKey(t, unlimited, largeSrc).key)
			bk := sizeTestBlock(t, large, largeSrc, sizeTestBlockStart)
			for i := range 5 {
				// the larger limit keeps the block, which the smaller one never reads, nor drops
				reads := sizeTestRead(t, large, largeSrc, sizeTestBlockStart, sizeTestBlockEnd)
				if i == 0 {
					require.Equal(t, whole, reads)
				} else {
					require.Empty(t, reads, "round %d", i)
				}
				require.Equal(t, whole, sizeTestRead(t, small, smallSrc, sizeTestBlockStart, sizeTestBlockEnd))
				require.Equal(t, filled, cachedSize(t, large, bk))
			}
			// a cache of the same limit shares the block
			same := newRawTestCache(shared, RawCacheOptions{MaxObjectSize: sizeTestLarge})
			require.Empty(t, sizeTestRead(t, same, rawTestSource(t), sizeTestBlockStart, sizeTestBlockEnd))
		})
	}
}

// sizeTestFilledFrom returns the size of the test block once reads of each range in turn fill it, in an unlimited cache
func sizeTestFilledFrom(t *testing.T, newCache func() cache.Cache, ranges ...[2]int64) int64 {
	t.Helper()
	src := rawTestSource(t)
	rc := newRawTestCache(newCache(), RawCacheOptions{})
	for _, r := range ranges {
		sizeTestRead(t, rc, src, r[0], r[1])
	}
	return cachedSize(t, rc, sizeTestBlock(t, rc, src, sizeTestBlockStart))
}

func TestRawCacheRefusalFollowsItsBlock(t *testing.T) {
	const early = sizeTestBlockStart + 10*rawTestMinute
	rest := [2]int64{sizeTestNarrowEnd + 1, sizeTestBlockEnd}
	for name, newCache := range rawTestCaches(t) {
		t.Run(name, func(t *testing.T) {
			_, filled := sizeTestSizes(t, newCache)
			// the whole block is past the limit, while the rest of it alone, or with less before it, fits
			limit := filled - 1
			require.Less(t, sizeTestFilledFrom(t, newCache, rest), limit)
			require.Less(t, sizeTestFilledFrom(t, newCache, [2]int64{sizeTestBlockStart, early}, rest), limit)
			cases := map[string]struct {
				// drop takes from the cache the block a merge was refused for
				drop func(rc *rawTestCache, bk string)
				// before, when set, is read once the block is dropped, so the block holds it alone
				before *[2]int64
				want   timeseries.ExtentList
			}{
				"removed": {
					drop: func(rc *rawTestCache, bk string) { rc.engine.Remove(bk) },
					want: timeseries.ExtentList{extent(rest[0], rest[1])},
				},
				"marked bad": {
					drop: func(rc *rawTestCache, bk string) {
						rc.engine.Store(bk, &nativedelta.Entry[*rawBlock]{Marker: true})
					},
					want: timeseries.ExtentList{extent(rest[0], rest[1])},
				},
				"refilled with less": {
					drop:   func(rc *rawTestCache, bk string) { rc.engine.Remove(bk) },
					before: &[2]int64{sizeTestBlockStart, early},
					want:   timeseries.ExtentList{extent(sizeTestBlockStart, early), extent(rest[0], rest[1])},
				},
			}
			for drop, tc := range cases {
				t.Run(drop, func(t *testing.T) {
					src := rawTestSource(t)
					c, counts := counting(newCache())
					var failures failureLog
					rc := newRawTestCache(c, RawCacheOptions{MaxObjectSize: limit, ObserveCacheFailure: failures.observe})
					bk := sizeTestBlock(t, rc, src, sizeTestBlockStart)
					sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestNarrowEnd)
					counts.take(bk)
					// merging the rest into the block is refused, and while the block holds what it did, not tried again
					for i, retrievals := range []int{2, 1} {
						require.Equal(t, [][2]int64{rest}, sizeTestRead(t, rc, src, sizeTestBlockStart, sizeTestBlockEnd))
						require.Equal(t, retrievals, counts.take(bk), "wide read %d", i)
					}
					// nor by a read of the rest alone, of which the block holds nothing, nor by one reaching into the next block too
					next := sizeTestBlockEnd + 20*rawTestMinute
					sizeTestRead(t, rc, src, sizeTestBlockEnd+1, next)
					for _, maxt := range []int64{rest[1], next} {
						require.Equal(t, [][2]int64{rest}, sizeTestRead(t, rc, src, rest[0], maxt))
						require.Equal(t, 1, counts.take(bk), "a read to %d", maxt)
					}
					require.Equal(t, []string{sizeTestRefused}, failures.take())
					entry, ok := rc.engine.Retrieve(bk)
					require.True(t, ok)
					require.Equal(t, timeseries.ExtentList{extent(sizeTestBlockStart, sizeTestNarrowEnd)}, entry.Extents)

					// once the block no longer holds what the merge was refused against, the rest is tried, and kept
					tc.drop(rc, bk)
					if tc.before != nil {
						sizeTestRead(t, rc, src, tc.before[0], tc.before[1])
					}
					require.Equal(t, [][2]int64{rest}, sizeTestRead(t, rc, src, rest[0], rest[1]))
					require.Empty(t, sizeTestRead(t, rc, src, rest[0], rest[1]), "the rest is then served from the block")
					require.Empty(t, failures.take())
					entry, ok = rc.engine.Retrieve(bk)
					require.True(t, ok)
					require.Equal(t, tc.want, entry.Extents)
				})
			}
		})
	}
}

func TestRawCacheRefusalNeedsWhatItWasMergedOnto(t *testing.T) {
	const lo, hi = 40 * rawTestMinute, 80 * rawTestMinute
	type list = timeseries.ExtentList
	before, later := extent(0, 20*rawTestMinute), extent(30*rawTestMinute, 35*rawTestMinute)
	cases := map[string]struct {
		// onto is what the block held when its samples from lo to hi were refused, and held what it holds as a read begins
		onto, held list
		// lo and hi move the store's range from the refused one's
		lo, hi  int64
		skipped bool
	}{
		"the same":                   {onto: list{before}, held: list{before}, skipped: true},
		"more around it":             {onto: list{before}, held: list{extent(0, 25*rawTestMinute)}, skipped: true},
		"more besides it":            {onto: list{before}, held: list{before, later}, skipped: true},
		"all its parts, joined":      {onto: list{before, later}, held: list{extent(0, 35*rawTestMinute)}, skipped: true},
		"more samples than refused":  {onto: list{before}, held: list{before}, lo: -rawTestMinute, hi: rawTestMinute, skipped: true},
		"stored anew, with no block": {skipped: true},
		"stored anew, onto a block":  {held: list{later}, skipped: true},
		"less of its start":          {onto: list{before}, held: list{extent(rawTestMinute, 20*rawTestMinute)}},
		"less of its end":            {onto: list{before}, held: list{extent(0, 19*rawTestMinute)}},
		"one of its parts":           {onto: list{before, later}, held: list{later}},
		"nothing, as once evicted":   {onto: list{before}},
		"fewer samples than refused": {onto: list{before}, held: list{before}, lo: rawTestMinute},
		"fewer samples, stored anew": {hi: -rawTestMinute},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rc := newRawTestCache(&rawByteCache{}, RawCacheOptions{})
			bk := blockKey(storeTestKey, 0)
			s := stripe(bk)
			rc.locks[s].Lock()
			rc.refuse(s, bk, lo, hi, tc.onto)
			rc.locks[s].Unlock()
			// a store not skipped keeps its samples, as the cache sets no limit
			err := rc.keepBlock(bk, nil, lo+tc.lo, hi+tc.hi, tc.held)
			_, kept := rc.engine.Retrieve(bk)
			if tc.skipped {
				require.ErrorIs(t, err, nativedelta.ErrObjectTooLarge)
				require.False(t, kept)
				return
			}
			require.NoError(t, err)
			require.True(t, kept)
		})
	}
}

func TestHeldExtentsByBlock(t *testing.T) {
	first := timeseries.ExtentList{extent(3*rawBlockMs, 3*rawBlockMs+1)}
	third := timeseries.ExtentList{extent(5*rawBlockMs, 5*rawBlockMs+1)}
	held := heldExtents{first: 3, extents: []timeseries.ExtentList{first, nil, third}}
	for b, want := range map[int64]timeseries.ExtentList{2: nil, 3: first, 4: nil, 5: third, 6: nil} {
		require.Equal(t, want, held.of(b), "block %d", b)
	}
	require.Nil(t, heldExtents{}.of(0), "a select that found no block of its own holds none")
}
