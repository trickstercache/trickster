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

package filesystem

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

const cacheType = "filesystem"
const cacheKey = "cacheKey"

func newCacheConfig(t *testing.T) config.CachingConfig {
	dir, err := ioutil.TempDir("/tmp", cacheType)
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	return config.CachingConfig{CacheType: cacheType, Filesystem: config.FilesystemCacheConfig{CachePath: dir}, Index: config.CacheIndexConfig{ReapInterval: time.Second}}
}

func TestConfiguration(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}
	cfg := fc.Configuration()
	if cfg.CacheType != cacheType {
		t.Fatalf("expected %s got %s", cacheType, cfg.CacheType)
	}
}

func TestFilesystemCache_Connect(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}

	// it should connect
	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestFilesystemCache_Store(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func TestFilesystemCache_StoreNoIndex(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer fc.Close()

	// it should store a value
	fc.storeNoIndex(cacheKey, []byte("data"))

	// it should retrieve a value
	data, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

}

func TestFilesystemCache_SetTTL(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	fc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer fc.Close()

	exp1 := fc.Index.GetExpiration(cacheKey)
	if !exp1.IsZero() {
		t.Errorf("expected Zero time, got %v", exp1)
	}

	// it should store a value
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	exp1 = fc.Index.GetExpiration(cacheKey)
	if exp1.IsZero() {
		t.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
	}

	e1 := int(exp1.Unix())

	fc.SetTTL(cacheKey, time.Duration(3600)*time.Second)

	exp2 := fc.Index.GetExpiration(cacheKey)
	if exp2.IsZero() {
		t.Errorf("expected time %d, got zero", int(time.Now().Unix())+3600)
	}
	e2 := int(exp2.Unix())

	// should be around 3595
	diff := e2 - e1
	const expected = 3500

	if diff < expected {
		t.Errorf("expected diff >= %d, got %d from: %d - %d", expected, diff, e2, e1)
	}

}

func TestFilesystemCache_Retrieve(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func TestFilesystemCache_Remove(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer fc.Close()

	// it should store a value
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	fc.Remove(cacheKey)

	// it should be a cache miss
	data, err = fc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestFilesystemCache_BulkRemove(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer fc.Close()

	// it should store a value
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	fc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	data, err = fc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}
