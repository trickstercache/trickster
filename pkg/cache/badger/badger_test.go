/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package badger

import (
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/cache/badger/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const (
	provider = "badger"
	cacheKey = "cacheKey"
)

func newCacheConfig(dbPath string) *co.Options {
	return &co.Options{
		Provider: provider,
		Badger:   &bo.Options{Directory: dbPath, ValueDirectory: dbPath},
	}
}

func TestBadgerCache_Connect(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), cacheConfig)

	// it should connect
	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBadgerCache_ConnectFailed(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig("/root/trickster-test-noaccess")
	bc := New(t.Name(), cacheConfig)

	// it should connect
	err := bc.Connect()
	if err == nil {
		t.Errorf("expected file access error for %s", cacheConfig.Badger.Directory)
		bc.Close()
	}
}

func TestBadgerCache_Store(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), cacheConfig)

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
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), cacheConfig)

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
	data, ls, err := bc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	bc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = bc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func TestBadgerCache_BulkRemove(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), cacheConfig)

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
	data, ls, err := bc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	bc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = bc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func TestBadgerCache_Retrieve(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), cacheConfig)

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should be a cache miss
	_, ls, err := bc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}

	err = bc.Store(cacheKey, []byte("data"), time.Duration(5)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := bc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func TestBadgerCache_Close(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := &co.Options{Provider: provider, Badger: &bo.Options{Directory: testDbPath, ValueDirectory: testDbPath}}
	bc := New(t.Name(), cacheConfig)

	if err := bc.Connect(); err != nil {
		t.Error(err)
	}

	// it should close
	if err := bc.Close(); err != nil {
		t.Error(err)
	}
}
