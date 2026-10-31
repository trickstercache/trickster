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
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	vo "github.com/trickstercache/trickster/v2/pkg/cache/valkey/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The live tests need the developer environment's servers, and run only when TRICKSTER_REDIS_TEST=1;
// the cluster tests also need its cache-cluster profile. They use the benchmarks' address variables.
const envLiveTest = "TRICKSTER_REDIS_TEST"

// envCompatAddr names the server that the compat smoke test runs the redis provider against
const envCompatAddr = "TRICKSTER_REDIS_COMPAT_ADDR"

func liveAddr(t *testing.T, env, fallback string) string {
	t.Helper()
	if os.Getenv(envLiveTest) != "1" {
		t.Skip("set " + envLiveTest + "=1 to run the live redis and valkey tests")
	}
	addr := envOr(env, fallback)
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Skipf("%s is not reachable: %v", addr, err)
	}
	c.Close()
	return addr
}

func liveValkey(t *testing.T, ct, addr string, mut func(*vo.Options)) *CacheClient {
	t.Helper()
	v := vo.New()
	v.Options = *benchRedisOptions(ct, addr)
	if mut != nil {
		mut(v)
	}
	c := New(context.Background(), "live", &co.Options{Provider: cp.Valkey, ProviderID: cp.ValkeyID, Valkey: v})
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func liveRedis(t *testing.T, ct, addr string, mut func(*ro.Options)) *CacheClient {
	t.Helper()
	r := benchRedisOptions(ct, addr)
	if mut != nil {
		mut(r)
	}
	c := New(context.Background(), "live", &co.Options{Provider: cp.Redis, ProviderID: cp.RedisID, Redis: r})
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func requireValue(t *testing.T, c *CacheClient, key, want string) {
	t.Helper()
	b, ls, err := c.Retrieve(key)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, ls)
	require.Equal(t, want, string(b))
}

func TestLiveClientSideCache(t *testing.T) {
	addr := liveAddr(t, envBenchValkeyAddr, defaultBenchValkey)
	for _, mode := range []string{vo.ClientCacheModeOptIn, vo.ClientCacheModeBroadcast} {
		t.Run(mode, func(t *testing.T) {
			prefix := "live:csc:" + mode + ":" + strconv.FormatInt(time.Now().UnixNano(), 10) + ":"
			c := liveValkey(t, ro.ClientTypeStandard, addr, func(o *vo.Options) {
				o.ClientSideCache.Enabled = true
				o.ClientSideCache.Mode = mode
				if mode == vo.ClientCacheModeBroadcast {
					o.ClientSideCache.Prefixes = []string{prefix}
				}
			})
			other := liveRedis(t, ro.ClientTypeStandard, addr, nil)
			key := prefix + "k"
			require.NoError(t, other.Store(key, []byte("v1"), time.Minute))
			requireValue(t, c, key, "v1")
			requireValue(t, c, key, "v1")
			require.GreaterOrEqual(t, testutil.ToFloat64(c.localHits), 1.0)

			// another instance's write and purge reach this instance's local copy
			require.NoError(t, other.Store(key, []byte("v2"), time.Minute))
			require.Eventually(t, func() bool {
				b, _, _ := c.Retrieve(key)
				return string(b) == "v2"
			}, 2*time.Second, 10*time.Millisecond)
			require.NoError(t, other.Remove(key))
			require.Eventually(t, func() bool {
				_, ls, err := c.Retrieve(key)
				return ls == status.LookupStatusKeyMiss && err == cache.ErrKNF
			}, 2*time.Second, 10*time.Millisecond)
		})
	}
}

func TestLiveClusterRemoveAcrossSlots(t *testing.T) {
	addr := liveAddr(t, envBenchClusterAddr, defaultBenchCluster)
	keys := []string{"live:purge:alpha", "live:purge:bravo", "live:purge:charlie", "live:purge:delta"}
	for name, c := range map[string]*CacheClient{
		"redis":  liveRedis(t, ro.ClientTypeCluster, addr, nil),
		"valkey": liveValkey(t, ro.ClientTypeCluster, addr, nil),
	} {
		t.Run(name, func(t *testing.T) {
			for _, k := range keys {
				require.NoError(t, c.Store(k, []byte(k), time.Minute))
			}
			// the keys hash to several slots, which one DEL can't span
			require.NoError(t, c.Remove(keys...))
			for _, k := range keys {
				_, ls, _ := c.Retrieve(k)
				require.Equal(t, status.LookupStatusKeyMiss, ls, k)
			}
		})
	}
}

func TestLiveValkeyClusterDB(t *testing.T) {
	addr := liveAddr(t, envBenchClusterAddr, defaultBenchCluster)
	key := "live:db:" + strconv.FormatInt(time.Now().UnixNano(), 10)
	db2 := liveValkey(t, ro.ClientTypeCluster, addr, func(o *vo.Options) { o.DB = 2 })
	require.NoError(t, db2.Store(key, []byte("in2"), time.Minute))
	requireValue(t, db2, key, "in2")
	// a redis cache ignores db in cluster mode, so it reads db 0, which doesn't have the key
	db0 := liveRedis(t, ro.ClientTypeCluster, addr, func(o *ro.Options) { o.DB = 2 })
	_, ls, _ := db0.Retrieve(key)
	require.Equal(t, status.LookupStatusKeyMiss, ls)
	require.NoError(t, db2.Remove(key))
}

func TestLiveCompat(t *testing.T) {
	if os.Getenv(envCompatAddr) == "" {
		t.Skip("set " + envCompatAddr + " to run the redis provider against an older or compatible server")
	}
	c := liveRedis(t, ro.ClientTypeStandard, liveAddr(t, envCompatAddr, ""), nil)
	keys := []string{"compat:alpha", "compat:bravo", "compat:charlie"}
	for _, k := range keys {
		require.NoError(t, c.Store(k, []byte(k), time.Minute))
		requireValue(t, c, k, k)
	}
	require.NoError(t, c.Remove(keys[0]))
	require.NoError(t, c.Remove(keys[1:]...))
	for _, k := range keys {
		_, ls, err := c.Retrieve(k)
		require.Equal(t, status.LookupStatusKeyMiss, ls, k)
		require.ErrorIs(t, err, cache.ErrKNF)
	}
}
