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
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cp "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	vo "github.com/trickstercache/trickster/v2/pkg/cache/valkey/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

const (
	cacheKey          = `cacheKey`
	benchmarkKeyCount = 10
)

func setupBenchmark(b *testing.B) *CacheClient {
	b.Helper()
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	if err := rc.Connect(); err != nil {
		closeServer()
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := rc.Close(); err != nil {
			b.Error(err)
		}
		closeServer()
	})
	return rc
}

func setupRedisCache(ct string) (*CacheClient, func()) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	rcfg := redisOptions(ct, s.Addr())
	cacheConfig := &co.Options{Provider: cp.Redis, ProviderID: cp.RedisID, Redis: rcfg}
	return New(context.Background(), "test", cacheConfig), s.Close
}

func redisOptions(ct, addr string) *ro.Options {
	rcfg := &ro.Options{Endpoint: addr, ClientType: ct}
	if ct != ro.ClientTypeStandard {
		rcfg.Endpoint = ""
		rcfg.Endpoints = []string{addr}
		if ct == ro.ClientTypeSentinel {
			rcfg.SentinelMaster = addr
		}
	}
	return rcfg
}

// setupValkeyCache returns a valkey cache, whose settings are the valkey provider's defaults
// with the client-side cache off, as miniredis has no CLIENT TRACKING
func setupValkeyCache(t *testing.T, ct string, mut func(*vo.Options)) (*CacheClient, *miniredis.Miniredis) {
	t.Helper()
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	s := miniredis.RunT(t)
	v := vo.New()
	v.Options = *redisOptions(ct, s.Addr())
	if mut != nil {
		mut(v)
	}
	rc := New(context.Background(), "test", &co.Options{Provider: cp.Valkey, ProviderID: cp.ValkeyID, Valkey: v})
	t.Cleanup(func() { _ = rc.Close() })
	return rc, s
}

func loadTestConfig(t *testing.T, path string) *co.Options {
	t.Helper()
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	conf, err := config.Load([]string{"-config", path, "-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info"})
	require.NoError(t, err)
	cfg, ok := conf.Caches["test"]
	require.True(t, ok, "expected cache named test")
	return cfg
}

func TestClientSelectionFromConfig(t *testing.T) {
	for _, ct := range []string{ro.ClientTypeStandard, ro.ClientTypeCluster, ro.ClientTypeSentinel} {
		t.Run(ct, func(t *testing.T) {
			cfg := loadTestConfig(t, "../../../testdata/test.redis-"+ct+".conf")
			require.Equal(t, ct, cfg.Redis.ClientType)
			c := New(context.Background(), "test", cfg)
			// the test configs name a protocol no client can dial
			require.ErrorIs(t, c.Connect(), ErrInvalidProtocolConfig)
			require.Nil(t, c.Holder())
			require.NoError(t, c.Close())
		})
	}
}

func TestConnectOptionErrors(t *testing.T) {
	tests := []struct {
		name string
		ct   string
		mut  func(*ro.Options)
		err  error
	}{
		{"standard without endpoint", ro.ClientTypeStandard, func(o *ro.Options) { o.Endpoint = "" }, ErrInvalidEndpointConfig},
		{"cluster without endpoints", ro.ClientTypeCluster, func(o *ro.Options) { o.Endpoints = nil }, ErrInvalidEndpointsConfig},
		{"sentinel without endpoints", ro.ClientTypeSentinel, func(o *ro.Options) { o.Endpoints = nil }, ErrInvalidEndpointsConfig},
		{"sentinel without master", ro.ClientTypeSentinel, func(o *ro.Options) { o.SentinelMaster = "" }, ErrInvalidSentinalMasterConfig},
		{"unknown protocol", ro.ClientTypeStandard, func(o *ro.Options) { o.Protocol = "udp" }, ErrInvalidProtocolConfig},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rc, closeServer := setupRedisCache(test.ct)
			defer closeServer()
			test.mut(rc.Config.Redis)
			require.ErrorIs(t, rc.Connect(), test.err)
		})
	}
}

func TestNotConnected(t *testing.T) {
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.ErrorIs(t, rc.Store(cacheKey, []byte("data"), time.Minute), ErrNotConnected)
	_, ls, err := rc.Retrieve(cacheKey)
	require.ErrorIs(t, err, ErrNotConnected)
	require.Equal(t, status.LookupStatusError, ls)
	require.ErrorIs(t, rc.Remove(cacheKey), ErrNotConnected)
	require.NoError(t, rc.Close())
}

func TestRedisCache_Connect(t *testing.T) {
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.NoError(t, rc.Connect())
	first := rc.Holder()
	// connecting again replaces, and closes, the previous client
	require.NoError(t, rc.Connect())
	require.NotSame(t, first, rc.Holder())
	_, err := first.Client()
	require.ErrorIs(t, err, ErrNotConnected)
	require.NoError(t, rc.Close())
}

func TestRedisCache_StoreTTL(t *testing.T) {
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.NoError(t, rc.Connect())
	defer rc.Close()
	s := miniredisOf(t, rc)
	require.NoError(t, rc.Store("forever", []byte("data"), 0))
	require.Zero(t, s.TTL("forever"))
	require.NoError(t, rc.Store("minute", []byte("data"), time.Minute))
	require.Equal(t, time.Minute, s.TTL("minute"))
	// a ttl under a millisecond rounds up to one, as PX 0 would be rejected
	require.NoError(t, rc.Store("brief", []byte("data"), time.Microsecond))
	require.Equal(t, time.Millisecond, s.TTL("brief"))
}

// miniredisOf returns the miniredis server that a standard test cache is connected to
func miniredisOf(t *testing.T, rc *CacheClient) *miniredis.Miniredis {
	t.Helper()
	s := miniredis.NewMiniRedis()
	require.NoError(t, s.StartAddr("127.0.0.1:0"))
	t.Cleanup(s.Close)
	rc.Config.Redis.Endpoint = s.Addr()
	require.NoError(t, rc.Connect())
	return s
}

func TestRedisCache_Store(t *testing.T) {
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.NoError(t, rc.Connect())
	defer rc.Close()
	require.NoError(t, rc.Store(cacheKey, []byte("data"), 60*time.Second))
}

func BenchmarkCache_Store(b *testing.B) {
	rc := setupBenchmark(b)
	n := 0
	for b.Loop() {
		suffix := strconv.Itoa(n)
		if err := rc.Store(cacheKey+suffix, []byte("data"+suffix), time.Minute); err != nil {
			b.Fatal(err)
		}
		n++
	}
}

func TestRedisCache_Retrieve(t *testing.T) {
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.NoError(t, rc.Connect())
	defer rc.Close()
	require.NoError(t, rc.Store(cacheKey, []byte("data"), 60*time.Second))
	data, ls, err := rc.Retrieve(cacheKey)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, ls)
	require.Equal(t, "data", string(data))

	_, ls, err = rc.Retrieve("missing")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, ls)
}

func TestRedisCache_RetrieveError(t *testing.T) {
	rc, s := setupValkeyCache(t, ro.ClientTypeStandard, nil)
	require.NoError(t, rc.Connect())
	// a GET of a hash is a WRONGTYPE error, not a miss
	s.HSet("hash", "f", "v")
	_, ls, err := rc.Retrieve("hash")
	require.Error(t, err)
	require.NotErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusError, ls)
}

func BenchmarkCache_Retrieve(b *testing.B) {
	rc := setupBenchmark(b)
	if err := rc.Store(cacheKey, []byte("data"), time.Minute); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		data, ls, err := rc.Retrieve(cacheKey)
		if err != nil {
			b.Fatal(err)
		}
		if ls != status.LookupStatusHit {
			b.Fatalf("expected %s, got %s", status.LookupStatusHit, ls)
		}
		if string(data) != "data" {
			b.Fatalf("wanted %q, got %q", "data", data)
		}
	}
}

func TestRedisCache_Close(t *testing.T) {
	rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
	defer closeServer()
	require.NoError(t, rc.Connect())
	require.NoError(t, rc.Close())
	// closing twice is harmless
	require.NoError(t, rc.Close())
}

func TestCache_Remove(t *testing.T) {
	for _, ct := range []string{ro.ClientTypeStandard, ro.ClientTypeCluster} {
		t.Run(ct, func(t *testing.T) {
			rc, closeServer := setupRedisCache(ct)
			defer closeServer()
			require.NoError(t, rc.Connect())
			defer rc.Close()

			require.NoError(t, rc.Store(cacheKey, []byte("data"), 60*time.Second))
			data, ls, err := rc.Retrieve(cacheKey)
			require.NoError(t, err)
			require.Equal(t, "data", string(data))
			require.Equal(t, status.LookupStatusHit, ls)

			require.NoError(t, rc.Remove(cacheKey))
			_, ls, err = rc.Retrieve(cacheKey)
			require.ErrorIs(t, err, cache.ErrKNF)
			require.Equal(t, status.LookupStatusKeyMiss, ls)
		})
	}
}

func BenchmarkCache_Remove(b *testing.B) {
	rc := setupBenchmark(b)
	for b.Loop() {
		b.StopTimer()
		if err := rc.Store(cacheKey, []byte("data"), time.Minute); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := rc.Remove(cacheKey); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCache_BulkRemove(t *testing.T) {
	for _, ct := range []string{ro.ClientTypeStandard, ro.ClientTypeCluster} {
		t.Run(ct, func(t *testing.T) {
			rc, closeServer := setupRedisCache(ct)
			defer closeServer()
			require.NoError(t, rc.Connect())
			defer rc.Close()
			keys := []string{"alpha", "bravo", "charlie"}
			for _, k := range keys {
				require.NoError(t, rc.Store(k, []byte(k), time.Minute))
			}
			require.NoError(t, rc.Remove(keys...))
			for _, k := range keys {
				_, ls, _ := rc.Retrieve(k)
				require.Equal(t, status.LookupStatusKeyMiss, ls, k)
			}
			require.NoError(t, rc.Remove())
		})
	}
}

func TestCache_BulkRemoveError(t *testing.T) {
	rc, s := setupValkeyCache(t, ro.ClientTypeStandard, func(o *vo.Options) {
		o.MaxRetries = -1
		o.DialTimeout = timeconv.Duration(50 * time.Millisecond)
	})
	require.NoError(t, rc.Connect())
	s.Close()
	require.Error(t, rc.Remove("a", "b"))
}

func BenchmarkCache_BulkRemove(b *testing.B) {
	rc := setupBenchmark(b)
	keys := make([]string, benchmarkKeyCount)
	values := make([][]byte, benchmarkKeyCount)
	for n := range benchmarkKeyCount {
		suffix := strconv.Itoa(n)
		keys[n] = cacheKey + suffix
		values[n] = []byte("data" + suffix)
	}
	for b.Loop() {
		b.StopTimer()
		for n, key := range keys {
			if err := rc.Store(key, values[n], time.Minute); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		if err := rc.Remove(keys...); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(benchmarkKeyCount, "keys/op")
}

func TestValkeyCache(t *testing.T) {
	multiplex := 0
	rc, _ := setupValkeyCache(t, ro.ClientTypeStandard, func(o *vo.Options) { o.PipelineMultiplex = &multiplex })
	require.NoError(t, rc.Connect())
	gotRedis, gotSettings := rc.connection()
	require.Same(t, &rc.Config.Valkey.Options, gotRedis)
	require.Same(t, &rc.Config.Valkey.Settings, gotSettings)
	require.False(t, rc.clientCache)
	require.NoError(t, rc.Store(cacheKey, []byte("data"), time.Minute))
	data, ls, err := rc.Retrieve(cacheKey)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, ls)
	require.Equal(t, "data", string(data))
}

func TestValkeyCacheClientCacheRefused(t *testing.T) {
	rc, _ := setupValkeyCache(t, ro.ClientTypeStandard, func(o *vo.Options) { o.ClientSideCache.Enabled = true })
	// miniredis has no CLIENT TRACKING, as some managed services don't, so the client can't connect
	require.Error(t, rc.Connect())
	require.True(t, rc.clientCache)
	require.NotNil(t, rc.localHits)
}

func TestConnectionWithoutRedisOptions(t *testing.T) {
	// a valkey block alone picks the valkey provider's settings, as ACME storage passes it
	c := New(context.Background(), "test", &co.Options{Valkey: vo.New()})
	r, s := c.connection()
	require.NotNil(t, s)
	require.Equal(t, vo.DefaultValkeyEndpoint, r.Endpoint)
	c = New(context.Background(), "test", &co.Options{Redis: ro.New(), Valkey: vo.New()})
	_, s = c.connection()
	require.Nil(t, s)
}

func TestCheckServer(t *testing.T) {
	for _, isValkey := range []bool{false, true} {
		rc, closeServer := setupRedisCache(ro.ClientTypeStandard)
		require.NoError(t, rc.Connect())
		cl, err := rc.Holder().Client()
		require.NoError(t, err)
		// miniredis reports neither server, which is logged for a valkey cache only
		rc.checkServer(cl, isValkey)
		require.NoError(t, rc.Close())
		closeServer()
	}
}
