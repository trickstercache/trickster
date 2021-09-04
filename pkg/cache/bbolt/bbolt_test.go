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

	"github.com/trickstercache/trickster/v2/pkg/cache"
	bo "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

const cacheProvider = "bbolt"
const cacheKey = "cacheKey"

func newCacheConfig(dbPath string) co.Options {
	return co.Options{Provider: cacheProvider, BBolt: &bo.Options{
		Filename: dbPath, Bucket: "trickster_test"}, Index: &io.Options{ReapInterval: time.Second}}
}

func storeBenchmark(b *testing.B) Cache {
	testDbPath := b.TempDir() + "/test.db"
	cacheConfig := co.Options{
		Provider: cacheProvider,
		BBolt:    &bo.Options{Filename: testDbPath, Bucket: "trickster_test"},
		Index:    &io.Options{ReapInterval: time.Second},
	}
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

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
	return bc
}

func TestConfiguration(t *testing.T) {
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}
	cfg := bc.Configuration()
	if cfg.Provider != cacheProvider {
		t.Errorf("expected %s got %s", cacheProvider, cfg.Provider)
	}
}

func TestBboltCache_Connect(t *testing.T) {
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}
	// it should connect
	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	bc.Close()
}

func TestBboltCache_ConnectFailed(t *testing.T) {
	const expected = `open /root/noaccess.bbolt:`
	cacheConfig := newCacheConfig("/root/noaccess.bbolt")
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}
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
	const expected = `create bucket: bucket name required`
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	cacheConfig.BBolt.Bucket = ""
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}
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

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

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

func TestBboltCache_SetTTL(t *testing.T) {

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

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

	time.Sleep(time.Millisecond * 10)

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

func BenchmarkCache_SetTTL(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()
	for n := 0; n < b.N; n++ {
		exp1 := bc.Index.GetExpiration(cacheKey + strconv.Itoa(n))
		if exp1.IsZero() {
			b.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
		}

		e1 := int(exp1.Unix())

		bc.SetTTL(cacheKey+strconv.Itoa(n), time.Duration(3600)*time.Second)

		exp2 := bc.Index.GetExpiration(cacheKey + strconv.Itoa(n))
		if exp2.IsZero() {
			b.Errorf("expected time %d, got zero", int(time.Now().Unix())+3600)
		}
		e2 := int(exp2.Unix())

		// should be around 3595
		diff := e2 - e1
		const expected = 3500

		if diff < expected {
			b.Errorf("expected diff >= %d, got %d from: %d - %d", expected, diff, e2, e1)
		}
	}
}

func TestBboltCache_StoreNoIndex(t *testing.T) {

	const expected = `value for key [] not in cache`

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

	err := bc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer bc.Close()

	// it should store a value
	bc.storeNoIndex(cacheKey, []byte("data"))

	// it should retrieve a value
	data, ls, err := bc.retrieve(cacheKey, false, false)
	if err != nil {
		t.Error(err)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	// test for error when bad key name
	bc.storeNoIndex("", []byte("data"))

	data, ls, err = bc.retrieve("", false, false)
	if err == nil {
		t.Errorf("expected error for %s", expected)
		bc.Close()
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
	if err != cache.ErrKNF {
		t.Error("expected error for KNF")
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func BenchmarkCache_StoreNoIndex(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()
	for n := 0; n < b.N; n++ {
		expected := `value for key [] not in cache`
		// it should store a value
		bc.storeNoIndex(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)))

		// it should retrieve a value
		data, ls, err := bc.retrieve(cacheKey+strconv.Itoa(n), false, false)
		if err != nil {
			b.Error(err)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}

		// test for error when bad key name
		bc.storeNoIndex("", []byte("data"+strconv.Itoa(n)))

		data, ls, err = bc.retrieve("", false, false)
		if err == nil {
			b.Errorf("expected error for %s", expected)
			bc.Close()
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
		if err.Error() != expected {
			b.Errorf("expected error '%s' got '%s'", expected, err.Error())
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
	}
}

func TestBboltCache_Remove(t *testing.T) {

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

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
	data, ls, err := bc.Retrieve(cacheKey, false)
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
	_, ls, err = bc.Retrieve(cacheKey, false)
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
		data, ls, err := bc.Retrieve(cacheKey+strconv.Itoa(n), false)
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
		data, ls, err = bc.Retrieve(cacheKey+strconv.Itoa(n), false)
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

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

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
	data, ls, err := bc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
	bc.BulkRemove([]string{cacheKey})

	// it should be a cache miss
	_, ls, err = bc.Retrieve(cacheKey, false)
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

	bc.BulkRemove(keyArray)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := bc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}

func TestBboltCache_Retrieve(t *testing.T) {

	const expected1 = `value for key [cacheKey] not in cache`
	const expected2 = `value for key [cacheKey-invalid] could not be deserialized from cache`

	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}

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
	data, ls, err := bc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	// it should still retrieve a value with nil index
	idx := bc.Index
	bc.Index = nil

	data, ls, err = bc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	// restore the index for further tests
	bc.Index = idx

	// expire the object
	bc.SetTTL(cacheKey, -1*time.Hour)

	time.Sleep(time.Millisecond * 10)

	// this should now return error
	data, ls, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected error for %s", expected1)
		bc.Close()
	}
	if err != cache.ErrKNF {
		t.Error("expected error for KNF")
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}

	// create a corrupted cache entry and expect an error
	writeToBBolt(bc.dbh, cacheConfig.BBolt.Bucket, cacheKey+"-invalid", []byte("asdasdfasf"))

	// it should fail to retrieve a value
	data, ls, err = bc.Retrieve(cacheKey+"-invalid", false)
	if err == nil || err.Error() != expected2 {
		t.Errorf("expected error '%s' got '%s'", expected2, err)
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusError {
		t.Errorf("expected %s got %s", status.LookupStatusError, ls)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()

	for n := 0; n < b.N; n++ {
		expected1 := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		expected2 := `value for key [` + cacheKey + strconv.Itoa(n) + `] could not be deserialized from cache`

		data, ls, err := bc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		// expire the object
		bc.SetTTL(cacheKey+strconv.Itoa(n), -1*time.Hour)

		// this should now return error
		data, ls, err = bc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected error for %s", expected1)
			bc.Close()
		}
		if err.Error() != expected1 {
			b.Errorf("expected error '%s' got '%s'", expected1, err.Error())
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}

		// create a corrupted cache entry and expect an error
		writeToBBolt(bc.dbh, bc.Config.BBolt.Bucket, cacheKey+strconv.Itoa(n), []byte("asdasdfasf"+strconv.Itoa(n)))

		// it should fail to retrieve a value
		data, ls, err = bc.Retrieve(cacheKey+strconv.Itoa(n), false)
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

func TestLocker(t *testing.T) {
	cache := Cache{locker: locks.NewNamedLocker()}
	l := cache.Locker()
	cache.SetLocker(locks.NewNamedLocker())
	m := cache.Locker()
	if l == m {
		t.Errorf("error setting locker")
	}
}

func TestClose(t *testing.T) {
	testDbPath := t.TempDir() + "/test.db"
	cacheConfig := newCacheConfig(testDbPath)
	bc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: locks.NewNamedLocker()}
	bc.dbh = nil
	err := bc.Close()
	if err != nil {
		t.Error(err)
	}
}
