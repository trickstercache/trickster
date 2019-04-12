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
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"

	"github.com/alicebob/miniredis"
)

func init() {
	metrics.Init()
}

func setupRedisCache() (*Cache, func()) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	config.Config = config.NewConfig()
	rcfg := config.RedisCacheConfig{Endpoint: s.Addr()}
	close := func() {
		s.Close()
	}
	cacheConfig := config.CachingConfig{Type: "redis", Redis: rcfg}
	config.Caches = map[string]config.CachingConfig{"default": cacheConfig}

	return &Cache{Config: &cacheConfig}, close
}

func TestRedisCache_Connect(t *testing.T) {
	rc, close := setupRedisCache()
	defer close()

	// it should connect
	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestRedisCache_Store(t *testing.T) {
	rc, close := setupRedisCache()
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = rc.Store("cacheKey", []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func TestRedisCache_Retrieve(t *testing.T) {
	rc, close := setupRedisCache()
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}
	err = rc.Store("cacheKey", []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := rc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func TestRedisCache_Close(t *testing.T) {
	rc, close := setupRedisCache()
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
