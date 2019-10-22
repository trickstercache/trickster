/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"

	"github.com/alicebob/miniredis"
)

func init() {
	metrics.Init()
}

const cacheKey = `cacheKey`

func storeBenchmark(b *testing.B) (*Cache, func()) {
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

func setupRedisCache(ct clientType) (*Cache, func()) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	config.Config = config.NewConfig()
	rcfg := config.RedisCacheConfig{Endpoint: s.Addr(), ClientType: ct.String()}
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
	cacheConfig := &config.CachingConfig{CacheType: "redis", Redis: rcfg}
	config.Caches = map[string]*config.CachingConfig{"default": cacheConfig}

	return &Cache{Config: cacheConfig}, close
}

func TestClientSelectionSentinel(t *testing.T) {
	const expected1 = "ERR unknown command `sentinel`"
	args := []string{"-config", "../../../testdata/test.redis-sentinel.conf",
		"-origin-url", "http://0.0.0.0", "-origin-type", "rpc", "-log-level", "info"}
	err := config.Load("trickster", "test", args)
	if err != nil {
		t.Error(err)
	}
	const cacheName = "test"
	cfg, ok := config.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := Cache{Name: cacheName, Config: cfg}
	if err != nil {
		t.Error(err)
	}
	err = cache.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestSentinelOpts(t *testing.T) {

	const expected1 = `Invalid 'endpoints' config`
	const expected2 = `Invalid 'sentinel_master' config`

	rc, close := setupRedisCache(clientTypeSentinel)
	defer close()

	// test empty endpoint
	rc.Configuration().Redis.Endpoints = nil
	err := rc.Connect()
	if err == nil || err.Error() != expected1 {
		t.Errorf("expected error for %s", expected1)
	}

	rc.Configuration().Redis.Endpoints = []string{"test"}
	rc.Configuration().Redis.SentinelMaster = ""

	// test empty SentinelMaster
	err = rc.Connect()
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected error for %s", expected2)
	}
}

func TestClusterOpts(t *testing.T) {

	const expected1 = `Invalid 'endpoints' config`

	rc, close := setupRedisCache(clientTypeCluster)
	defer close()

	// test empty endpoint
	rc.Configuration().Redis.Endpoints = nil
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
	rc.Configuration().Redis.Endpoint = ""
	err := rc.Connect()
	if err == nil || err.Error() != expected1 {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestClientSelectionCluster(t *testing.T) {
	expected1 := "invalid endpoint"
	args := []string{"-config", "../../../testdata/test.redis-cluster.conf",
		"-origin-url", "http://0.0.0.0", "-origin-type", "rpc", "-log-level", "info"}
	err := config.Load("trickster", "test", args)
	if err != nil {
		t.Error(err)
	}
	const cacheName = "test"
	cfg, ok := config.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := Cache{Name: cacheName, Config: cfg}
	if err != nil {
		t.Error(err)
	}
	err = cache.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestClientSelectionStandard(t *testing.T) {
	expected1 := "invalid endpoint"
	args := []string{"-config", "../../../testdata/test.redis-standard.conf",
		"-origin-url", "http://0.0.0.0", "-origin-type", "rpc", "-log-level", "info"}
	err := config.Load("trickster", "test", args)
	if err != nil {
		t.Error(err)
	}
	const cacheName = "test"
	cfg, ok := config.Caches[cacheName]
	if !ok {
		t.Errorf("expected cache named %s", cacheName)
	}
	cache := Cache{Name: cacheName, Config: cfg}
	if err != nil {
		t.Error(err)
	}
	err = cache.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
}

func TestDurationFromMS(t *testing.T) {

	tests := []struct {
		input    int
		expected time.Duration
	}{
		{0, time.Duration(0)},
		{5000, time.Duration(5000) * time.Millisecond},
		{60000, time.Duration(60000) * time.Millisecond},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			res := durationFromMS(test.input)

			if res != test.expected {
				t.Fatalf("Mismatch in durationFromMS: expected=%f actual=%f", test.expected.Seconds(), res.Seconds())
			}
		})
	}

}

func TestRedisCache_SetTTL(t *testing.T) {

	const expected = "data"

	cache, closer := setupRedisCache(clientTypeStandard)
	defer closer()

	err := cache.Connect()
	if err != nil {
		t.Error(err)
	}
	defer cache.Close()

	// it should store a value
	err = cache.Store(cacheKey, []byte(expected), time.Duration(1)*time.Second)
	if err != nil {
		t.Error(err)
	}
	cache.SetTTL(cacheKey, time.Duration(3600)*time.Second)

	// since the TTL is updated to 1 hour, waiting more than the original TTL of 1s
	// should not matter
	time.Sleep(1010 * time.Millisecond)

	val, err := cache.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}

	if string(val) != expected {
		t.Errorf("expected %s got %s", expected, string(val))
	}

}

func BenchmarkCache_SetTTL(b *testing.B) {
	rc, close := storeBenchmark(b)
	defer close()

	for n := 0; n < b.N; n++ {
		expected := "data" + strconv.Itoa(n)
		rc.SetTTL(cacheKey+strconv.Itoa(n), time.Duration(3600)*time.Second)
		//time.Sleep(1010 * time.Millisecond)
		val, err := rc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}

		if string(val) != expected {
			b.Errorf("expected %s got %s", expected, string(val))
		}
	}
}

func TestConfiguration(t *testing.T) {
	rc, close := setupRedisCache(clientTypeStandard)
	defer close()

	cfg := rc.Configuration()
	if cfg.Redis.ClientType != clientTypeStandard.String() {
		t.Fatalf("expected %s got %s", clientTypeStandard.String(), cfg.Redis.ClientType)
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
	data, err := rc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	rc, close := storeBenchmark(b)
	defer close()

	for n := 0; n < b.N; n++ {
		data, err := rc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
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
	data, err := rc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	rc.Remove(cacheKey)

	// it should be a cache miss
	_, err = rc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func BenchmarkCache_Remove(b *testing.B) {
	rc, close := storeBenchmark(b)
	defer close()

	for n := 0; n < b.N; n++ {
		var data []byte
		data, err := rc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}

		rc.Remove(cacheKey + strconv.Itoa(n))

		data, err = rc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey+strconv.Itoa(n))
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
	data, err := rc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	rc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	_, err = rc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func BenchmarkCache_BulkRemove(b *testing.B) {
	rc, close := storeBenchmark(b)
	defer close()

	var keyArray []string
	for n := 0; n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	rc.BulkRemove(keyArray, true)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, err := rc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
	}
}
