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

package memory

import (
	"io/ioutil"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

const cacheType = "memory"
const cacheKey = "cacheKey"

func newCacheConfig(t *testing.T) config.CachingConfig {
	dir, err := ioutil.TempDir("/tmp", cacheType)
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	return config.CachingConfig{Type: cacheType, Index: config.CacheIndexConfig{ReapInterval: 0}}
}

func TestConfiguration(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}
	cfg := mc.Configuration()
	if cfg.Type != cacheType {
		t.Fatalf("expected %s got %s", cacheType, cfg.Type)
	}
}

func TestCache_Connect(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	// it should connect
	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Store(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Retrieve(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	var data []byte
	data, err = mc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func TestCache_Close(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}
	mc.Close()
}

func TestCache_Remove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer mc.Close()

	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := mc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	mc.Remove(cacheKey)

	// it should be a cache miss
	_, err = mc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestCache_BulkRemove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer mc.Close()

	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := mc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	mc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	_, err = mc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}
