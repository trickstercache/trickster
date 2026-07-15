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

	"github.com/alicebob/miniredis/v2"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const (
	cacheKey          = `cacheKey`
	benchmarkKeyCount = 10
)

func setupBenchmark(b *testing.B) *CacheClient {
	b.Helper()
	rc, closeServer := setupRedisCache(clientTypeStandard)
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

func setupRedisCache(ct clientType) (*CacheClient, func()) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	conf := config.NewConfig()
	rcfg := &ro.Options{Endpoint: s.Addr(), ClientType: ct.String()}
	if ct != clientTypeStandard {
		rcfg.Endpoint = ""
		rcfg.Endpoints = []string{s.Addr()}
		if ct == clientTypeSentinel {
			rcfg.SentinelMaster = s.Addr()
		}
	}
	close := func() {
		s.Close()
	}
	cacheConfig := &co.Options{Provider: "redis", Redis: rcfg}
	conf.Caches = co.Lookup{"default": cacheConfig}

	return New(context.Background(), "test", cacheConfig), close
}

func TestClientSelectionSentinel(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const expected1 = "ERR unknown command `sentinel`"
	args := []string{
		"-config", "../../../testdata/test.redis-sentinel.conf",
		"-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info",
	}
	conf, err := config.Load(args)
	if err != nil {
		t.Fatal(err)
	}
	const cacheName = "test"
	cfg, ok := conf.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := New(context.Background(), cacheName, cfg)
	if err != nil {
		t.Error(err)
	}
	err = cache.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestSentinelOpts(t *testing.T) {
	const expected1 = `invalid 'endpoints' config`
	const expected2 = `invalid 'sentinel_master' config`

	rc, close := setupRedisCache(clientTypeSentinel)
	defer close()

	// test empty endpoint
	rc.Config.Redis.Endpoints = nil
	err := rc.Connect()
	if err == nil || err.Error() != expected1 {
		t.Errorf("expected error for %s", expected1)
	}

	rc.Config.Redis.Endpoints = []string{"test"}
	rc.Config.Redis.SentinelMaster = ""

	// test empty SentinelMaster
	err = rc.Connect()
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected error for %s", expected2)
	}
}

func TestClusterOpts(t *testing.T) {
	const expected1 = `invalid 'endpoints' config`

	rc, close := setupRedisCache(clientTypeCluster)
	defer close()

	// test empty endpoint
	rc.Config.Redis.Endpoints = nil
	err := rc.Connect()
	if err == nil || err.Error() != expected1 {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestClientOpts(t *testing.T) {
	const expected1 = `invalid endpoint: `

	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	// test empty endpoint
	rc.Config.Redis.Endpoint = ""
	err := rc.Connect()
	if err == nil || err.Error() != expected1 {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestClientSelectionCluster(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	expected1 := "invalid endpoint"
	args := []string{
		"-config", "../../../testdata/test.redis-cluster.conf",
		"-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info",
	}
	conf, err := config.Load(args)
	if err != nil {
		t.Fatal(err)
	}
	const cacheName = "test"
	cfg, ok := conf.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := New(context.Background(), cacheName, cfg)
	if err != nil {
		t.Error(err)
	}
	err = cache.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestClientSelectionStandard(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	expected1 := "invalid endpoint"
	args := []string{
		"-config", "../../../testdata/test.redis-standard.conf",
		"-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info",
	}
	conf, err := config.Load(args)
	if err != nil {
		t.Fatal(err)
	}
	const cacheName = "test"
	cfg, ok := conf.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := New(context.Background(), cacheName, cfg)
	if err != nil {
		t.Error(err)
	}
	err = cache.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestRedisCache_Connect(t *testing.T) {
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	// it should connect
	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestRedisCache_Store(t *testing.T) {
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = rc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
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
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
	err = rc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := rc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
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
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should close
	err = rc.Close()
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Remove(t *testing.T) {
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer rc.Close()

	// it should store a value
	err = rc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := rc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	rc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = rc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
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
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer rc.Close()

	// it should store a value
	err = rc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := rc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	rc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = rc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
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
