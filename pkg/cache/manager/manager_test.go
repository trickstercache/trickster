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

package manager

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
)

func TestNewCache(t *testing.T) {
	opts := CacheOptions{
		UseIndex: true,
	}
	c := NewCache(nil, opts, nil)
	require.NotNil(t, c)
	require.Equal(t, opts, c.(*Manager).opts)
}

func TestManager(t *testing.T) {
	opts := CacheOptions{
		UseIndex: true,
	}
	cacheConfig := co.Options{Provider: "memory"}
	mc := memory.New("test", &cacheConfig)
	c := NewCache(mc, opts, &cacheConfig)

	t.Run("create/read", func(t *testing.T) {
		key := "foo"
		require.NoError(t, c.Store(key, []byte("bar"), 0))
		b, s, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("bar"), b)
	})

	t.Run("create/read/delete", func(t *testing.T) {
		key := "foo"
		require.NoError(t, c.Store(key, []byte("bar"), 0))
		b, s, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("bar"), b)
		require.NoError(t, c.Remove(key))
		b, s, err = c.Retrieve(key)
		require.ErrorContains(t, err, "key not found in cache")
		require.Equal(t, status.LookupStatusKeyMiss, s)
		require.Len(t, b, 0)
	})

	t.Run("create/update/read", func(t *testing.T) {
		key := "foo"
		require.NoError(t, c.Store(key, []byte("bar"), 0))
		b, s, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("bar"), b)
		require.NoError(t, c.Store(key, []byte("baz"), 0))
		b, s, err = c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, []byte("baz"), b)
	})

	t.Run("reference", func(t *testing.T) {
		mc := c.(cache.MemoryCache)
		key := "foo"
		val := object{"bar"}
		require.NoError(t, mc.StoreReference(key, &val, 0))
		v, s, err := mc.RetrieveReference(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, s)
		require.Equal(t, val, *v.(*object))
	})
}

type object struct {
	field string
}

func (o *object) Size() int {
	return len(o.field)
}

// blockingClient is a cache.MemoryCache stub whose Store/Retrieve/Remove
// block on a per-test gate, and whose Close records its call. It lets tests
// observe Manager.Close's drain semantics without racing on a real backend.
type blockingClient struct {
	gate     chan struct{} // closed to release in-flight ops
	closed   atomic.Int32
	closeErr error
	storeCnt atomic.Int32
}

func newBlockingClient() *blockingClient {
	return &blockingClient{gate: make(chan struct{})}
}

func (b *blockingClient) Connect() error { return nil }
func (b *blockingClient) Store(_ string, _ []byte, _ time.Duration) error {
	b.storeCnt.Add(1)
	<-b.gate
	return nil
}

func (b *blockingClient) Retrieve(_ string) ([]byte, status.LookupStatus, error) {
	<-b.gate
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

func (b *blockingClient) Remove(_ ...string) error {
	<-b.gate
	return nil
}

func (b *blockingClient) Close() error {
	b.closed.Add(1)
	return b.closeErr
}

func (b *blockingClient) StoreReference(_ string, _ cache.ReferenceObject, _ time.Duration) error {
	<-b.gate
	return nil
}

func (b *blockingClient) RetrieveReference(_ string) (any, status.LookupStatus, error) {
	<-b.gate
	return nil, status.LookupStatusKeyMiss, cache.ErrKNF
}

// TestManagerCloseRejectsAfterClose verifies that once Close() has begun
// draining, further Store/Retrieve/Remove return ErrCacheClosed and never
// invoke the underlying client. This is the reload-safety contract.
func TestManagerCloseRejectsAfterClose(t *testing.T) {
	t.Parallel()
	bc := newBlockingClient()
	cfg := &co.Options{Name: "n", Provider: "memory"}
	cm := NewCache(bc, CacheOptions{}, cfg).(*Manager)

	close(bc.gate) // never block real ops
	require.NoError(t, cm.Close())

	require.ErrorIs(t, cm.Store("k", []byte("v"), 0), ErrCacheClosed)
	_, _, err := cm.Retrieve("k")
	require.ErrorIs(t, err, ErrCacheClosed)
	require.ErrorIs(t, cm.Remove("k"), ErrCacheClosed)
	require.ErrorIs(t, cm.StoreReference("k", &object{}, 0), ErrCacheClosed)
	_, _, err = cm.RetrieveReference("k")
	require.ErrorIs(t, err, ErrCacheClosed)

	// underlying client.Store must not have been invoked by any of the above
	require.Zero(t, bc.storeCnt.Load(), "post-Close ops must not reach underlying client")
}

// TestManagerCloseWaitsForInflight verifies that Close blocks until in-flight
// ops complete, then closes the underlying client.
func TestManagerCloseWaitsForInflight(t *testing.T) {
	t.Parallel()
	bc := newBlockingClient()
	cfg := &co.Options{Name: "n", Provider: "memory"}
	cm := NewCache(bc, CacheOptions{}, cfg).(*Manager)

	storeErr := make(chan error, 1)
	go func() { storeErr <- cm.Store("k", []byte("v"), 0) }()

	// Spin until acquire() has bumped inflight so Close races the in-flight
	// op rather than running before it starts.
	deadline := time.Now().Add(500 * time.Millisecond)
	for bc.storeCnt.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("in-flight Store never reached underlying client")
		}
		time.Sleep(time.Millisecond)
	}

	closeErr := make(chan error, 1)
	go func() { closeErr <- cm.Close() }()

	// Close must not have completed yet.
	select {
	case <-closeErr:
		t.Fatal("Close returned before in-flight op released")
	case <-time.After(50 * time.Millisecond):
	}

	close(bc.gate) // release the in-flight Store

	require.NoError(t, <-storeErr)
	require.NoError(t, <-closeErr)
	require.Equal(t, int32(1), bc.closed.Load())
}

// TestManagerCloseTimeoutProceeds verifies that if inflight never drains,
// Close still closes the underlying client after the timeout elapses.
func TestManagerCloseTimeoutProceeds(t *testing.T) {
	t.Parallel()
	bc := newBlockingClient()
	cfg := &co.Options{Name: "n", Provider: "memory"}
	cm := NewCache(bc, CacheOptions{}, cfg).(*Manager)
	cm.SetCloseDrainTimeout(50 * time.Millisecond)

	// Launch a Store that will never release (gate stays open until test
	// shutdown). Block its caller side too with the same channel.
	go func() { _ = cm.Store("k", []byte("v"), 0) }()
	deadline := time.Now().Add(500 * time.Millisecond)
	for bc.storeCnt.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("stuck Store never reached underlying client")
		}
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	require.NoError(t, cm.Close())
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 50*time.Millisecond,
		"Close returned before drain timeout elapsed (%v)", elapsed)
	require.Less(t, elapsed, 5*time.Second,
		"Close exceeded reasonable upper bound (%v) -- did it ignore SetCloseDrainTimeout?", elapsed)
	require.Equal(t, int32(1), bc.closed.Load(),
		"underlying Close must run even when drain times out")

	// Release the stuck goroutine so the test process exits cleanly.
	close(bc.gate)
}

// TestManagerCloseIdempotent verifies that calling Close twice does not
// re-drain, double-invoke the WaitGroup, or block on the second call.
func TestManagerCloseIdempotent(t *testing.T) {
	t.Parallel()
	bc := newBlockingClient()
	bc.closeErr = errors.New("first close")
	cfg := &co.Options{Name: "n", Provider: "memory"}
	cm := NewCache(bc, CacheOptions{}, cfg).(*Manager)

	close(bc.gate)
	require.EqualError(t, cm.Close(), "first close")
	// second call short-circuits the drain and forwards to the client only
	bc.closeErr = errors.New("second close")
	require.EqualError(t, cm.Close(), "second close")
	require.Equal(t, int32(2), bc.closed.Load())
}

// TestManagerCloseConcurrent verifies that concurrent callers racing with
// Close all see a defined outcome -- either ErrCacheClosed or success -- and
// that no in-flight Add fires after the WaitGroup has begun Wait. This is
// the case the mu+closing pattern is meant to make safe; without it, a race
// between acquire's Add(1) and Close's Wait can panic with
// "sync: WaitGroup misuse: Add called concurrently with Wait".
func TestManagerCloseConcurrent(t *testing.T) {
	t.Parallel()
	bc := newBlockingClient()
	close(bc.gate) // ops complete immediately
	cfg := &co.Options{Name: "n", Provider: "memory"}
	cm := NewCache(bc, CacheOptions{}, cfg).(*Manager)

	const N = 64
	var wg sync.WaitGroup
	errs := make(chan error, N)
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			errs <- cm.Store("k", []byte("v"), 0)
		}()
	}
	// Fire Close in the middle of the storm.
	closeErrCh := make(chan error, 1)
	go func() { closeErrCh <- cm.Close() }()

	wg.Wait()
	close(errs)
	require.NoError(t, <-closeErrCh)
	for err := range errs {
		if err != nil && !errors.Is(err, ErrCacheClosed) {
			t.Fatalf("unexpected error from concurrent Store: %v", err)
		}
	}
}
