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

package bbolt

import (
	"strconv"
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const (
	cacheProvider     = "bbolt"
	cacheKey          = "cacheKey"
	benchmarkKeyCount = 10
)

func newCacheConfig(dbPath string) co.Options {
	return co.Options{Provider: cacheProvider, BBolt: &bo.Options{
		Filename: dbPath, Bucket: "trickster_test",
	}, Index: &io.Options{ReapInterval: time.Second}}
}

func setupBenchmark(b *testing.B) *CacheClient {
	b.Helper()
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := b.TempDir() + "/test.db"
	cacheConfig := co.Options{
		Provider: cacheProvider,
		BBolt:    &bo.Options{Filename: testDbPath, Bucket: "trickster_test"},
		Index:    &io.Options{ReapInterval: time.Second},
	}
	bc := New(b.Name(), "", "", &cacheConfig)

	if err := bc.Connect(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := bc.Close(); err != nil {
			b.Error(err)
		}
	})
	return bc
}

func TestBBoltCache_Connect(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), "", "", &cacheConfig)
	// it should connect
	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBBoltCache_ConnectFailed(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const expected = `open /root/noaccess.bbolt:`
	cacheConfig := newCacheConfig("/root/noaccess.bbolt")
	bc := New(t.Name(), "", "", &cacheConfig)
	// it should connect
	err := bc.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected)
		bc.Close()
	}
	if !strings.HasPrefix(err.Error(), expected) {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
	}
}

func TestBBoltCache_ConnectBadBucketName(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const expected = `create bucket: bucket name required`
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	cacheConfig.BBolt.Bucket = ""
	bc := New(t.Name(), "", "", &cacheConfig)
	// it should connect
	err := bc.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected)
		bc.Close()
	} else if err.Error() != expected {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
	}
}

func TestBBoltCache_Store(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), "", "", &cacheConfig)

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

func BenchmarkCache_Store(b *testing.B) {
	bc := setupBenchmark(b)
	n := 0
	for b.Loop() {
		suffix := strconv.Itoa(n)
		if err := bc.Store(cacheKey+suffix, []byte("data"+suffix), time.Minute); err != nil {
			b.Fatal(err)
		}
		n++
	}
}

func TestBBoltCache_Remove(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), "", "", &cacheConfig)

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

func BenchmarkCache_Remove(b *testing.B) {
	bc := setupBenchmark(b)
	for b.Loop() {
		b.StopTimer()
		if err := bc.Store(cacheKey, []byte("data"), time.Minute); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if err := bc.Remove(cacheKey); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBBoltCache_BulkRemove(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), "", "", &cacheConfig)

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

func BenchmarkCache_BulkRemove(b *testing.B) {
	bc := setupBenchmark(b)
	keys := make([]string, benchmarkKeyCount)
	values := make([][]byte, benchmarkKeyCount)
	for n := range benchmarkKeyCount {
		suffix := strconv.Itoa(n)
		keys[n] = cacheKey + suffix
		values[n] = []byte("data" + suffix)
	}

	for b.Loop() {
		b.StopTimer()
		for n, key := range keys {
			if err := bc.Store(key, values[n], time.Minute); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		if err := bc.Remove(keys...); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(benchmarkKeyCount, "keys/op")
}

func TestBBoltCache_Retrieve(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), "", "", &cacheConfig)

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
}

func BenchmarkCache_Retrieve(b *testing.B) {
	bc := setupBenchmark(b)
	if err := bc.Store(cacheKey, []byte("data"), time.Minute); err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		data, ls, err := bc.Retrieve(cacheKey)
		if err != nil {
			b.Fatal(err)
		}
		if string(data) != "data" {
			b.Fatalf("wanted %q, got %q", "data", data)
		}
		if ls != status.LookupStatusHit {
			b.Fatalf("expected %s, got %s", status.LookupStatusHit, ls)
		}
	}
}

func TestClose(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := New(t.Name(), "", "", &cacheConfig)
	bc.dbh = nil
	err := bc.Close()
	if err != nil {
		t.Error(err)
	}
}
