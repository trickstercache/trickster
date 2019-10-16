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

package bbolt

import (
	"os"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

const cacheType = "bbolt"
const cacheKey = "cacheKey"

func newCacheConfig() config.CachingConfig {
	const testDbPath = "/tmp/test.db"
	os.Remove(testDbPath)
	return config.CachingConfig{CacheType: cacheType, BBolt: config.BBoltCacheConfig{Filename: testDbPath, Bucket: "trickster_test"}, Index: config.CacheIndexConfig{ReapInterval: time.Second}}
}

func TestConfiguration(t *testing.T) {
	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}
	cfg := bc.Configuration()
	if cfg.CacheType != cacheType {
		t.Fatalf("expected %s got %s", cacheType, cfg.CacheType)
	}
}

func TestBboltCache_Connect(t *testing.T) {
	cacheConfig := newCacheConfig()
	defer os.RemoveAll(cacheConfig.BBolt.Filename)
	bc := Cache{Config: &cacheConfig}
	// it should connect
	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBboltCache_Store(t *testing.T) {

	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func TestBboltCache_SetTTL(t *testing.T) {

	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	exp1 := bc.Index.GetExpiration(cacheKey)
	if !exp1.IsZero() {
		t.Errorf("expected Zero time, got %v", exp1)
	}

	// it should store a value
	err = bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	exp1 = bc.Index.GetExpiration(cacheKey)
	if exp1.IsZero() {
		t.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
	}

	e1 := int(exp1.Unix())

	bc.SetTTL(cacheKey, time.Duration(3600)*time.Second)

	exp2 := bc.Index.GetExpiration(cacheKey)
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

func TestBboltCache_StoreNoIndex(t *testing.T) {

	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	bc.storeNoIndex(cacheKey, []byte("data"))

	// it should retrieve a value
	data, err := bc.retrieve(cacheKey, false, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

}

func TestBboltCache_Remove(t *testing.T) {

	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
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
	_, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestBboltCache_BulkRemove(t *testing.T) {

	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
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
	_, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func TestBboltCache_Retrieve(t *testing.T) {

	cacheConfig := newCacheConfig()
	bc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	err = bc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
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
}
