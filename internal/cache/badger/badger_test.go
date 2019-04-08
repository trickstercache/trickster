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

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

func TestBadgerCache_Connect(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	// it should connect
	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBadgerCache_Store(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	err = bc.Store("cacheKey", []byte("data"), 60000)
	if err != nil {
		t.Error(err)
	}
}

func TestBadgerCache_Retrieve(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	err = bc.Store("cacheKey", []byte("data"), 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, err := bc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func TestBadgerCache_Close(t *testing.T) {
	dir, err := ioutil.TempDir("/tmp", "badger")
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	defer os.RemoveAll(dir)

	cacheConfig := config.CachingConfig{Type: "badger", Badger: config.BadgerCacheConfig{Directory: dir, ValueDirectory: dir}}
	bc := Cache{Config: &cacheConfig}

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}

	// it should close
	if err := bc.Close(); err != nil {
		t.Error(err)
	}
}
