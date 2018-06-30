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

package main

import (
	"github.com/go-kit/kit/log"
	"testing"
)

func setupMemoryCache() MemoryCache {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1000}}
	tr := TricksterHandler{
		Logger:           log.NewNopLogger(),
		ResponseChannels: make(map[string]chan *ClientRequestContext),
		Config:           &cfg,
	}
	return MemoryCache{T: &tr}
}

func TestMemoryCache_Connect(t *testing.T) {
	mc := setupMemoryCache()

	// it should connect
	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestMemoryCache_Store(t *testing.T) {
	mc := setupMemoryCache()

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = mc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}
}

func TestMemoryCache_Retrieve(t *testing.T) {
	mc := setupMemoryCache()

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	err = mc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	var data string
	data, err = mc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if data != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func TestMemoryCache_ReapOnce(t *testing.T) {
	mc := setupMemoryCache()

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	// fake an expired entry
	mc.Store("cacheKey", "data", -1000)

	// fake a response channel to reap
	ch := make(chan *ClientRequestContext, 100)
	mc.T.ResponseChannels["cacheKey"] = ch

	// it should remove empty response channel
	mc.ReapOnce()

	if mc.T.ResponseChannels["cacheKey"] != nil {
		t.Errorf("expected response channel to be removed")
	}
}

func TestMemoryCache_Close(t *testing.T) {
	mc := setupMemoryCache()
	mc.Close()
}
