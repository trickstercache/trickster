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
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/filesystem"
	fso "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestIndexedClient(t *testing.T) {
	t.Run("basic", func(t *testing.T) {
		const provider = "filesystem"

		// init filesystem cache client
		cacheConfig := co.Options{
			Provider: provider,
			Filesystem: &fso.Options{
				CachePath: t.TempDir(),
			},
		}
		fsc := filesystem.NewCache("test", &cacheConfig)
		// init indexed client
		ic := NewIndexedClient("test", provider, &options.Options{
			ReapInterval:          timeconv.Duration(time.Second * time.Duration(10)),
			FlushInterval:         timeconv.Duration(time.Second * time.Duration(10)),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
			IndexExpiry:           timeconv.Duration(1 * time.Hour),
		}, fsc)
		t.Log("basic")
		state := getIndexedClientState(ic)
		require.Equal(t, int64(0), state.ObjectCount)
		require.Equal(t, int64(0), state.CacheSize)
		require.Len(t, state.Objects, 0)

		// retrieve non-existent key
		key := "foo"
		b, s, err := ic.Retrieve(key)
		require.ErrorContains(t, err, "key not found in cache")
		require.Equal(t, status.LookupStatusKeyMiss, s)
		require.Len(t, b, 0)

		// store & retrieve
		val := []byte("bar")
		ttl := 60 * time.Second
		require.NoError(t, ic.Store(key, val, ttl))

		state = getIndexedClientState(ic)
		require.Equal(t, int64(1), state.ObjectCount)
		require.Equal(t, int64(3), state.CacheSize)
		require.Len(t, state.Objects, 1)

		b, s, err = ic.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, val, b)

		// trigger reap & expect no change
		ic.reap()
		state = getIndexedClientState(ic)
		require.Equal(t, int64(1), state.ObjectCount)
		require.Equal(t, int64(3), state.CacheSize)
		require.Len(t, state.Objects, 1)

		// clear, expect empty state
		ic.Clear()
		state = getIndexedClientState(ic)
		require.Equal(t, int64(0), state.ObjectCount)
		require.Equal(t, int64(0), state.CacheSize)
		require.Len(t, state.Objects, 0)
		// require.Equal(t)
		require.NoError(t, ic.Close())
	})

	t.Run("atime", func(t *testing.T) {
		const provider = "filesystem"

		// init filesystem cache client
		cacheConfig := co.Options{
			Provider: provider,
			Filesystem: &fso.Options{
				CachePath: t.TempDir(),
			},
		}
		fsc := filesystem.NewCache("test", &cacheConfig)
		// init indexed client
		ic := NewIndexedClient("test", provider, &options.Options{
			ReapInterval:          timeconv.Duration(time.Second * time.Duration(10)),
			FlushInterval:         timeconv.Duration(time.Second * time.Duration(10)),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
			IndexExpiry:           timeconv.Duration(1 * time.Hour),
		}, fsc)
		t.Cleanup(func() { _ = ic.Close() })

		// store & retrieve
		val := []byte("bar")
		ttl := 60 * time.Second
		require.NoError(t, ic.Store("foo", val, ttl))
		// expect atime to be set
		o, ok := ic.Objects.Load("foo")
		require.True(t, ok)
		obj, ok := o.(*Object)
		require.True(t, ok)
		atime := obj.LastAccess.Load()
		require.NotZero(t, atime)

		// access the object and expect atime to be updated
		b, s, err := ic.Retrieve("foo")
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, val, b)
		atime2 := obj.LastAccess.Load()
		require.NotZero(t, atime2)
		require.True(t, atime2.After(atime), "expected %s to be after %s", atime2, atime)
	})

	t.Run("flush", func(t *testing.T) {
		const provider = "filesystem"

		// init memory cache client
		cacheConfig := co.Options{
			Provider: provider,
			Filesystem: &fso.Options{
				CachePath: t.TempDir(),
			},
		}
		fs := filesystem.NewCache("test", &cacheConfig)
		ic := NewIndexedClient("test", provider, &options.Options{
			ReapInterval:          timeconv.Duration(time.Second * time.Duration(10)),
			FlushInterval:         timeconv.Duration(time.Second * time.Duration(10)),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
			IndexExpiry:           timeconv.Duration(1 * time.Hour),
		}, fs, func(ico *IndexedClientOptions) {
			ico.NeedsFlushInterval = true
			ico.NeedsReapInterval = true
		})

		// write a key and trigger a flush
		ttl := 60 * time.Second
		require.NoError(t, ic.Store("test.1", []byte("test_value"), ttl))
		ic.flushOnce()

		// look up the cache key, expect an error
		_, s, err := ic.Retrieve(IndexKey)
		require.Equal(t, status.LookupStatusError, s)
		require.ErrorAs(t, err, &ErrIndexInvalidCacheKey)

		// use the internal client to retrieve the key
		b, s, err := ic.Client.Retrieve(IndexKey)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		t.Log(string(b))
		// close the cache
		ic.Close()

		// start a new cache, verify it reuses the index
		ic = NewIndexedClient("test", provider, &options.Options{
			ReapInterval:          timeconv.Duration(time.Second * time.Duration(10)),
			FlushInterval:         timeconv.Duration(time.Second * time.Duration(10)),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
			IndexExpiry:           timeconv.Duration(1 * time.Hour),
		}, fs, func(ico *IndexedClientOptions) {
			ico.NeedsFlushInterval = true
			ico.NeedsReapInterval = true
		})
		t.Cleanup(func() { _ = ic.Close() })
		// look up the index key, expect a hit
		b2, s, err := ic.Client.Retrieve(IndexKey)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, b, b2)

		// inspect the index and expect keys
		keys := ic.Objects.Keys()
		require.Len(t, keys, 1)
		require.Equal(t, "test.1", keys[0])

		// expect that we can look up test.1
		_, s, err = ic.Retrieve("test.1")
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)

		t.Run("flush loop", func(t *testing.T) {
			// test the actual flush loop by forcing it to flush, this utilizes goroutines
			// and should detect more potential race conditions vs the existing flush tests that use
			// flush internal methods

			// Create fresh filesystem cache for this subtest
			freshCacheConfig := co.Options{
				Provider: provider,
				Filesystem: &fso.Options{
					CachePath: t.TempDir(),
				},
			}
			freshFs := filesystem.NewCache("flushTest", &freshCacheConfig)
			ttl := 60 * time.Second
			ic1 := NewIndexedClient("flushTest", provider, &options.Options{
				ReapInterval:          timeconv.Duration(time.Second * 60 * 60 * 24),
				FlushInterval:         timeconv.Duration(time.Second * 60 * 60 * 24),
				MaxSizeObjects:        5,
				MaxSizeBackoffObjects: 3,
				MaxSizeBytes:          100,
				MaxSizeBackoffBytes:   30,
				IndexExpiry:           timeconv.Duration(1 * time.Hour),
			}, freshFs)
			defer ic1.Close()
			for i := range 5 {
				index := fmt.Sprintf("%d", i)
				key := "key." + index
				require.NoError(t, ic1.Store(key, []byte("value1."+index), ttl))
			}
			_, s, err := ic1.Client.Retrieve(IndexKey)
			require.Equal(t, cache.ErrKNF, err)
			require.Equal(t, status.LookupStatusKeyMiss, s)
			ic1.forceFlush <- true
			<-ic1.hasFlushed
			_, s, err = ic1.Client.Retrieve(IndexKey)
			require.NoError(t, err)
			require.Equal(t, status.LookupStatusHit, s)
		})

		t.Run("reap loop", func(t *testing.T) {
			// test the actual reap loop by forcing it to reap, this utilizes goroutines
			// and should detect more potential race conditions vs the existing reap tests that use
			// reap internal methods

			// Create fresh filesystem cache for this subtest
			freshCacheConfig := co.Options{
				Provider: provider,
				Filesystem: &fso.Options{
					CachePath: t.TempDir(),
				},
			}
			freshFs := filesystem.NewCache("reapTest", &freshCacheConfig)
			ttl := 60 * time.Second
			ic2 := NewIndexedClient("reapTest", provider, &options.Options{
				ReapInterval:          timeconv.Duration(time.Second * 60 * 60 * 24),
				FlushInterval:         timeconv.Duration(time.Second * 60 * 60 * 24),
				MaxSizeObjects:        5,
				MaxSizeBackoffObjects: 5,
				MaxSizeBytes:          10000,
				MaxSizeBackoffBytes:   300,
				IndexExpiry:           timeconv.Duration(1 * time.Hour),
			}, freshFs)
			defer ic2.Close()

			// write 5 objects, expect 5 objects
			for i := range 5 {
				index := fmt.Sprintf("%d", i)
				key := "key." + index
				require.NoError(t, ic2.Store(key, []byte("value1."+index), ttl))
			}
			state := getIndexedClientState(ic2)
			require.Equal(t, int64(5), state.ObjectCount)
			require.Len(t, state.Objects, 5)
			// force reap, expect 5 objects still
			ic2.forceReap <- true
			<-ic2.hasReaped
			state = getIndexedClientState(ic2)
			require.Equal(t, int64(5), state.ObjectCount)
			require.Len(t, state.Objects, 5)

			// write more objects, then force reap to trigger (count based) eviction
			for i := range 5 {
				index := fmt.Sprintf("%d", i)
				key := "another.key." + index
				require.NoError(t, ic2.Store(key, []byte("value1."+index), ttl))
			}
			state = getIndexedClientState(ic2)
			require.Equal(t, int64(10), state.ObjectCount)
			require.Equal(t, len(state.Objects), 10)
			// force reap, expect some evictions (back to the MaxSizeObjects count)
			evictions := metrics.CacheEvents.WithLabelValues("reapTest", provider,
				"eviction", "size_objects")
			evictionsBefore := testutil.ToFloat64(evictions)
			ic2.forceReap <- true
			<-ic2.hasReaped
			state = getIndexedClientState(ic2)
			require.Equal(t, int64(5), state.ObjectCount)
			require.Equal(t, len(state.Objects), 5)
			require.Equal(t, evictionsBefore+1, testutil.ToFloat64(evictions))
		})
	})

	t.Run("reap eviction", func(t *testing.T) {
		const provider = "filesystem"

		// init filesystem cache client
		cacheConfig := co.Options{
			Provider: provider,
			Filesystem: &fso.Options{
				CachePath: t.TempDir(),
			},
		}
		fsc := filesystem.NewCache("test", &cacheConfig)
		// init indexed client
		ic := NewIndexedClient("test", provider, &options.Options{
			ReapInterval:          timeconv.Duration(time.Second * time.Duration(10)),
			FlushInterval:         timeconv.Duration(time.Second * time.Duration(10)),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
			IndexExpiry:           timeconv.Duration(1 * time.Hour),
		}, fsc, func(ico *IndexedClientOptions) {
			ico.NeedsFlushInterval = true
			ico.NeedsReapInterval = true
		})
		t.Cleanup(func() { _ = ic.Close() })
		ttl := 60 * time.Second

		// add expired key to cover the case that the reaper remove it
		ic.Store("test.1", []byte("test_value"), ttl)

		// add key with no expiration which should not be reaped
		ic.Store("test.2", []byte("test_value"), ttl)

		// add key with future expiration which should not be reaped
		ic.Store("test.3", []byte("test_value"), ttl)

		// trigger a reap that will only remove expired elements but not size down the full cache
		keyCount := len(ic.Objects.Keys())
		ic.reap()
		require.Equal(t, keyCount, len(ic.Objects.Keys()))

		state := getIndexedClientState(ic)
		require.Equal(t, int64(3), state.ObjectCount)
		require.Equal(t, int64(30), state.CacheSize)
		require.Len(t, state.Objects, 3)

		// add key with future expiration which should not be reaped
		ic.Store("test.4", []byte("test_value"), ttl)

		// add key with future expiration which should not be reaped
		ic.Store("test.5", []byte("test_value"), ttl)

		// add key with future expiration which should not be reaped
		ic.Store("test.6", []byte("test_value"), ttl)

		// trigger size-based reap eviction of some elements
		keyCount = len(ic.Objects.Keys())
		require.Equal(t, 6, keyCount)
		ic.reap()

		_, ok := ic.Objects.Load("test.1")
		require.False(t, ok, "expected key %s to be missing", "test.1")

		_, ok = ic.Objects.Load("test.2")
		require.False(t, ok, "expected key test.2 to be missing")

		_, ok = ic.Objects.Load("test.3")
		require.False(t, ok, "expected key test.3 to be missing")

		_, ok = ic.Objects.Load("test.4")
		require.False(t, ok, "expected key test.4 to be missing")

		_, ok = ic.Objects.Load("test.5")
		require.True(t, ok, "expected key test.5 to be present")

		_, ok = ic.Objects.Load("test.6")
		require.True(t, ok, "expected key test.6 to be present")

		// add key with large body to reach byte size threshold
		ic.Store("test.7", []byte("test_value00000000000000000000000000000000000000000000000000000000000000000000000000000"), ttl)

		// trigger a byte-based reap
		keyCount = len(ic.Objects.Keys())
		require.Equal(t, 3, keyCount)
		ic.reap()
		require.Len(t, ic.Objects.Keys(), 0)

		// expect index to be empty
		objects := ic.Objects.ToObjects()
		require.Len(t, objects, 0)
		state = getIndexedClientState(ic)
		require.Len(t, state.Objects, 0)
		require.Equal(t, int64(0), state.ObjectCount)
		require.Equal(t, int64(0), state.CacheSize)
	})
}

type indexedClientState struct {
	ObjectCount int64
	CacheSize   int64
	Objects     Objects
}

func getIndexedClientState(ic *IndexedClient) *indexedClientState {
	return &indexedClientState{
		ObjectCount: atomic.LoadInt64(&ic.ObjectCount),
		CacheSize:   atomic.LoadInt64(&ic.CacheSize),
		Objects:     ic.Objects.ToObjects(),
	}
}

type testRefObject struct {
	n int
}

func (r *testRefObject) Size() int { return r.n }

// mapClient is a simple in-memory cache.Client for index unit tests.
type mapClient struct {
	data      map[string][]byte
	storeErr  error
	removeErr error
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
	m.data[key] = append([]byte(nil), b...)
	return nil
}

func (m *mapClient) Retrieve(key string) ([]byte, status.LookupStatus, error) {
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
	for _, k := range keys {
		delete(m.data, k)
	}
	return nil
}

// customRetrieveClient lets tests control Retrieve responses.
type customRetrieveClient struct {
	*mapClient
	retrieveFn func(string) ([]byte, status.LookupStatus, error)
}

func (c *customRetrieveClient) Retrieve(key string) ([]byte, status.LookupStatus, error) {
	if c.retrieveFn != nil {
		return c.retrieveFn(key)
	}
	return c.mapClient.Retrieve(key)
}

type errMemoryClient struct {
	*mapClient
	storeRefErr error
}

func (e *errMemoryClient) StoreReference(string, cache.ReferenceObject, time.Duration) error {
	return e.storeRefErr
}

func (e *errMemoryClient) RetrieveReference(string) (any, status.LookupStatus, error) {
	return nil, status.LookupStatusError, e.storeRefErr
}

func defaultIndexOpts() *options.Options {
	return &options.Options{
		ReapInterval:          timeconv.Duration(time.Hour),
		FlushInterval:         timeconv.Duration(time.Hour),
		MaxSizeObjects:        100,
		MaxSizeBackoffObjects: 10,
		MaxSizeBytes:          1 << 20,
		MaxSizeBackoffBytes:   1024,
		IndexExpiry:           timeconv.Duration(time.Hour),
	}
}

func TestConnectAndUpdateOptions(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.NoError(t, ic.Connect())

	o := defaultIndexOpts()
	o.MaxSizeObjects = 42
	ic.UpdateOptions(o)
	require.Equal(t, int64(42), ic.options.Load().(*options.Options).MaxSizeObjects)
}

func TestStoreAndRetrieveIndexKeyRejected(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.ErrorIs(t, ic.Store(IndexKey, []byte("x"), time.Second), ErrIndexInvalidCacheKey)

	b, s, err := ic.Retrieve(IndexKey)
	require.Nil(t, b)
	require.Equal(t, status.LookupStatusError, s)
	require.ErrorIs(t, err, ErrIndexInvalidCacheKey)
}

func TestStoreClientError(t *testing.T) {
	mc := newMapClient()
	mc.storeErr = errors.New("store failed")
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.ErrorContains(t, ic.Store("k", []byte("v"), time.Second), "store failed")
}

func TestStoreUpdateExistingKey(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.NoError(t, ic.Store("k", []byte("ab"), time.Second))
	state := getIndexedClientState(ic)
	require.Equal(t, int64(1), state.ObjectCount)
	require.Equal(t, int64(2), state.CacheSize)

	require.NoError(t, ic.Store("k", []byte("abcd"), time.Second))
	state = getIndexedClientState(ic)
	require.Equal(t, int64(1), state.ObjectCount)
	require.Equal(t, int64(4), state.CacheSize)
}

func TestRetrieveCorruptObject(t *testing.T) {
	mc := newMapClient()
	require.NoError(t, mc.Store("k", []byte("not-msgpack"), 0))
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	b, s, err := ic.Retrieve("k")
	require.Nil(t, b)
	require.Equal(t, status.LookupStatusError, s)
	require.Error(t, err)
}

func TestRetrieveNonHitStatus(t *testing.T) {
	mc := &customRetrieveClient{
		mapClient: newMapClient(),
		retrieveFn: func(string) ([]byte, status.LookupStatus, error) {
			return nil, status.LookupStatusPartialHit, nil
		},
	}
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	b, s, err := ic.Retrieve("k")
	require.Nil(t, b)
	require.Equal(t, status.LookupStatusPartialHit, s)
	require.NoError(t, err)
}

func TestUpdateAccessTimeMissingKey(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	// should no-op without panicking when the key is absent from the index
	ic.updateAccessTime("missing")
}

func TestStoreRetrieveReference(t *testing.T) {
	mem := memory.New("test", &co.Options{Provider: "memory"})
	ic := NewIndexedClient("test", "memory", defaultIndexOpts(), mem)
	t.Cleanup(func() { _ = ic.Close() })

	ref := &testRefObject{n: 7}
	require.NoError(t, ic.StoreReference("ref", ref, time.Minute))
	state := getIndexedClientState(ic)
	require.Equal(t, int64(1), state.ObjectCount)
	require.Equal(t, int64(7), state.CacheSize)

	v, s, err := ic.RetrieveReference("ref")
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, s)
	require.Equal(t, ref, v)

	// zero TTL still stores
	require.NoError(t, ic.StoreReference("ref2", &testRefObject{n: 3}, 0))
}

func TestStoreRetrieveReferenceErrors(t *testing.T) {
	t.Run("index key", func(t *testing.T) {
		mem := memory.New("test", &co.Options{Provider: "memory"})
		ic := NewIndexedClient("test", "memory", defaultIndexOpts(), mem)
		t.Cleanup(func() { _ = ic.Close() })

		require.ErrorIs(t, ic.StoreReference(IndexKey, &testRefObject{n: 1}, 0), ErrIndexInvalidCacheKey)
		v, s, err := ic.RetrieveReference(IndexKey)
		require.Nil(t, v)
		require.Equal(t, status.LookupStatusError, s)
		require.ErrorIs(t, err, ErrIndexInvalidCacheKey)
	})

	t.Run("non-memory backend", func(t *testing.T) {
		mc := newMapClient()
		ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
		t.Cleanup(func() { _ = ic.Close() })

		require.ErrorIs(t, ic.StoreReference("k", &testRefObject{n: 1}, 0), ErrInvalidCacheBackend)
		v, s, err := ic.RetrieveReference("k")
		require.Nil(t, v)
		require.Equal(t, status.LookupStatusError, s)
		require.ErrorIs(t, err, ErrInvalidCacheBackend)
	})

	t.Run("backend store error", func(t *testing.T) {
		backend := &errMemoryClient{
			mapClient:   newMapClient(),
			storeRefErr: errors.New("ref store failed"),
		}
		ic := NewIndexedClient("test", "memory", defaultIndexOpts(), backend)
		t.Cleanup(func() { _ = ic.Close() })

		require.ErrorContains(t, ic.StoreReference("k", &testRefObject{n: 1}, 0), "ref store failed")
	})
}

func TestNewIndexedClientWarningsAndLoad(t *testing.T) {
	t.Run("needs intervals without intervals", func(t *testing.T) {
		mc := newMapClient()
		ic := NewIndexedClient("test", "map", &options.Options{
			IndexExpiry: timeconv.Duration(time.Hour),
		}, mc, func(ico *IndexedClientOptions) {
			ico.NeedsFlushInterval = true
			ico.NeedsReapInterval = true
		})
		require.NoError(t, ic.Close())
	})

	t.Run("oversized index discarded", func(t *testing.T) {
		prev := maxIndexBytes
		maxIndexBytes = 8
		t.Cleanup(func() { maxIndexBytes = prev })

		mc := newMapClient()
		require.NoError(t, mc.Store(IndexKey, []byte("0123456789"), 0))
		ic := NewIndexedClient("test", "map", &options.Options{
			FlushInterval: timeconv.Duration(time.Hour),
			IndexExpiry:   timeconv.Duration(time.Hour),
		}, mc)
		t.Cleanup(func() { _ = ic.Close() })
		require.Equal(t, int64(0), atomic.LoadInt64(&ic.ObjectCount))
	})

	t.Run("expired index cleared", func(t *testing.T) {
		mc := newMapClient()
		seed := &IndexedClient{CacheSize: 9, ObjectCount: 1}
		seed.LastFlush.Store(time.Now().Add(-2 * time.Hour))
		seed.Objects.Store("old", &Object{Key: "old", Size: 9})
		b, err := seed.MarshalMsg(nil)
		require.NoError(t, err)
		require.NoError(t, mc.Store(IndexKey, b, 0))

		ic := NewIndexedClient("test", "map", &options.Options{
			FlushInterval: timeconv.Duration(time.Hour),
			IndexExpiry:   timeconv.Duration(time.Minute),
		}, mc)
		t.Cleanup(func() { _ = ic.Close() })
		require.Equal(t, int64(0), atomic.LoadInt64(&ic.ObjectCount))
		require.Equal(t, int64(0), atomic.LoadInt64(&ic.CacheSize))
		require.Empty(t, ic.Objects.Keys())
	})
}

func TestCloseFlushesWhenNeeded(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	require.NoError(t, ic.Store("k", []byte("v"), time.Minute))
	ic.ico.NeedsFlushInterval = true
	require.NoError(t, ic.Close())

	_, s, err := mc.Retrieve(IndexKey)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, s)
}

func TestFlusherTimerSkipAndSignalDrop(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", &options.Options{
		ReapInterval:   timeconv.Duration(time.Hour),
		FlushInterval:  timeconv.Duration(20 * time.Millisecond),
		IndexExpiry:    timeconv.Duration(time.Hour),
		MaxSizeBytes:   1 << 20,
		MaxSizeObjects: 100,
	}, mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.NoError(t, ic.Store("k", []byte("v"), time.Minute))
	ic.flushOnce() // LastFlush after lastWrite → timer path should skip flushOnce

	// Allow the timer branch (and skip) to run at least once.
	time.Sleep(60 * time.Millisecond)

	// Fill hasFlushed buffer then force another flush to exercise the drop path.
	select {
	case ic.hasFlushed <- true:
	default:
	}
	ic.forceFlush <- true
	time.Sleep(20 * time.Millisecond)
}

func TestReaperTimerAndSignalDrop(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", &options.Options{
		ReapInterval:   timeconv.Duration(20 * time.Millisecond),
		FlushInterval:  timeconv.Duration(time.Hour),
		IndexExpiry:    timeconv.Duration(time.Hour),
		MaxSizeBytes:   1 << 20,
		MaxSizeObjects: 100,
	}, mc)
	t.Cleanup(func() { _ = ic.Close() })

	time.Sleep(60 * time.Millisecond)

	select {
	case ic.hasReaped <- true:
	default:
	}
	ic.forceReap <- true
	time.Sleep(20 * time.Millisecond)
}

func TestReapTTLExpiration(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.NoError(t, ic.Store("keep", []byte("v"), time.Hour))
	require.NoError(t, ic.Store("expire", []byte("v"), time.Hour))

	o, ok := ic.Objects.Load("expire")
	require.True(t, ok)
	o.(*Object).Expiration.Store(time.Now().Add(-time.Minute))

	ic.reap()
	_, ok = ic.Objects.Load("expire")
	require.False(t, ok)
	_, ok = ic.Objects.Load("keep")
	require.True(t, ok)
}

func TestReapRemoveErrors(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", &options.Options{
		ReapInterval:          timeconv.Duration(time.Hour),
		FlushInterval:         timeconv.Duration(time.Hour),
		MaxSizeObjects:        1,
		MaxSizeBackoffObjects: 0,
		MaxSizeBytes:          1 << 20,
		MaxSizeBackoffBytes:   0,
		IndexExpiry:           timeconv.Duration(time.Hour),
	}, mc)
	t.Cleanup(func() { _ = ic.Close() })

	require.NoError(t, ic.Store("a", []byte("1"), time.Hour))
	require.NoError(t, ic.Store("b", []byte("2"), time.Hour))

	o, ok := ic.Objects.Load("a")
	require.True(t, ok)
	o.(*Object).Expiration.Store(time.Now().Add(-time.Minute))

	mc.removeErr = errors.New("remove failed")
	ic.reap() // TTL removal error path
	mc.removeErr = nil

	// size-based eviction error path
	require.NoError(t, ic.Store("c", []byte("3"), time.Hour))
	require.NoError(t, ic.Store("d", []byte("4"), time.Hour))
	mc.removeErr = errors.New("remove failed")
	ic.reap()
}

func TestWorkerPanicHandler(t *testing.T) {
	mc := newMapClient()
	ic := NewIndexedClient("test", "map", defaultIndexOpts(), mc)
	t.Cleanup(func() { _ = ic.Close() })

	var exited atomic.Bool
	h := ic.workerPanicHandler("flusher", &exited)
	h("boom", []byte("stack"))
	require.True(t, exited.Load())
}

func TestObjectsAtimeSortInterface(t *testing.T) {
	o := objectsAtime{
		&Object{Key: "b", LastAccess: *atomicx.NewTime(time.Unix(2, 0))},
		&Object{Key: "a", LastAccess: *atomicx.NewTime(time.Unix(1, 0))},
		&Object{Key: "c", LastAccess: *atomicx.NewTime(time.Unix(3, 0))},
	}
	require.Equal(t, 3, o.Len())
	require.True(t, o.Less(1, 0))
	o.Swap(0, 1)
	require.Equal(t, "a", o[0].Key)
	sort.Sort(o)
	require.Equal(t, "a", o[0].Key)
	require.Equal(t, "b", o[1].Key)
	require.Equal(t, "c", o[2].Key)
}
