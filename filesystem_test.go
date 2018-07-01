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

func TestFilesystemCache_Connect(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	fc := FilesystemCache{T: &tr, Config: FilesystemCacheConfig{CachePath: "."}}

	// it should connect
	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestFilesystemCache_Store(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	fc := FilesystemCache{T: &tr, Config: FilesystemCacheConfig{CachePath: "."}}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = fc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}
}

func TestFilesystemCache_Retrieve(t *testing.T) {
	cfg := Config{Caching: CachingConfig{ReapSleepMS: 1}}
	tr := TricksterHandler{Logger: log.NewNopLogger(), Config: &cfg}
	fc := FilesystemCache{T: &tr, Config: FilesystemCacheConfig{CachePath: "."}}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	err = fc.Store("cacheKey", "data", 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := fc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if data != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}
