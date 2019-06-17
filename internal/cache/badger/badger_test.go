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

package badger

import (
	"io/ioutil"
	"os"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

const cacheType = "badger"
const cacheKey = "cacheKey"

func newCacheConfig(t *testing.T) config.CachingConfig {
	dir, err := ioutil.TempDir("/tmp", cacheType)
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	return config.CachingConfig{CacheType: cacheType, Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
}

func TestConfiguration(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Badger.Directory)
	bc := Cache{Config: &cacheConfig}

	cfg := bc.Configuration()
	if cfg.CacheType != cacheType {
		t.Fatalf("expected %s got %s", cacheType, cfg.CacheType)
	}
}

func TestBadgerCache_Connect(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Badger.Directory)
	bc := Cache{Config: &cacheConfig}

	// it should connect
	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBadgerCache_Store(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Badger.Directory)
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err := bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func TestBadgerCache_Remove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Badger.Directory)
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err := bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := bc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	bc.Remove(cacheKey)

	// it should be a cache miss
	data, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestBadgerCache_BulkRemove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Badger.Directory)
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err := bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := bc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	bc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	data, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestBadgerCache_Retrieve(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Badger.Directory)
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should be a cache miss
	data, err := bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

	err = bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err = bc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func TestBadgerCache_Close(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", cacheType)
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{CacheType: cacheType, Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}

	// it should close
	if err := bc.Close(); err != nil {
		t.Error(err)
	}
}
