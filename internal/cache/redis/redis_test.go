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

func setupRedisCache(clientType string) (*Cache, func()) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	config.Config = config.NewConfig()
	rcfg := config.RedisCacheConfig{Endpoint: s.Addr(), ClientType: clientType}
	close := func() {
		s.Close()
	}
	cacheConfig := &config.CachingConfig{CacheType: "redis", Redis: rcfg}
	config.Caches = map[string]*config.CachingConfig{"default": cacheConfig}

	return &Cache{Config: cacheConfig}, close
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

const ctStandard = "standard"

func TestConfiguration(t *testing.T) {
	rc, close := setupRedisCache(ctStandard)
	defer close()

	cfg := rc.Configuration()
	if cfg.Redis.ClientType != ctStandard {
		t.Fatalf("expected %s got %s", ctStandard, cfg.Redis.ClientType)
	}
}

func TestRedisCache_Connect(t *testing.T) {
	rc, close := setupRedisCache(ctStandard)
	defer close()

	// it should connect
	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestRedisCache_Store(t *testing.T) {
	rc, close := setupRedisCache(ctStandard)
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

func TestRedisCache_Retrieve(t *testing.T) {
	rc, close := setupRedisCache(ctStandard)
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
	data, err := rc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func TestRedisCache_Close(t *testing.T) {
	rc, close := setupRedisCache(ctStandard)
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

	rc, close := setupRedisCache(ctStandard)
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
	data, err := rc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	rc.Remove(cacheKey)

	// it should be a cache miss
	data, err = rc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestCache_BulkRemove(t *testing.T) {

	rc, close := setupRedisCache(ctStandard)
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
	data, err := rc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	rc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	data, err = rc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestMapOpFuncs(t *testing.T) {

	// test sentinel
	rc, _ := setupRedisCache("sentinel")
	rc.mapOpFuncs()
	if rc.connectFunc == nil {
		t.Errorf("invalid function mapping for sentinel")
	}

	// test cluster
	rc, _ = setupRedisCache("cluster")
	rc.mapOpFuncs()
	if rc.connectFunc == nil {
		t.Errorf("invalid function mapping for cluster")
	}

}
