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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/cache/filesystem"
	fso "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

func TestIndexedClient(t *testing.T) {
	const provider = "memory"

	// init memory cache client
	cacheConfig := co.Options{Provider: provider}
	mc := memory.New("test", &cacheConfig)

	t.Run("basic", func(t *testing.T) {
		// init indexed client
		ic := NewIndexedClient("test", provider, []byte{}, &options.Options{
			ReapInterval:          time.Second * time.Duration(10),
			FlushInterval:         time.Second * time.Duration(10),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
		}, mc)
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
		require.NoError(t, ic.Store(key, val, 0))

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

		// init indexed client
		ic := NewIndexedClient("test", provider, []byte{}, &options.Options{
			ReapInterval:          time.Second * time.Duration(10),
			FlushInterval:         time.Second * time.Duration(10),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
		}, mc)

		// store & retrieve
		val := []byte("bar")
		require.NoError(t, ic.Store("foo", val, 0))
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
		ic := NewIndexedClient("test", provider, []byte{}, &options.Options{
			ReapInterval:          time.Second * time.Duration(10),
			FlushInterval:         time.Second * time.Duration(10),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
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
		ic = NewIndexedClient("test", provider, []byte{}, &options.Options{
			ReapInterval:          time.Second * time.Duration(10),
			FlushInterval:         time.Second * time.Duration(10),
			MaxSizeObjects:        5,
			MaxSizeBackoffObjects: 3,
			MaxSizeBytes:          100,
			MaxSizeBackoffBytes:   30,
		}, fs, func(ico *IndexedClientOptions) {
			ico.NeedsFlushInterval = true
			ico.NeedsReapInterval = true
		})
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
	})

	/* converting */
	// init indexed client
	ic := NewIndexedClient("test", provider, []byte{}, &options.Options{
		ReapInterval:          time.Second * time.Duration(10),
		FlushInterval:         time.Second * time.Duration(10),
		MaxSizeObjects:        5,
		MaxSizeBackoffObjects: 3,
		MaxSizeBytes:          100,
		MaxSizeBackoffBytes:   30,
	}, mc, func(ico *IndexedClientOptions) {
		ico.NeedsFlushInterval = true
		ico.NeedsReapInterval = true
	})
	t.Log("wip, converting prior test")
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
