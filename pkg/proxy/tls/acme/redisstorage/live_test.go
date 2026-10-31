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
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// envCompatAddr names the server that the compat smoke test runs the Lua lock scripts against
const envCompatAddr = "TRICKSTER_REDIS_COMPAT_ADDR"

func TestLiveCompat(t *testing.T) {
	addr := os.Getenv(envCompatAddr)
	if addr == "" {
		t.Skip("set " + envCompatAddr + " to run the storage against an older or compatible server")
	}
	ctx := context.Background()
	prefix := testPrefix + strconv.FormatInt(time.Now().UnixNano(), 10) + ":"
	s := New(newSource(t, addr, nil), prefix, testTTL, false)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.Store(ctx, certKey, []byte("cert")))
	b, err := s.Load(ctx, certKey)
	require.NoError(t, err)
	require.Equal(t, "cert", string(b))
	keys, err := s.List(ctx, "certificates", true)
	require.NoError(t, err)
	require.Equal(t, []string{certKey}, keys)

	// the lease refresh and release run the Lua scripts
	require.NoError(t, s.Lock(ctx, lockName))
	require.NoError(t, s.RenewLockLease(ctx, lockName, time.Minute))
	require.NoError(t, s.Unlock(ctx, lockName))
	require.ErrorIs(t, s.Unlock(ctx, lockName), ErrLockNotHeld)
	require.NoError(t, s.Delete(ctx, certKey))
	require.False(t, s.Exists(ctx, certKey))
}
