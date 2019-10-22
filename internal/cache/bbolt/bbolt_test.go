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
	"strconv"
	"strings"
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

func storeBenchmark(b *testing.B) Cache {
	testDbPath := "/tmp/test.db"
	os.Remove(testDbPath)
	cacheConfig := config.CachingConfig{CacheType: cacheType, BBolt: config.BBoltCacheConfig{Filename: testDbPath, Bucket: "trickster_test"}, Index: config.CacheIndexConfig{ReapInterval: time.Second}}
	bc := Cache{Config: &cacheConfig}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := bc.Connect()
	if err != nil {
		b.Error(err)
	}

	// it should store a value
	for n:=0; n < b.N; n++ {
		err = bc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return bc
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

func TestBboltCache_ConnectFailed(t *testing.T) {
	const expected = `open /root/noaccess.bbolt:`
	cacheConfig := newCacheConfig()
	cacheConfig.BBolt.Filename = "/root/noaccess.bbolt"
	bc := Cache{Config: &cacheConfig}
	// it should connect
	err := bc.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected)
		bc.Close()
		defer os.RemoveAll(cacheConfig.BBolt.Filename)
	}
	if !strings.HasPrefix(err.Error(), expected) {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
	}
}

func TestBboltCache_ConnectBadBucketName(t *testing.T) {
	const expected = `create bucket: bucket name required`
	cacheConfig := newCacheConfig()
	cacheConfig.BBolt.Bucket = ""
	defer os.RemoveAll(cacheConfig.BBolt.Filename)
	bc := Cache{Config: &cacheConfig}
	// it should connect
	err := bc.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected)
		bc.Close()
	}
	if err.Error() != expected {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
	}
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

func BenchmarkCache_Store(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()
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

func BenchmarkCache_SetTTL(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()
	for n:= 0;n < b.N; n++ {
		exp1 := bc.Index.GetExpiration(cacheKey+strconv.Itoa(n))
		if exp1.IsZero() {
			b.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
		}

		e1 := int(exp1.Unix())

		bc.SetTTL(cacheKey+strconv.Itoa(n), time.Duration(3600)*time.Second)

		exp2 := bc.Index.GetExpiration(cacheKey+strconv.Itoa(n))
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

	// test for error when bad key name
	bc.storeNoIndex("", []byte("data"))

	data, err = bc.retrieve("", false, false)
	if err == nil {
		t.Errorf("expected error for %s", expected)
		bc.Close()
	}
	if err.Error() != expected {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
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
		data, err := bc.retrieve(cacheKey+strconv.Itoa(n), false, false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}

		// test for error when bad key name
		bc.storeNoIndex("", []byte("data"+strconv.Itoa(n)))

		data, err = bc.retrieve("", false, false)
		if err == nil {
			b.Errorf("expected error for %s", expected)
			bc.Close()
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

func BenchmarkCache_Remove(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()
	for n:= 0;n < b.N; n++ {
		var data []byte
		data, err := bc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}

		bc.Remove(cacheKey+strconv.Itoa(n))

		// this should now return error
		data, err = bc.Retrieve(cacheKey+strconv.Itoa(n), false)
		expectederr := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		if err == nil {
			b.Errorf("expected error for %s", expectederr)
			bc.Close()
		}
		if err.Error() != expectederr {
			b.Errorf("expected error '%s' got '%s'", expectederr, err.Error())
		}

		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}
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

func BenchmarkCache_BulkRemove(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()

	var keyArray []string
	for n:= 0;n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	bc.BulkRemove(keyArray, true)

	// it should be a cache miss
	for n:= 0;n < b.N; n++ {
		_, err := bc.Retrieve(cacheKey + strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
	}
}

func TestBboltCache_Retrieve(t *testing.T) {

	const expected1 = `value for key [cacheKey] not in cache`
	const expected2 = `value for key [cacheKey] could not be deserialized from cache`

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

	// expire the object
	bc.SetTTL(cacheKey, -1*time.Hour)

	// this should now return error
	data, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected error for %s", expected1)
		bc.Close()
	}
	if err.Error() != expected1 {
		t.Errorf("expected error '%s' got '%s'", expected1, err.Error())
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	// create a corrupted cache entry and expect an error
	writeToBBolt(bc.dbh, cacheConfig.BBolt.Bucket, cacheKey, []byte("asdasdfasf"))

	// it should fail to retrieve a value
	data, err = bc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected error for %s", expected2)
		bc.Close()
	}
	if err.Error() != expected2 {
		t.Errorf("expected error '%s' got '%s'", expected2, err.Error())
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	bc := storeBenchmark(b)
	defer bc.Close()

	for n:=0; n<b.N; n++ {
		expected1 := `value for key [`+cacheKey+strconv.Itoa(n)+`] not in cache`
		expected2 := `value for key [`+cacheKey+strconv.Itoa(n)+`] could not be deserialized from cache`

		data, err := bc.Retrieve(cacheKey+ strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+ strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+ strconv.Itoa(n), data)
		}

		// expire the object
		bc.SetTTL(cacheKey+ strconv.Itoa(n), -1*time.Hour)

		// this should now return error
		data, err = bc.Retrieve(cacheKey+ strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected error for %s", expected1)
			bc.Close()
		}
		if err.Error() != expected1 {
			b.Errorf("expected error '%s' got '%s'", expected1, err.Error())
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+ strconv.Itoa(n), data)
		}

		// create a corrupted cache entry and expect an error
		writeToBBolt(bc.dbh, bc.Config.BBolt.Bucket, cacheKey+ strconv.Itoa(n), []byte("asdasdfasf"+ strconv.Itoa(n)))

		// it should fail to retrieve a value
		data, err = bc.Retrieve(cacheKey+ strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected error for %s", expected2)
			bc.Close()
		}
		if err.Error() != expected2 {
			b.Errorf("expected error '%s' got '%s'", expected2, err.Error())
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+ strconv.Itoa(n), data)
		}
	}
}