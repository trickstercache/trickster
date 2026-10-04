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

package index

import (
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	cm "github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestBucketOf(t *testing.T) {
	const now = int64(1_000_000 * time.Minute)
	tests := []struct {
		name            string
		expiration, due time.Duration
	}{
		{"on the second", 15 * time.Second, 15 * time.Second},
		{"within the second", 15*time.Second + 1, 16 * time.Second},
		{"end of the fine window", time.Minute, time.Minute},
		{"past the fine window", time.Minute + 1, 2 * time.Minute},
		{"on the minute", 6 * time.Minute, 6 * time.Minute},
		{"within the minute", 6*time.Minute + time.Second, 7 * time.Minute},
		{"already past", -time.Second, -time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// the time of reference is on a minute, so the offsets are those of the buckets
			require.Zero(t, now%coarseBucket)
			due := bucketOf(now+int64(test.expiration), now)
			require.Equal(t, now+int64(test.due), due)
			require.GreaterOrEqual(t, due, now+int64(test.expiration), "a bucket is never due before its objects expire")
		})
	}
}

func TestReapExpired(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	evictions := metrics.CacheEvents.WithLabelValues(testName, "memory", eventEviction, reasonTTL)
	before := testutil.ToFloat64(evictions)
	now := time.Now()
	require.NoError(t, idx.Store("soon", []byte("1"), 15*time.Second))
	require.NoError(t, idx.Store("later", []byte("22"), 10*time.Minute))
	require.NoError(t, idx.Store("never", []byte("333"), 0))

	idx.reapAt(now.Add(14 * time.Second).UnixNano())
	requireTotals(t, idx, 3, 6)
	require.Equal(t, before, testutil.ToFloat64(evictions))

	idx.reapAt(now.Add(17 * time.Second).UnixNano())
	requireTotals(t, idx, 2, 5)
	require.NotContains(t, mc.data, "soon")
	require.Equal(t, before+1, testutil.ToFloat64(evictions))

	idx.reapAt(now.Add(12 * time.Minute).UnixNano())
	requireTotals(t, idx, 1, 3)
	require.Equal(t, []string{"never"}, idx.Keys())
	idx.reapAt(now.Add(24 * time.Hour).UnixNano())
	requireTotals(t, idx, 1, 3)
}

func TestReapFollowsTheLatestWrite(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	now := time.Now()
	require.NoError(t, idx.Store("extended", []byte("1"), 15*time.Second))
	require.NoError(t, idx.Store("extended", []byte("1"), 10*time.Minute))
	require.NoError(t, idx.Store("shortened", []byte("1"), 10*time.Minute))
	require.NoError(t, idx.Store("shortened", []byte("1"), 15*time.Second))
	require.NoError(t, idx.Store("unexpiring", []byte("1"), 15*time.Second))
	require.NoError(t, idx.Store("unexpiring", []byte("1"), 0))
	require.NoError(t, idx.Store("removed", []byte("1"), 15*time.Second))
	require.NoError(t, idx.Remove("removed"))
	require.NoError(t, idx.Store("removed", []byte("1"), 10*time.Minute))

	idx.reapAt(now.Add(time.Minute).UnixNano())
	keys := idx.Keys()
	slices.Sort(keys)
	require.Equal(t, []string{"extended", "removed", "unexpiring"}, keys)

	idx.reapAt(now.Add(12 * time.Minute).UnixNano())
	require.Equal(t, []string{"unexpiring"}, idx.Keys())
}

func TestReapReschedulesWhatIsNotExpired(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), 10*time.Minute))
	o, _ := idx.Object("k")
	due := o.due
	// the object comes to expire later, by a write its bucket did not hear of
	later := time.Unix(0, due).Add(10 * time.Minute)
	o.expiration.Store(later.UnixNano())

	idx.reapAt(due)
	requireTotals(t, idx, 1, 1)
	require.Greater(t, o.due, due)
	idx.reapAt(later.Add(time.Minute).UnixNano())
	requireTotals(t, idx, 0, 0)

	// an object that leaves the index while it is taken as due is not put back
	require.NoError(t, idx.Store("k", []byte("1"), 10*time.Minute))
	o, _ = idx.Object("k")
	due = o.due
	idx.forget("k")
	require.Empty(t, idx.shards.of("k").buckets[due], "removed from its bucket with the index")
	idx.shards.of("k").reschedule(o, time.Now().UnixNano())
	require.Zero(t, o.due)
}

func TestReapInBatches(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	const n = 2*evictionBatch + 10
	for i := range n {
		require.NoError(t, idx.Store("key-"+strconv.Itoa(i), []byte("1"), 15*time.Second))
	}
	time.Sleep(time.Millisecond)
	idx.reapAt(time.Now().Add(time.Minute).UnixNano())
	requireTotals(t, idx, 0, 0)
	require.Empty(t, mc.data)
}

// n objects of one byte each, the first of them used longest ago
func fill(t testing.TB, idx *IndexedClient, n int) {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	for i := range n {
		key := "key-" + strconv.Itoa(i)
		require.NoError(t, idx.Store(key, []byte("1"), time.Hour))
		o, _ := idx.Object(key)
		o.lastAccess.Store(at.Add(time.Duration(i) * time.Millisecond).UnixNano())
	}
}

func TestOverage(t *testing.T) {
	tests := []struct {
		name           string
		o              options.Options
		free, minFree  int64
		bytes, objects int64
		reason         string
	}{
		{name: "within its size", o: options.Options{MaxSizeBytes: 100, MaxSizeObjects: 100}},
		{name: "no limits"},
		{name: "bytes", o: options.Options{MaxSizeBytes: 60, MaxSizeBackoffBytes: 10}, bytes: 50, reason: reasonBytes},
		{name: "bytes, backoff over the limit", o: options.Options{MaxSizeBytes: 60, MaxSizeBackoffBytes: 60}, bytes: 40, reason: reasonBytes},
		{name: "objects", o: options.Options{MaxSizeObjects: 60, MaxSizeBackoffObjects: 10}, objects: 50, reason: reasonObjects},
		{name: "objects, backoff over the limit", o: options.Options{MaxSizeObjects: 60, MaxSizeBackoffObjects: 60}, objects: 40, reason: reasonObjects},
		{
			name: "both", o: options.Options{MaxSizeBytes: 60, MaxSizeObjects: 80},
			bytes: 40, objects: 20, reason: reasonBytes,
		},
		{name: "free space", o: options.Options{MaxSizeBackoffBytes: 5}, free: 70, minFree: 100, bytes: 35, reason: reasonFreeSpace},
		{name: "free space enough", free: 100, minFree: 100},
		{
			name: "free space short by less than the cache is over", o: options.Options{MaxSizeBytes: 60},
			free: 90, minFree: 100, bytes: 40, reason: reasonBytes,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := blobtest.NewMemStore()
			s.Free = test.free
			idx := openIndex(t, s, idleOpts(), func(o *IndexedClientOptions) { o.MinFreeBytes = test.minFree })
			fill(t, idx, 100)
			bytes, objects, reason := idx.overage(&test.o)
			require.Equal(t, test.bytes, bytes)
			require.Equal(t, test.objects, objects)
			require.Equal(t, test.reason, reason)
		})
	}
	t.Run("free space unknown", func(t *testing.T) {
		s := blobtest.NewMemStore()
		s.FreeErr = errFault
		idx := openIndex(t, s, idleOpts(), func(o *IndexedClientOptions) { o.MinFreeBytes = 100 })
		_, _, reason := idx.overage(&options.Options{})
		require.Empty(t, reason)
		plain, _ := newPlainIndex(t, idleOpts())
		plain.ico.MinFreeBytes = 100
		_, _, reason = plain.overage(&options.Options{})
		require.Empty(t, reason)
	})
}

func TestEvictLeastRecentlyUsed(t *testing.T) {
	for name, o := range map[string]*options.Options{
		"bytes":   {MaxSizeBytes: 60, MaxSizeBackoffBytes: 10},
		"objects": {MaxSizeObjects: 60, MaxSizeBackoffObjects: 10},
	} {
		t.Run(name, func(t *testing.T) {
			idx, mc := newPlainIndex(t, idleOpts())
			fill(t, idx, 100)
			time.Sleep(time.Millisecond)
			reason := reasonBytes
			if o.MaxSizeObjects > 0 {
				reason = reasonObjects
			}
			evictions := metrics.CacheEvents.WithLabelValues(testName, "memory", eventEviction, reason)
			dels := metrics.CacheObjectOperations.WithLabelValues(testName, "memory", cm.KeyDel, cm.KeyNone)
			before, delsBefore := testutil.ToFloat64(evictions), testutil.ToFloat64(dels)

			idx.UpdateOptions(o)
			idx.reap()
			requireTotals(t, idx, 50, 50)
			require.Len(t, mc.data, 50)
			for i := range 100 {
				_, listed := idx.Object("key-" + strconv.Itoa(i))
				require.Equal(t, i >= 50, listed, "object %d", i)
			}
			require.Equal(t, before+1, testutil.ToFloat64(evictions))
			require.Equal(t, delsBefore+1, testutil.ToFloat64(dels), "the reaper records its own removals")

			idx.reap()
			requireTotals(t, idx, 50, 50)
		})
	}
}

func TestEvictAll(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	fill(t, idx, 10)
	time.Sleep(time.Millisecond)
	// more is asked for than the cache holds, as when its totals have drifted
	idx.cacheSize.Add(100)
	idx.evictOverage(&options.Options{MaxSizeBytes: 1}, time.Now())
	requireTotals(t, idx, 0, 100)
	idx.evictOverage(&options.Options{MaxSizeBytes: 1}, time.Now())
	requireTotals(t, idx, 0, 100)
	idx.cacheSize.Store(0)

	fill(t, idx, 2*exactEvictionLimit)
	idx.cacheSize.Add(1 << 20)
	idx.evictOverage(&options.Options{MaxSizeBytes: 1}, time.Now())
	require.Less(t, idx.Count(), int64(evictionBatch), "sampling finds all but the last few of a cache")
	idx.cacheSize.Store(idx.Count())
}

// a large cache is not ranked whole: the object evicted is the one used longest ago of
// a few taken at random, which must still be among those used long ago
func TestEvictBySampling(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	const n, evicted = 20_000, 2_000
	fill(t, idx, n)
	require.Greater(t, idx.Count(), int64(exactEvictionLimit))

	idx.UpdateOptions(&options.Options{MaxSizeObjects: n - evicted + 100, MaxSizeBackoffObjects: 100})
	idx.reap()
	requireTotals(t, idx, n-evicted, n-evicted)
	require.Len(t, mc.data, n-evicted)

	var oldest, old int
	for i := range n {
		if _, listed := idx.Object("key-" + strconv.Itoa(i)); listed {
			continue
		}
		if i < evicted {
			oldest++
		}
		if i < n/4 {
			old++
		}
	}
	// a full ranking would evict exactly the first tenth
	require.Greater(t, oldest, evicted/2, "evicted from the tenth used longest ago")
	require.Greater(t, old, evicted*98/100, "evicted from the quarter used longest ago")
}

func TestVictim(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	require.Nil(t, idx.victim(), "an empty cache")
	fill(t, idx, 8*shardCount)
	require.NotNil(t, idx.victim())
	idx.each(func(o *Object) bool {
		o.evicting.Store(true)
		return true
	})
	require.Nil(t, idx.victim(), "an object is chosen once")
}

func TestByLastAccess(t *testing.T) {
	a, b := &Object{}, &Object{}
	a.lastAccess.Store(1)
	b.lastAccess.Store(2)
	require.Negative(t, byLastAccess(a, b))
	require.Positive(t, byLastAccess(b, a))
	require.Zero(t, byLastAccess(a, a))
}

func TestReaperWorker(t *testing.T) {
	t.Run("interval", func(t *testing.T) {
		o := idleOpts()
		o.ReapInterval = timeconv.Duration(time.Millisecond)
		o.MaxSizeObjects = 5
		idx, _ := newPlainIndex(t, o)
		fill(t, idx, 10)
		require.Eventually(t, func() bool { return idx.Count() == 5 }, testTimeout, time.Millisecond)
		// a pass with no test waiting on it signals no one
		require.Eventually(t, func() bool { return len(idx.hasReaped) == 1 }, testTimeout, time.Millisecond)
		require.NoError(t, idx.Close())
		require.True(t, idx.reaperExited.Load())
	})
	t.Run("pressure", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := idleOpts()
			o.MaxSizeObjects = 5
			idx, _ := newPlainIndex(t, o)
			require.False(t, idx.pressure.Load())
			fill(t, idx, 10)
			require.True(t, idx.pressure.Load())
			time.Sleep(pressureInterval)
			synctest.Wait()
			require.EqualValues(t, 5, idx.Count())
			require.False(t, idx.pressure.Load())
		})
	})
	t.Run("forced", func(t *testing.T) {
		idx, _ := newPlainIndex(t, idleOpts())
		fill(t, idx, 10)
		idx.UpdateOptions(&options.Options{ReapInterval: timeconv.Duration(time.Hour), MaxSizeObjects: 5})
		idx.forceReap <- true
		await(t, idx.hasReaped)
		requireTotals(t, idx, 5, 5)
	})
}

func TestShardRaces(t *testing.T) {
	now := time.Now().UnixNano()
	t.Run("removed before it was inserted", func(t *testing.T) {
		var s shard
		o := &Object{Key: "k", slot: noSlot}
		o.size.Store(5)
		require.Zero(t, s.remove(o), "an object that was never counted frees nothing")
		_, inserted := s.insert(o, now)
		require.False(t, inserted)
		require.Empty(t, s.objects)
		_, updated := s.update(o, 9, 0, now)
		require.False(t, updated)
		require.Zero(t, s.remove(o), "removed once")
	})
	t.Run("written again before it was inserted", func(t *testing.T) {
		var s shard
		o := &Object{Key: "k", slot: noSlot}
		o.size.Store(5)
		delta, updated := s.update(o, 9, 0, now)
		require.True(t, updated)
		require.Zero(t, delta, "the size is counted by whoever inserts the object")
		counted, inserted := s.insert(o, now)
		require.True(t, inserted)
		require.Equal(t, int64(9), counted)
		delta, _ = s.update(o, 4, 0, now)
		require.Equal(t, int64(-5), delta)
		require.Equal(t, int64(4), s.remove(o))
	})
	t.Run("slots are kept dense", func(t *testing.T) {
		var s shard
		objects := make([]*Object, 5)
		for i := range objects {
			objects[i] = &Object{Key: strconv.Itoa(i), slot: noSlot}
			s.insert(objects[i], now)
		}
		s.remove(objects[1])
		s.remove(objects[4])
		s.remove(objects[0])
		require.Len(t, s.objects, 2)
		for slot, o := range s.objects {
			require.Equal(t, slot, o.slot)
			require.Same(t, o, s.sample().pick(o))
		}
		s.clear()
		require.Nil(t, s.sample())
		require.Equal(t, noSlot, objects[2].slot)
	})
}

// want, when the receiver is any object at all, which is all a sample promises
func (o *Object) pick(want *Object) *Object {
	if o == nil {
		return nil
	}
	return want
}

func TestEachStopsWhenTold(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	fill(t, idx, 10)
	var n int
	idx.each(func(*Object) bool { n++; return n < 3 })
	require.Equal(t, 3, n)
}

// an object written again after it was chosen for removal is not removed, in the index or the cache
func TestEvictSparesAFreshWrite(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("stale", []byte("1"), time.Second))
	require.NoError(t, idx.Store("fresh", []byte("1"), time.Second))
	since := time.Now()
	time.Sleep(time.Millisecond)
	require.NoError(t, idx.Store("fresh", []byte("12"), time.Hour))

	idx.evict(reasonTTL, since, []string{"stale", "fresh", "absent"})
	requireTotals(t, idx, 1, 2)
	_, listed := idx.Object("fresh")
	require.True(t, listed)
	_, held := s.Frame("fresh")
	require.True(t, held)
	_, held = s.Frame("stale")
	require.False(t, held)
}

func TestEvictReschedulesSparedTTL(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), time.Nanosecond))
	o, ok := idx.Object("k")
	require.True(t, ok)
	idx.shards.of("k").takeDue(nil, time.Now().Add(time.Minute).UnixNano())
	require.Zero(t, o.due)

	idx.evict(reasonTTL, o.LastWrite(), []string{"k"})
	requireTotals(t, idx, 1, 1)
	require.NotZero(t, o.due)
}

// a key that is stored while the reaper removes it ends up in the index and the cache, or in neither
func TestStoreAndRemoveAgree(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range 500 {
				key := "key-" + strconv.Itoa(i%16)
				if i%3 == 0 {
					idx.Remove(key)
				} else {
					idx.Store(key, []byte("v"), time.Hour)
				}
			}
		})
		wg.Go(func() {
			for i := range 500 {
				key := "key-" + strconv.Itoa(i%16)
				idx.evict(reasonBytes, time.Now(), []string{key})
			}
		})
	}
	wg.Wait()
	for i := range 16 {
		key := "key-" + strconv.Itoa(i)
		_, listed := idx.Object(key)
		_, held := s.Frame(key)
		require.Equal(t, held, listed, key)
	}
}

// an object is in one expiry bucket at a time, however often its expiration moves
func TestExpiryBucketsHoldEachObjectOnce(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	for i := range 200 {
		require.NoError(t, idx.Store("k", []byte("1"), time.Duration(i+1)*time.Hour))
		require.NoError(t, idx.Store("other", []byte("1"), time.Duration(200-i)*time.Hour))
	}
	require.NoError(t, idx.Store("k", []byte("1"), 0))
	var members int
	for i := range idx.shards {
		for _, bucket := range idx.shards[i].buckets {
			members += len(bucket)
		}
	}
	require.Equal(t, 1, members, "one object expires, and is in one bucket")
	require.NoError(t, idx.Remove("other"))
	members = 0
	for i := range idx.shards {
		for _, bucket := range idx.shards[i].buckets {
			members += len(bucket)
		}
	}
	require.Zero(t, members)
}

// an object spared from eviction by a fresh write can be chosen again later
func TestSparedObjectCanBeEvictedLater(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	fill(t, idx, 8*shardCount)
	// chosen, then written again before it could be removed
	o, _ := idx.Object("key-0")
	o.evicting.Store(true)
	since := time.Now()
	time.Sleep(time.Millisecond)
	require.NoError(t, idx.Store("key-0", []byte("1"), time.Hour))
	idx.evict(reasonBytes, since, []string{"key-0"})
	_, listed := idx.Object("key-0")
	require.True(t, listed)
	require.False(t, o.evicting.Load(), "cleared when spared")

	o.evicting.Store(true)
	require.NoError(t, idx.Store("key-0", []byte("1"), time.Hour))
	require.False(t, o.evicting.Load(), "cleared when written again")

	// with every other object marked, only the spared one is left to choose
	idx.each(func(x *Object) bool {
		if x != o {
			x.evicting.Store(true)
		}
		return true
	})
	var chosen *Object
	for range 500 {
		if v := idx.victim(); v != nil {
			require.Same(t, o, v)
			chosen = v
		}
	}
	require.Same(t, o, chosen)
}

// emptied buckets leave neither an entry in the map nor, for long, a time in the heap
func TestEmptiedBucketsAreDropped(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	for i := range 500 {
		require.NoError(t, idx.Store("k", []byte("1"), time.Duration(i+1)*time.Hour))
	}
	s := idx.shards.of("k")
	require.Len(t, s.buckets, 1)
	require.LessOrEqual(t, len(s.times), 2, "stale times are rebuilt away")
	require.NoError(t, idx.Remove("k"))
	require.Empty(t, s.buckets)
	require.LessOrEqual(t, len(s.times), 2)

	// a stale time is skipped when it comes, and the count kept with it
	other := "other"
	for i := 0; idx.shards.of(other) != s; i++ {
		other = "other-" + strconv.Itoa(i)
	}
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	require.NoError(t, idx.Store("k", []byte("1"), 2*time.Hour))
	require.NoError(t, idx.Store(other, []byte("1"), 3*time.Hour))
	s.mu.Lock()
	stale := s.stale
	s.mu.Unlock()
	idx.reapAt(time.Now().Add(90 * time.Minute).UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, max(stale-1, 0), s.stale)
	require.Len(t, s.buckets, 2)
}
