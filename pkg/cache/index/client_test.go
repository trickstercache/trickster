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
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	cm "github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	testName     = "test"
	testProvider = "filesystem"
	testTimeout  = 5 * time.Second
)

var errFault = errors.New("fault")

// a cache with none of the capabilities an index can make use of
type mapClient struct {
	mu          sync.Mutex
	data        map[string][]byte
	storeErr    error
	removeErr   error
	retrieveErr error
	status      status.LookupStatus
}

func newMapClient() *mapClient {
	return &mapClient{data: make(map[string][]byte)}
}

func (m *mapClient) Connect() error { return nil }
func (m *mapClient) Close() error   { return nil }

func (m *mapClient) Store(key string, b []byte, _ time.Duration) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.mu.Lock()
	m.data[key] = append([]byte(nil), b...)
	m.mu.Unlock()
	return nil
}

func (m *mapClient) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	if m.retrieveErr != nil || m.status != status.LookupStatusHit {
		return nil, m.status, m.retrieveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	return append([]byte(nil), b...), status.LookupStatusHit, nil
}

func (m *mapClient) Remove(keys ...string) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	m.mu.Lock()
	for _, k := range keys {
		delete(m.data, k)
	}
	m.mu.Unlock()
	return nil
}

func (m *mapClient) drop(key string) {
	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()
}

// no worker acts under these unless a test tells it to
func idleOpts() *options.Options {
	return &options.Options{
		ReapInterval:  timeconv.Duration(time.Hour),
		FlushInterval: timeconv.Duration(time.Hour),
		IndexExpiry:   timeconv.Duration(time.Hour),
	}
}

func newPlainIndex(t testing.TB, o *options.Options) (*IndexedClient, *mapClient) {
	t.Helper()
	mc := newMapClient()
	idx := NewIndexedClient(testName, "memory", o, mc)
	t.Cleanup(func() { idx.Close() })
	return idx, mc
}

// a cache that cannot list what it holds, so its index never sweeps it
type unscannedClient struct {
	c *blob.Client
}

func (u unscannedClient) Connect() error                    { return u.c.Connect() }
func (u unscannedClient) Close() error                      { return u.c.Close() }
func (u unscannedClient) Remove(keys ...string) error       { return u.c.Remove(keys...) }
func (u unscannedClient) MetaStore() (blob.MetaStore, bool) { return u.c.MetaStore() }
func (u unscannedClient) FreeBytes() (int64, bool)          { return u.c.FreeBytes() }

func (u unscannedClient) Store(key string, b []byte, ttl time.Duration) error {
	return u.c.Store(key, b, ttl)
}

func (u unscannedClient) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	return u.c.Retrieve(key)
}

// the cache keeps its objects and the index's files in s, and the index does not sweep it
func openIndex(t testing.TB, s *blobtest.MemStore, o *options.Options, opts ...func(*IndexedClientOptions)) *IndexedClient {
	t.Helper()
	c := unscannedClient{blob.NewClient(s, testName, testProvider)}
	idx := NewIndexedClient(testName, testProvider, o, c, opts...)
	t.Cleanup(func() { idx.crash() })
	return idx
}

// the cache keeps its objects and the index's files in s, and the index sweeps it
func openSweptIndex(t testing.TB, s *blobtest.MemStore, o *options.Options) *IndexedClient {
	t.Helper()
	idx := NewIndexedClient(testName, testProvider, o, blob.NewClient(s, testName, testProvider))
	t.Cleanup(func() { idx.crash() })
	return idx
}

// stops the workers and persists nothing more, as a crash would
func (idx *IndexedClient) crash() {
	idx.cancel()
	idx.wg.Wait()
}

func await(t testing.TB, done chan bool) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("the worker did not finish a pass")
	}
}

func requireTotals(t testing.TB, idx *IndexedClient, count, size int64) {
	t.Helper()
	require.Equal(t, count, idx.Count())
	require.Equal(t, size, idx.Size())
	require.Len(t, idx.Keys(), int(count))
	var slots int64
	idx.each(func(*Object) bool { slots++; return true })
	require.Equal(t, count, slots)
}

func TestStoreRetrieveRemove(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Connect())
	usage := metrics.CacheObjects.WithLabelValues(testName, "memory")
	usageBytes := metrics.CacheBytes.WithLabelValues(testName, "memory")
	requireTotals(t, idx, 0, 0)

	b, s, err := idx.Retrieve("foo")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, s)
	require.Empty(t, b)

	start := time.Now()
	require.NoError(t, idx.Store("foo", []byte("bar"), time.Minute))
	require.NoError(t, idx.Store("forever", []byte("12345"), 0))
	requireTotals(t, idx, 2, 8)
	require.Equal(t, 2.0, testutil.ToFloat64(usage))
	require.Equal(t, 8.0, testutil.ToFloat64(usageBytes))

	o, ok := idx.Object("foo")
	require.True(t, ok)
	require.Equal(t, "foo", o.Key)
	require.Equal(t, int64(3), o.Size())
	require.WithinDuration(t, start.Add(time.Minute), o.Expiration(), time.Second)
	require.WithinDuration(t, start, o.LastWrite(), time.Second)
	require.Equal(t, o.LastWrite(), o.LastAccess())
	forever, _ := idx.Object("forever")
	require.True(t, forever.Expiration().IsZero())
	_, ok = idx.Object("absent")
	require.False(t, ok)

	time.Sleep(time.Millisecond)
	b, s, err = idx.Retrieve("foo")
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, s)
	require.Equal(t, "bar", string(b))
	require.True(t, o.LastAccess().After(o.LastWrite()), "a retrieval is an access")

	// requested removals record freed bytes, but the delete operation is left to the cache manager
	dels := metrics.CacheObjectOperations.WithLabelValues(testName, "memory", cm.KeyDel, cm.KeyNone)
	delBytes := metrics.CacheByteOperations.WithLabelValues(testName, "memory", cm.KeyDel, cm.KeyNone)
	delsBefore, delBytesBefore := testutil.ToFloat64(dels), testutil.ToFloat64(delBytes)
	require.NoError(t, idx.Remove("foo", "absent"))
	require.Equal(t, delsBefore, testutil.ToFloat64(dels))
	require.Equal(t, delBytesBefore+3, testutil.ToFloat64(delBytes))
	requireTotals(t, idx, 1, 5)
	require.Equal(t, 1.0, testutil.ToFloat64(usage))
	_, s, _ = idx.Retrieve("foo")
	require.Equal(t, status.LookupStatusKeyMiss, s)

	idx.Clear()
	requireTotals(t, idx, 0, 0)
	require.Equal(t, 0.0, testutil.ToFloat64(usageBytes))
}

func TestIndexKeyIsReserved(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	require.ErrorIs(t, idx.Store(IndexKey, []byte("x"), time.Minute), ErrIndexInvalidCacheKey)
	b, s, err := idx.Retrieve(IndexKey)
	require.ErrorIs(t, err, ErrIndexInvalidCacheKey)
	require.Equal(t, status.LookupStatusError, s)
	require.Nil(t, b)
	require.Empty(t, mc.data)
}

func TestStoreFailureListsNothing(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	mc.storeErr = errFault
	require.ErrorIs(t, idx.Store("k", []byte("v"), time.Minute), errFault)
	requireTotals(t, idx, 0, 0)
}

func TestStoreAgainUpdatesInPlace(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), time.Minute))
	first, _ := idx.Object("k")
	require.NoError(t, idx.Store("k", []byte("12345"), time.Hour))
	second, _ := idx.Object("k")
	require.Same(t, first, second)
	require.WithinDuration(t, time.Now().Add(time.Hour), second.Expiration(), time.Minute)
	requireTotals(t, idx, 1, 5)
	require.NoError(t, idx.Store("k", []byte("12"), 0))
	require.True(t, second.Expiration().IsZero())
	requireTotals(t, idx, 1, 2)
}

func TestRetrieveForgetsWhatTheCacheLost(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Store("k", []byte("value"), time.Minute))
	// the cache drops the object on its own, as it does one it finds expired or corrupt
	mc.drop("k")
	b, s, err := idx.Retrieve("k")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, s)
	require.Nil(t, b)
	requireTotals(t, idx, 0, 0)
}

func TestRetrieveFailures(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Store("k", []byte("value"), time.Minute))
	accessed, _ := idx.Object("k")
	before := accessed.LastAccess()
	time.Sleep(time.Millisecond)

	mc.retrieveErr, mc.status = errFault, status.LookupStatusError
	b, s, err := idx.Retrieve("k")
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, s)
	require.Nil(t, b)

	mc.retrieveErr, mc.status = nil, status.LookupStatusKeyMiss
	b, s, err = idx.Retrieve("k")
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusKeyMiss, s)
	require.Nil(t, b)

	requireTotals(t, idx, 1, 5)
	require.Equal(t, before, accessed.LastAccess(), "neither was an access")
	idx.updateAccessTime("absent", time.Now())
}

func TestRemoveFailure(t *testing.T) {
	idx, mc := newPlainIndex(t, idleOpts())
	require.NoError(t, idx.Store("k", []byte("value"), time.Minute))
	mc.removeErr = errFault
	require.ErrorIs(t, idx.Remove("k"), errFault)
	requireTotals(t, idx, 0, 0)
	require.NoError(t, idx.Store("k", []byte("value"), time.Nanosecond))
	time.Sleep(time.Millisecond)
	idx.reapAt(time.Now().Add(time.Minute).UnixNano())
	requireTotals(t, idx, 0, 0)
}

func TestUpdateOptions(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	o := idleOpts()
	o.MaxSizeObjects = 7
	idx.UpdateOptions(o)
	require.Same(t, o, idx.options.Load())
}

func TestWorkersNotStarted(t *testing.T) {
	// a provider that needs its workers is warned of those its options leave out
	mc := newMapClient()
	idx := NewIndexedClient(testName, testProvider, &options.Options{}, mc, func(o *IndexedClientOptions) {
		o.NeedsFlushInterval, o.NeedsReapInterval = true, true
	})
	require.Nil(t, idx.journal, "the cache cannot hold the index's files")
	require.NoError(t, idx.Store("k", []byte("v"), time.Minute))
	idx.flushOnce()
	require.NoError(t, idx.Close())

	// a cache that can hold them, but says it does not
	idx = NewIndexedClient(testName, testProvider, idleOpts(), blob.NewClient(plainStore{blobtest.NewMemStore()}, "", ""))
	require.Nil(t, idx.journal)
	require.NoError(t, idx.Close())
}

// hides the optional capabilities of the Store it wraps
type plainStore struct{ blob.Store }

func TestWorkerPanicHandler(t *testing.T) {
	idx, _ := newPlainIndex(t, idleOpts())
	for worker, exited := range map[string]*atomic.Bool{
		workerFlusher: &idx.flusherExited, workerReaper: &idx.reaperExited, workerScanner: &idx.scannerExited,
	} {
		before := testutil.ToFloat64(metrics.CacheIndexPanicRecovered.WithLabelValues(worker))
		idx.workerPanicHandler(worker, exited)("boom", []byte("stack"))
		require.True(t, exited.Load())
		require.Equal(t, before+1, testutil.ToFloat64(metrics.CacheIndexPanicRecovered.WithLabelValues(worker)))
	}
}

// the index is written to, read from and reaped all at once, and its totals still add up
func TestConcurrentUse(t *testing.T) {
	o := idleOpts()
	o.MaxSizeObjects, o.MaxSizeBackoffObjects = 64, 8
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, o)
	const workers, rounds, keys = 8, 400, 128
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range rounds {
				key := "key-" + strconv.Itoa((w*31+i)%keys)
				switch i % 4 {
				case 0, 1:
					ttl := time.Duration(i%3) * time.Minute
					if err := idx.Store(key, make([]byte, 1+i%7), ttl); err != nil {
						t.Error(err)
					}
				case 2:
					idx.Retrieve(key)
				default:
					idx.Remove(key)
				}
			}
		})
	}
	wg.Go(func() {
		for range rounds / 10 {
			idx.reap()
			idx.flushOnce()
		}
	})
	wg.Wait()

	var count, size int64
	idx.each(func(o *Object) bool {
		count++
		size += o.Size()
		_, listed := idx.Object(o.Key)
		require.True(t, listed, o.Key)
		return true
	})
	requireTotals(t, idx, count, size)
	for _, key := range idx.Keys() {
		_, held := s.Frame(key)
		require.True(t, held, "%s is listed, and the cache does not hold it", key)
	}
}

func TestSplit(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := NewIndexedClient(testName, testProvider, idleOpts(), blob.NewClient(s, testName, testProvider))
	t.Cleanup(func() { idx.crash() })
	require.True(t, idx.SupportsSplit())
	require.NoError(t, idx.StoreSplit("k", []byte("meta"), []byte("body!"), time.Minute))
	requireTotals(t, idx, 1, 9)
	o, _ := idx.Object("k")
	before := o.LastAccess()
	time.Sleep(time.Millisecond)

	meta, body, st, err := idx.RetrieveSplit("k")
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, st)
	require.Equal(t, "meta", string(meta))
	require.Equal(t, "body!", string(body))
	require.True(t, o.LastAccess().After(before))

	require.NoError(t, s.Delete("k"))
	_, _, st, err = idx.RetrieveSplit("k")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, st)
	requireTotals(t, idx, 0, 0)

	require.ErrorIs(t, idx.StoreSplit(IndexKey, nil, nil, 0), ErrIndexInvalidCacheKey)
	_, _, _, err = idx.RetrieveSplit(IndexKey)
	require.ErrorIs(t, err, ErrIndexInvalidCacheKey)
	s.PutErr, s.OpenErr = errFault, errFault
	require.ErrorIs(t, idx.StoreSplit("k", nil, []byte("b"), 0), errFault)
	_, _, st, err = idx.RetrieveSplit("k")
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)
	requireTotals(t, idx, 0, 0)

	plain, _ := newPlainIndex(t, idleOpts())
	require.False(t, plain.SupportsSplit())
	require.ErrorIs(t, plain.StoreSplit("k", nil, nil, 0), ErrSplitUnsupported)
	_, _, st, err = plain.RetrieveSplit("k")
	require.ErrorIs(t, err, ErrSplitUnsupported)
	require.Equal(t, status.LookupStatusError, st)
}

func TestOpenSplit(t *testing.T) {
	plain, _ := newPlainIndex(t, idleOpts())
	require.False(t, plain.SupportsStream(), "the cache reads objects whole")
	_, _, st, err := plain.OpenSplit("k")
	require.ErrorIs(t, err, ErrSplitUnsupported)
	require.Equal(t, status.LookupStatusError, st)

	s := blobtest.NewMemStore()
	idx := NewIndexedClient(testName, testProvider, idleOpts(), blob.NewClient(s, testName, testProvider))
	t.Cleanup(func() { idx.crash() })
	require.True(t, idx.SupportsStream())
	require.NoError(t, idx.StoreSplit("k", []byte("meta"), []byte("body!"), time.Minute))
	o, _ := idx.Object("k")
	before := o.LastAccess()
	time.Sleep(time.Millisecond)

	meta, body, st, err := idx.OpenSplit("k")
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, st)
	require.Equal(t, "meta", string(meta))
	require.Equal(t, int64(5), body.Size())
	require.NoError(t, body.Close())
	require.True(t, o.LastAccess().After(before))

	_, _, _, err = idx.OpenSplit(IndexKey)
	require.ErrorIs(t, err, ErrIndexInvalidCacheKey)
	s.OpenErr = errFault
	_, _, st, err = idx.OpenSplit("k")
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)
	requireTotals(t, idx, 1, 9)
	s.OpenErr = nil
	require.NoError(t, s.Delete("k"))
	_, _, st, err = idx.OpenSplit("k")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, st)
	requireTotals(t, idx, 0, 0)
}

// retrievals of sections that are neither hits nor failures
type splitMissClient struct{ *mapClient }

func (splitMissClient) SupportsSplit() bool                                    { return true }
func (splitMissClient) StoreSplit(string, []byte, []byte, time.Duration) error { return nil }
func (splitMissClient) RetrieveSplit(string) ([]byte, []byte, status.LookupStatus, error) {
	return []byte("m"), []byte("b"), status.LookupStatusKeyMiss, nil
}

func TestSplitNonHit(t *testing.T) {
	idx := NewIndexedClient(testName, "memory", idleOpts(), splitMissClient{newMapClient()})
	t.Cleanup(func() { idx.Close() })
	meta, body, st, err := idx.RetrieveSplit("k")
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusKeyMiss, st)
	require.Nil(t, meta)
	require.Nil(t, body)
}
