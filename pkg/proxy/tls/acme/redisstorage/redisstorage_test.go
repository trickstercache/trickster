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
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/stretchr/testify/require"
	"github.com/valkey-io/valkey-go"
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

type staticSource struct {
	c valkey.Client
}

func (s staticSource) Client() (valkey.Client, error) {
	return s.c, nil
}

var errNoClient = errors.New("no client")

type noSource struct{}

func (noSource) Client() (valkey.Client, error) {
	return nil, errNoClient
}

func newSource(t *testing.T, addr string, mut func(*valkey.ClientOption)) staticSource {
	t.Helper()
	o := valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, DisableCache: true}
	if mut != nil {
		mut(&o)
	}
	c, err := valkey.NewClient(o)
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return staticSource{c: c}
}

func newTestStorage(t *testing.T) (*Storage, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	s := New(newSource(t, mr.Addr(), nil), testPrefix, testTTL, false)
	t.Cleanup(func() { _ = s.Close() })
	return s, mr
}

// withoutLua makes mr answer scripts as a Valkey server without its Lua engine does, and serve
// Valkey's DELIFEQ and SET IFEQ, which miniredis lacks
func withoutLua(mr *miniredis.Miniredis) {
	mr.Server().SetPreHook(func(c *server.Peer, cmd string, args ...string) bool {
		switch strings.ToUpper(cmd) {
		case "EVALSHA":
			c.WriteError("NOSCRIPT No matching script.")
		case "EVAL":
			c.WriteError("ERR Could not find scripting engine 'lua'")
		case "DELIFEQ":
			if got, err := mr.Get(args[0]); err == nil && got == args[1] {
				mr.Del(args[0])
				c.WriteInt(1)
				return true
			}
			c.WriteInt(0)
		case "SET":
			if len(args) < 6 || !strings.EqualFold(args[2], "IFEQ") {
				return false
			}
			if got, err := mr.Get(args[0]); err != nil || got != args[3] {
				c.WriteNull()
				return true
			}
			ms, _ := strconv.Atoi(args[5])
			mr.Set(args[0], args[1])
			mr.SetTTL(args[0], time.Duration(ms)*time.Millisecond)
			c.WriteOK()
		default:
			return false
		}
		return true
	})
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
	other := New(newSource(t, mr.Addr(), nil), testPrefix, testTTL, false)
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
	src := newSource(t, mr.Addr(), func(o *valkey.ClientOption) {
		o.DisableRetry = true
		o.Dialer.Timeout = 50 * time.Millisecond
	})
	s := New(src, testPrefix, testTTL, false)
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

func TestErrorsWithoutClient(t *testing.T) {
	s := New(noSource{}, testPrefix, testTTL, true)
	ctx := context.Background()
	require.ErrorIs(t, s.Store(ctx, certKey, nil), errNoClient)
	_, err := s.Load(ctx, certKey)
	require.ErrorIs(t, err, errNoClient)
	require.ErrorIs(t, s.Delete(ctx, certKey), errNoClient)
	require.False(t, s.Exists(ctx, certKey))
	_, err = s.List(ctx, "", true)
	require.ErrorIs(t, err, errNoClient)
	_, err = s.Stat(ctx, certKey)
	require.ErrorIs(t, err, errNoClient)
	require.ErrorIs(t, s.Lock(ctx, lockName), errNoClient)
	_, err = s.release(ctx, lockName, "t")
	require.ErrorIs(t, err, errNoClient)
	_, err = s.refresh(ctx, lockName, "t", time.Minute)
	require.ErrorIs(t, err, errNoClient)
}

func TestLocksWithoutLua(t *testing.T) {
	mr := miniredis.RunT(t)
	withoutLua(mr)
	s := New(newSource(t, mr.Addr(), nil), testPrefix, testTTL, true)
	ctx := context.Background()
	require.NoError(t, s.Lock(ctx, lockName))
	// the background refresh switches to native commands and keeps the lease alive
	time.Sleep(2 * testTTL)
	require.True(t, s.noLua.Load())
	require.True(t, mr.Exists(s.lockKey(lockName)))
	require.NoError(t, s.RenewLockLease(ctx, lockName, time.Minute))

	// a lease another instance took is neither renewed nor released
	mr.Set(s.lockKey(lockName), "someone-else")
	require.ErrorIs(t, s.RenewLockLease(ctx, lockName, time.Minute), ErrLockNotHeld)
	require.ErrorIs(t, s.Unlock(ctx, lockName), ErrLockNotHeld)
	got, err := mr.Get(s.lockKey(lockName))
	require.NoError(t, err)
	require.Equal(t, "someone-else", got)

	mr.Del(s.lockKey(lockName))
	require.NoError(t, s.Lock(ctx, lockName))
	require.NoError(t, s.Unlock(ctx, lockName))
	require.False(t, mr.Exists(s.lockKey(lockName)))
}

func TestLocksWithoutLuaOrFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	withoutLua(mr)
	// a redis storage never falls back, so a server without Lua fails its lock calls
	s := New(newSource(t, mr.Addr(), nil), testPrefix, testTTL, false)
	ctx := context.Background()
	require.NoError(t, s.Lock(ctx, lockName))
	err := s.RenewLockLease(ctx, lockName, time.Minute)
	require.ErrorContains(t, err, "scripting engine")
	require.False(t, s.noLua.Load())
	require.ErrorContains(t, s.Unlock(ctx, lockName), "scripting engine")
}

func TestLacksLua(t *testing.T) {
	s := New(noSource{}, testPrefix, testTTL, true)
	require.False(t, s.lacksLua(nil))
	require.False(t, s.lacksLua(errNoClient))
	require.False(t, s.noLua.Load())
}
