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

package redisstorage

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

const (
	testPrefix = "trickster:acme:test:"
	testTTL    = 300 * time.Millisecond
	certKey    = "certificates/ca/www.acme.test/www.acme.test.crt"
	keyKey     = "certificates/ca/www.acme.test/www.acme.test.key"
	otherKey   = "certificates/ca/api.acme.test/api.acme.test.crt"
	accountKey = "acme/ca/users/ops/ops.json"
	lockName   = "issue_cert_www.acme.test"
)

func newTestStorage(t *testing.T) (*Storage, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	s := New(client, testPrefix, testTTL)
	t.Cleanup(func() { _ = s.Close() })
	return s, mr
}

func TestStoreLoadListStatDelete(t *testing.T) {
	s, mr := newTestStorage(t)
	ctx := context.Background()
	for _, k := range []string{certKey, keyKey, otherKey, accountKey} {
		require.NoError(t, s.Store(ctx, k, []byte(k)))
	}
	b, err := s.Load(ctx, certKey)
	require.NoError(t, err)
	require.Equal(t, certKey, string(b))
	_, err = s.Load(ctx, "missing")
	require.ErrorIs(t, err, fs.ErrNotExist)

	// every key shares one hash tag, so the keyspace stays in a single cluster slot
	for _, k := range mr.Keys() {
		require.Contains(t, k, "{"+testPrefix+"}")
	}

	require.True(t, s.Exists(ctx, certKey))
	require.True(t, s.Exists(ctx, "certificates/ca"))
	require.False(t, s.Exists(ctx, "certificates/other"))

	all, err := s.List(ctx, "certificates", true)
	require.NoError(t, err)
	require.Equal(t, []string{otherKey, certKey, keyKey}, all)
	children, err := s.List(ctx, "certificates/ca/", false)
	require.NoError(t, err)
	require.Equal(t, []string{"certificates/ca/api.acme.test", "certificates/ca/www.acme.test"}, children)
	_, err = s.List(ctx, "nothing", false)
	require.ErrorIs(t, err, fs.ErrNotExist)
	everything, err := s.List(ctx, "", true)
	require.NoError(t, err)
	require.Len(t, everything, 4)

	info, err := s.Stat(ctx, certKey)
	require.NoError(t, err)
	require.True(t, info.IsTerminal)
	require.Equal(t, int64(len(certKey)), info.Size)
	require.WithinDuration(t, time.Now(), info.Modified, time.Minute)
	info, err = s.Stat(ctx, "certificates/ca")
	require.NoError(t, err)
	require.False(t, info.IsTerminal)
	_, err = s.Stat(ctx, "missing")
	require.ErrorIs(t, err, fs.ErrNotExist)

	require.NoError(t, s.Delete(ctx, "certificates/ca/www.acme.test"))
	require.False(t, s.Exists(ctx, certKey))
	require.False(t, s.Exists(ctx, keyKey))
	require.True(t, s.Exists(ctx, otherKey))
	require.NoError(t, s.Delete(ctx, "missing"))
}

func TestLocks(t *testing.T) {
	s, mr := newTestStorage(t)
	ctx := context.Background()
	require.NoError(t, s.Lock(ctx, lockName))

	// a second holder waits, and gives up when its context ends
	other := New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), testPrefix, testTTL)
	waitCtx, cancel := context.WithTimeout(ctx, 3*testTTL)
	defer cancel()
	require.ErrorIs(t, other.Lock(waitCtx, lockName), context.DeadlineExceeded)

	// the holder's lease is refreshed in the background, so it outlives its TTL
	time.Sleep(2 * testTTL)
	require.True(t, mr.Exists(s.lockKey(lockName)))
	require.NoError(t, s.RenewLockLease(ctx, lockName, time.Minute))

	require.NoError(t, s.Unlock(ctx, lockName))
	require.ErrorIs(t, s.Unlock(ctx, lockName), ErrLockNotHeld)
	require.ErrorIs(t, s.RenewLockLease(ctx, lockName, time.Minute), ErrLockNotHeld)
	require.NoError(t, other.Lock(ctx, lockName))
	require.NoError(t, other.Close())
	require.False(t, mr.Exists(other.lockKey(lockName)))
}

func TestLockLost(t *testing.T) {
	s, mr := newTestStorage(t)
	ctx := context.Background()
	require.NoError(t, s.Lock(ctx, lockName))
	// another instance took over once the lease lapsed; this holder must not release its lock
	mr.Set(s.lockKey(lockName), "someone-else")
	require.ErrorIs(t, s.RenewLockLease(ctx, lockName, time.Minute), ErrLockNotHeld)
	require.ErrorIs(t, s.Unlock(ctx, lockName), ErrLockNotHeld)
	got, err := mr.Get(s.lockKey(lockName))
	require.NoError(t, err)
	require.Equal(t, "someone-else", got)
}

func TestErrorsWhenUnreachable(t *testing.T) {
	mr := miniredis.RunT(t)
	// no retries and a short dial, so each call against the stopped server fails at once
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(), MaxRetries: -1,
		DialTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { client.Close() })
	s := New(client, testPrefix, testTTL)
	ctx := context.Background()
	require.NoError(t, s.Lock(ctx, lockName))
	mr.Close()
	require.Error(t, s.Store(ctx, certKey, nil))
	_, err := s.Load(ctx, certKey)
	require.Error(t, err)
	require.Error(t, s.Delete(ctx, certKey))
	require.False(t, s.Exists(ctx, certKey))
	_, err = s.List(ctx, "", true)
	require.Error(t, err)
	_, err = s.Stat(ctx, certKey)
	require.Error(t, err)
	require.Error(t, s.Lock(ctx, "other"))
	require.Error(t, s.RenewLockLease(ctx, lockName, time.Minute))
	require.Error(t, s.Close())
}
