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

package redis

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
)

func TestNilHolder(t *testing.T) {
	var h *Holder
	_, err := h.Client()
	require.ErrorIs(t, err, ErrNotConnected)
	h.close()
}

func TestHolderConnectsWhenTheClusterComesUp(t *testing.T) {
	// a free address, which nothing serves until the test starts a server there
	s := miniredis.RunT(t)
	addr := s.Addr()
	s.Close()

	rc := New(context.Background(), "test", &co.Options{Provider: cp.Redis, ProviderID: cp.RedisID,
		Redis: &ro.Options{
			ClientType: ro.ClientTypeCluster, Endpoints: []string{addr},
			MinRetryBackoff: timeconv.Duration(5 * time.Millisecond),
			MaxRetryBackoff: timeconv.Duration(20 * time.Millisecond),
		}})
	require.Error(t, rc.Connect())
	require.NotNil(t, rc.Holder())
	require.ErrorIs(t, rc.Store(cacheKey, []byte("data"), time.Minute), ErrNotConnected)

	up := miniredis.NewMiniRedis()
	require.NoError(t, up.StartAddr(addr))
	defer up.Close()
	require.Eventually(t, func() bool {
		_, err := rc.Holder().Client()
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, rc.Store(cacheKey, []byte("data"), time.Minute))
	require.NoError(t, rc.Close())
}

func TestHolderCloseStopsRetrying(t *testing.T) {
	var attempts atomic.Int32
	h := newHolder(valkey.ClientOption{}, time.Millisecond, 2*time.Millisecond, nil)
	h.newClient = func(valkey.ClientOption) (valkey.Client, error) {
		attempts.Add(1)
		return nil, errors.New("unreachable")
	}
	require.Error(t, h.connect())
	require.Eventually(t, func() bool { return attempts.Load() > 2 }, time.Second, time.Millisecond)
	h.close()
	n := attempts.Load()
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, n, attempts.Load())
	h.close()
}

func TestHolderRetryDiscardsAFailedClient(t *testing.T) {
	s := miniredis.RunT(t)
	real, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{s.Addr()}, ForceSingleClient: true,
		DisableCache: true})
	require.NoError(t, err)
	var attempts atomic.Int32
	var connected atomic.Bool
	h := newHolder(valkey.ClientOption{}, time.Millisecond, 2*time.Millisecond,
		func(valkey.Client) { connected.Store(true) })
	h.newClient = func(valkey.ClientOption) (valkey.Client, error) {
		switch attempts.Add(1) {
		case 1:
			return nil, errors.New("unreachable")
		case 2:
			// a client returned with an error is closed, not kept
			c, _ := valkey.NewClient(valkey.ClientOption{InitAddress: []string{s.Addr()}, ForceSingleClient: true,
				DisableCache: true})
			return c, errors.New("half made")
		}
		return real, nil
	}
	require.Error(t, h.connect())
	require.Eventually(t, connected.Load, time.Second, time.Millisecond)
	c, err := h.Client()
	require.NoError(t, err)
	require.Same(t, real, c)
	h.close()
}

func TestHolderSingleClientAtStart(t *testing.T) {
	// a single-server client is kept even when its first dial fails, as it redials on its own
	s := miniredis.RunT(t)
	addr := s.Addr()
	s.Close()
	var connected atomic.Bool
	h := newHolder(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, DisableCache: true,
		DisableRetry: true}, time.Millisecond, time.Millisecond, func(valkey.Client) { connected.Store(true) })
	require.Error(t, h.connect())
	_, err := h.Client()
	require.NoError(t, err)
	require.False(t, connected.Load())
	h.close()
}
