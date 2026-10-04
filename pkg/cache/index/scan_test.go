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
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// stores an object behind the index's back
func plant(t testing.TB, s *blobtest.MemStore, key string, value []byte, ttl time.Duration) {
	t.Helper()
	require.NoError(t, blob.NewClient(s, testName, testProvider).Store(key, value, ttl))
}

func sortedKeys(idx *IndexedClient) []string {
	keys := idx.Keys()
	slices.Sort(keys)
	return keys
}

// a cache with no index is swept at once, and the index lists all that it holds
func TestSweepRebuildsALostIndex(t *testing.T) {
	s := blobtest.NewMemStore()
	start := time.Now()
	for i := range 10 {
		plant(t, s, "key-"+strconv.Itoa(i), make([]byte, i+1), time.Hour)
	}
	plant(t, s, "forever", []byte("1"), 0)
	plant(t, s, "expired", []byte("1"), time.Nanosecond)
	s.SetFrame("corrupt", []byte("what a cache of an earlier format held"))
	adopted := metrics.CacheEvents.WithLabelValues(testName, testProvider, reasonSweep, reasonAdopted)
	before := testutil.ToFloat64(adopted)

	o := idleOpts()
	o.ScanBatchSize = 3
	idx := openSweptIndex(t, s, o)
	await(t, idx.hasSwept)
	requireTotals(t, idx, 11, 56)
	require.Equal(t, before+11, testutil.ToFloat64(adopted))
	require.Equal(t, 11, s.Len(), "what cannot be served is removed from the cache")

	k, _ := idx.Object("key-4")
	require.Equal(t, int64(5), k.Size())
	require.WithinDuration(t, start.Add(time.Hour), k.Expiration(), time.Minute)
	require.WithinDuration(t, start, k.LastWrite(), time.Minute)
	require.Equal(t, k.LastWrite(), k.LastAccess())
	forever, _ := idx.Object("forever")
	require.True(t, forever.Expiration().IsZero())

	// what was adopted is persisted, and expires, as any other object
	require.NoError(t, idx.Close())
	idx = openSweptIndex(t, s, idleOpts())
	require.False(t, idx.sweepDue)
	requireTotals(t, idx, 11, 56)
	idx.reapAt(time.Now().Add(2 * time.Hour).UnixNano())
	require.Equal(t, []string{"forever"}, idx.Keys())
}

func TestSweepReconciles(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openSweptIndex(t, s, idleOpts())
	await(t, idx.hasSwept)
	dropped := metrics.CacheEvents.WithLabelValues(testName, testProvider, reasonSweep, reasonDropped)
	sweeps := metrics.CacheEvents.WithLabelValues(testName, testProvider, eventIndex, reasonSweep)
	before, sweepsBefore := testutil.ToFloat64(dropped), testutil.ToFloat64(sweeps)

	require.NoError(t, idx.Store("kept", []byte("1"), time.Hour))
	require.NoError(t, idx.Store("lost", []byte("12"), time.Hour))
	require.NoError(t, idx.Store("also lost", []byte("123"), time.Hour))
	time.Sleep(time.Millisecond)
	require.NoError(t, s.Delete("lost", "also lost"))
	plant(t, s, "orphan", []byte("1234"), time.Hour)
	plant(t, s, IndexKey, []byte("1"), time.Hour)

	idx.forceSweep <- true
	await(t, idx.hasSwept)
	require.Equal(t, []string{"kept", "orphan"}, sortedKeys(idx))
	requireTotals(t, idx, 2, 5)
	require.Equal(t, before+2, testutil.ToFloat64(dropped))
	require.Equal(t, sweepsBefore+1, testutil.ToFloat64(sweeps))

	// an index that agrees with the cache is left as it is
	idx.forceSweep <- true
	await(t, idx.hasSwept)
	requireTotals(t, idx, 2, 5)
	require.Equal(t, before+2, testutil.ToFloat64(dropped))
}

// an object written while the cache is swept may lie behind the sweep, and is not
// taken for one the cache has lost
func TestSweepLeavesNewWritesAlone(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openSweptIndex(t, s, idleOpts())
	await(t, idx.hasSwept)
	require.NoError(t, idx.Store("written", []byte("1"), time.Hour))
	require.NoError(t, s.Delete("written"))
	o, _ := idx.Object("written")
	o.lastWrite.Store(time.Now().Add(time.Minute).UnixNano())
	idx.sweep(t.Context())
	require.Equal(t, []string{"written"}, idx.Keys())
}

func TestSweepPausesBetweenBatches(t *testing.T) {
	s := blobtest.NewMemStore()
	for i := range 10 {
		plant(t, s, "key-"+strconv.Itoa(i), []byte("1"), time.Hour)
	}
	o := idleOpts()
	o.ScanBatchSize = 4
	o.ScanBatchPause = timeconv.Duration(20 * time.Millisecond)
	idx := openIndex(t, s, o)
	idx.scanner = blob.NewClient(s, testName, testProvider)

	start := time.Now()
	idx.sweep(t.Context())
	require.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond, "three batches, and a pause after two")
	requireTotals(t, idx, 10, 10)

	// a sweep that is stopped in a pause leaves the index as far as it got
	idx.Clear()
	o.ScanBatchPause = timeconv.Duration(time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(10*time.Millisecond, cancel)
	idx.sweep(ctx)
	requireTotals(t, idx, 4, 4)
}

func TestSweepFailure(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	idx.scanner = blob.NewClient(s, testName, testProvider)
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	require.NoError(t, s.Delete("k"))
	s.ScanErr = errFault
	idx.sweep(t.Context())
	requireTotals(t, idx, 1, 1)
}

func TestScannerWorker(t *testing.T) {
	s := blobtest.NewMemStore()
	o := idleOpts()
	o.ScanInterval = timeconv.Duration(time.Millisecond)
	idx := openSweptIndex(t, s, o)
	plant(t, s, "orphan", []byte("1"), time.Hour)
	require.Eventually(t, func() bool { return idx.Count() == 1 }, testTimeout, time.Millisecond)
	require.Eventually(t, func() bool { return len(idx.hasSwept) == 1 }, testTimeout, time.Millisecond)
	require.NoError(t, idx.Close())
	require.True(t, idx.scannerExited.Load())

	// an index that vouches for the cache, and has no interval, starts no scanner
	idx = openSweptIndex(t, s, idleOpts())
	require.False(t, idx.sweepDue)
	plant(t, s, "another", []byte("1"), time.Hour)
	select {
	case idx.forceSweep <- true:
		t.Fatal("a scanner is running")
	case <-time.After(10 * time.Millisecond):
	}
	requireTotals(t, idx, 1, 1)
}
