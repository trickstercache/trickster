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

const cacheProvider = "bbolt"
const cacheKey = "cacheKey"

func newCacheConfig(dbPath string) co.Options {
	return co.Options{Provider: cacheProvider, BBolt: &bo.Options{
		Filename: dbPath, Bucket: "trickster_test"}, Index: &io.Options{ReapInterval: time.Second}}
}

func storeBenchmark(b *testing.B) CacheClient {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	testDbPath := b.TempDir() + "/test.db"
	cacheConfig := co.Options{
		Provider: cacheProvider,
		BBolt:    &bo.Options{Filename: testDbPath, Bucket: "trickster_test"},
		Index:    &io.Options{ReapInterval: time.Second},
	}
	bc := New(b.Name(), "", "", &cacheConfig)

	err := bc.Connect()
	if err != nil {
		b.Error(err)
	}

	// it should store a value
	for n := 0; n < b.N; n++ {
		err = bc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return *bc
}

func TestBboltCache_Connect(t *testing.T) {
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

func TestBboltCache_ConnectFailed(t *testing.T) {
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

func TestBboltCache_ConnectBadBucketName(t *testing.T) {
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

func TestBboltCache_Store(t *testing.T) {
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
	bc := storeBenchmark(b)
	defer bc.Close()
}

func TestBboltCache_Remove(t *testing.T) {
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
	bc := storeBenchmark(b)
	defer bc.Close()
	for n := 0; n < b.N; n++ {
		var data []byte
		data, ls, err := bc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		bc.Remove(cacheKey + strconv.Itoa(n))

		// this should now return error
		data, ls, err = bc.Retrieve(cacheKey + strconv.Itoa(n))
		expectederr := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		if err == nil {
			b.Errorf("expected error for %s", expectederr)
			bc.Close()
		}
		if err.Error() != expectederr {
			b.Errorf("expected error '%s' got '%s'", expectederr, err.Error())
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}
	}
}

func TestBboltCache_BulkRemove(t *testing.T) {
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
	bc := storeBenchmark(b)
	defer bc.Close()

	var keyArray []string
	for n := 0; n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	bc.Remove(keyArray...)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := bc.Retrieve(cacheKey + strconv.Itoa(n))
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}

func TestBboltCache_Retrieve(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	const expected1 = `value for key [cacheKey] not in cache`
	const expected2 = `value for key [cacheKey-invalid] could not be deserialized from cache`

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
	bc := storeBenchmark(b)
	defer bc.Close()

	for n := 0; n < b.N; n++ {
		expected2 := `value for key [` + cacheKey + strconv.Itoa(n) + `] could not be deserialized from cache`

		data, ls, err := bc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		// create a corrupted cache entry and expect an error
		writeToBBolt(bc.dbh, bc.Config.BBolt.Bucket, cacheKey+strconv.Itoa(n), []byte("asdasdfasf"+strconv.Itoa(n)))

		// it should fail to retrieve a value
		data, ls, err = bc.Retrieve(cacheKey + strconv.Itoa(n))
		if err == nil {
			b.Errorf("expected error for %s", expected2)
			bc.Close()
		}
		if err.Error() != expected2 {
			b.Errorf("expected error '%s' got '%s'", expected2, err.Error())
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
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
