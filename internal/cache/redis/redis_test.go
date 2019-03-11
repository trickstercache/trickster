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

	"github.com/alicebob/miniredis"
	"github.com/go-kit/kit/log"
)

func setupRedisCache() (RedisCache, func()) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1000}}
	tr := TricksterHandler{
		Logger:           log.NewNopLogger(),
		ResponseChannels: make(map[string]chan *ClientRequestContext),
		Config:           &cfg,
	}
	rcfg := RedisCacheConfig{Endpoint: s.Addr()}
	close := func() {
		s.Close()
	}
	return RedisCache{T: &tr, Config: rcfg}, close
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
	err = rc.Store("cacheKey", "data", 60000)
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
	err = rc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := rc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if data != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func TestRedisCache_ReapOnce(t *testing.T) {
	rc, close := setupRedisCache()
	defer close()

	err := rc.Connect()
	if err != nil {
		t.Error(err)
	}

	// fake an empty response channel to reap
	ch := make(chan *ClientRequestContext, 100)
	rc.T.ResponseChannels["cacheKey"] = ch

	// it should remove empty response channel
	rc.ReapOnce()

	if rc.T.ResponseChannels["cacheKey"] != nil {
		t.Errorf("expected response channel to be removed")
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
