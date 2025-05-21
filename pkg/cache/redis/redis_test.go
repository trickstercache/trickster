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
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	ro "github.com/trickstercache/trickster/v2/pkg/cache/redis/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	"github.com/alicebob/miniredis"
)

const cacheKey = `cacheKey`

func storeBenchmark(b *testing.B) (*CacheClient, func()) {
	rc, close := setupRedisCache(clientTypeStandard)
	err := rc.Connect()
	if err != nil {
		b.Error(err)
	}
	for n := 0; n < b.N; n++ {
		err := rc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return rc, close
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

	return New("test", cacheConfig), close
}

func TestClientSelectionSentinel(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const expected1 = "ERR unknown command `sentinel`"
	args := []string{"-config", "../../../testdata/test.redis-sentinel.conf",
		"-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info"}
	conf, err := config.Load(args)
	if err != nil {
		t.Fatal(err)
	}
	const cacheName = "test"
	cfg, ok := conf.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := New(cacheName, cfg)
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
	args := []string{"-config", "../../../testdata/test.redis-cluster.conf",
		"-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info"}
	conf, err := config.Load(args)
	if err != nil {
		t.Fatal(err)
	}
	const cacheName = "test"
	cfg, ok := conf.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := New(cacheName, cfg)
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
	args := []string{"-config", "../../../testdata/test.redis-standard.conf",
		"-origin-url", "http://0.0.0.0", "-provider",
		providers.ReverseProxyCacheShort, "-log-level", "info"}
	conf, err := config.Load(args)
	if err != nil {
		t.Fatal(err)
	}
	const cacheName = "test"
	cfg, ok := conf.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := New(cacheName, cfg)
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
	rc, close := storeBenchmark(b)
	if rc == nil {
		b.Error("Could not create the redis cache")
	}
	defer close()
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
	rc, close := storeBenchmark(b)
	defer close()

	for n := 0; n < b.N; n++ {
		data, ls, err := rc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
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
	rc, close := storeBenchmark(b)
	defer close()

	for n := 0; n < b.N; n++ {
		var data []byte
		data, ls, err := rc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		rc.Remove(cacheKey + strconv.Itoa(n))

		_, ls, err = rc.Retrieve(cacheKey + strconv.Itoa(n))
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey+strconv.Itoa(n))
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
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
	rc, close := storeBenchmark(b)
	defer close()

	var keyArray []string
	for n := 0; n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	rc.Remove(keyArray...)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := rc.Retrieve(cacheKey + strconv.Itoa(n))
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}
