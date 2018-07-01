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
	"testing"

	"github.com/go-kit/kit/log"
)

func TestBoltDBCache_Connect(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	bc := BoltDBCache{T: &tr, Config: BoltDBCacheConfig{CachePath: ".", Filename: "test.db", Bucket: "trickster_test"}}

	// it should connect
	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}

	bc.Close()

}

func TestBoltDBCache_Store(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	bc := BoltDBCache{T: &tr, Config: BoltDBCacheConfig{CachePath: ".", Filename: "test.db", Bucket: "trickster_test"}}

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}
}

func TestBoltDBCache_Delete(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	bc := BoltDBCache{T: &tr, Config: BoltDBCacheConfig{CachePath: ".", Filename: "test.db", Bucket: "trickster_test"}}

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = bc.Delete("cacheKey")
	if err != nil {
		t.Error(err)
	}

}

func TestBoltDBCache_Retrieve(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	bc := BoltDBCache{T: &tr, Config: BoltDBCacheConfig{CachePath: ".", Filename: "test.db", Bucket: "trickster_test"}}

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	err = bc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := bc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if data != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}
