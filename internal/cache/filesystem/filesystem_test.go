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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/cache/status"
	"github.com/Comcast/trickster/internal/config"
	tl "github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init(&config.TricksterConfig{}, tl.ConsoleLogger("error"))
}

const cacheType = "filesystem"
const cacheKey = "cacheKey"

func storeBenchmark(b *testing.B) Cache {
	dir, _ := ioutil.TempDir("/tmp", cacheType)
	cacheConfig := config.CachingConfig{CacheType: cacheType, Filesystem: config.FilesystemCacheConfig{CachePath: dir}, Index: config.CacheIndexConfig{ReapInterval: time.Second}}
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}
	defer os.RemoveAll(cacheConfig.BBolt.Filename)

	err := fc.Connect()
	if err != nil {
		b.Error(err)
	}

	// it should store a value
	for n := 0; n < b.N; n++ {
		err = fc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return fc
}

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
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}
	cfg := fc.Configuration()
	if cfg.CacheType != cacheType {
		t.Fatalf("expected %s got %s", cacheType, cfg.CacheType)
	}
}

func TestFilesystemCache_Connect(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

	// it should connect
	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestFilesystemCache_ConnectFailed(t *testing.T) {
	const expected = `[/root/noaccess.trickster.filesystem.cache] directory is not writeable by trickster:`
	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = "/root/noaccess.trickster.filesystem.cache"
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}
	// it should connect
	err := fc.Connect()
	if err == nil {
		t.Errorf("expected error for %s", expected)
		fc.Close()
	}
	if !strings.HasPrefix(err.Error(), expected) {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
	}
}

func TestFilesystemCache_Store(t *testing.T) {

	const expected1 = "invalid ttl: -1"
	const expected2 = "open /root/noaccess.trickster.filesystem.cache/cacheKey.data:"

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should return an error
	err = fc.Store(cacheKey, []byte("data"), time.Duration(-1)*time.Second)
	if err == nil {
		t.Errorf("expected error for %s", expected1)
	}
	if err.Error() != expected1 {
		t.Errorf("expected error '%s' got '%s'", expected1, err.Error())
	}

	cacheConfig.Filesystem.CachePath = "/root/noaccess.trickster.filesystem.cache"
	// it should return an error
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err == nil {
		t.Errorf("expected error for %s", expected2)
	}
	if !strings.HasPrefix(err.Error(), expected2) {
		t.Errorf("expected error '%s' got '%s'", expected2, err.Error())
	}

}

func BenchmarkCache_Store(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()
}

func TestFilesystemCache_StoreNoIndex(t *testing.T) {

	const expected = "value for key [] not in cache"

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer fc.Close()

	// it should store a value
	fc.storeNoIndex(cacheKey, []byte("data"))

	// it should retrieve a value
	data, ls, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	// test for error when bad key name
	fc.storeNoIndex("", []byte("data"))

	data, ls, err = fc.retrieve("", false, false)
	if err == nil {
		t.Errorf("expected error for %s", expected)
		fc.Close()
	}
	if err.Error() != expected {
		t.Errorf("expected error '%s' got '%s'", expected, err.Error())
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func BenchmarkCache_StoreNoIndex(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()
	for n := 0; n < b.N; n++ {
		expected := `value for key [] not in cache`
		// it should store a value
		fc.storeNoIndex(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)))

		// it should retrieve a value
		data, ls, err := fc.retrieve(cacheKey+strconv.Itoa(n), false, false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}
		// test for error when bad key name
		fc.storeNoIndex("", []byte("data"+strconv.Itoa(n)))

		data, ls, err = fc.retrieve("", false, false)
		if err == nil {
			b.Errorf("expected error for %s", expected)
			fc.Close()
		}
		if err.Error() != expected {
			b.Errorf("expected error '%s' got '%s'", expected, err.Error())
		}
		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}

func TestFilesystemCache_SetTTL(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}
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

func BenchmarkCache_SetTTL(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()
	for n := 0; n < b.N; n++ {
		exp1 := fc.Index.GetExpiration(cacheKey + strconv.Itoa(n))
		if exp1.IsZero() {
			b.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
		}

		e1 := int(exp1.Unix())

		fc.SetTTL(cacheKey+strconv.Itoa(n), time.Duration(3600)*time.Second)

		exp2 := fc.Index.GetExpiration(cacheKey + strconv.Itoa(n))
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

func TestFilesystemCache_Retrieve(t *testing.T) {

	const expected1 = `value for key [cacheKey] not in cache`
	const expected2 = `value for key [cacheKey] could not be deserialized from cache`

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := fc.Retrieve(cacheKey, false)
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
	idx := fc.Index
	fc.Index = nil

	data, ls, err = fc.Retrieve(cacheKey, false)
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
	fc.Index = idx

	// expire the object
	fc.SetTTL(cacheKey, -1*time.Hour)

	// this should now return error
	data, ls, err = fc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected error for %s", expected1)
		fc.Close()
	}
	if err.Error() != expected1 {
		t.Errorf("expected error '%s' got '%s'", expected1, err.Error())
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}

	// should fail
	filename := fc.getFileName(cacheKey)
	err = ioutil.WriteFile(filename, []byte("junk"), os.FileMode(0777))
	if err != nil {
		t.Error(err)
	}
	_, ls, err = fc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected error for %s", expected2)
	}
	if err.Error() != expected2 {
		t.Errorf("expected error '%s' got '%s'", expected2, err.Error())
	}
	if ls != status.LookupStatusError {
		t.Errorf("expected %s got %s", status.LookupStatusError, ls)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()

	for n := 0; n < b.N; n++ {
		expected1 := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		expected2 := `value for key [` + cacheKey + strconv.Itoa(n) + `] could not be deserialized from cache`

		data, ls, err := fc.Retrieve(cacheKey+strconv.Itoa(n), false)
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
		fc.SetTTL(cacheKey+strconv.Itoa(n), -1*time.Hour)

		// this should now return error
		data, ls, err = fc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected error for %s", expected1)
			fc.Close()
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

		filename := fc.getFileName(cacheKey + strconv.Itoa(n))
		// create a corrupted cache entry and expect an error
		ioutil.WriteFile(filename, []byte("junk"), os.FileMode(0777))

		// it should fail to retrieve a value
		data, ls, err = fc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected error for %s", expected2)
			fc.Close()
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

func TestFilesystemCache_Remove(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

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
	data, ls, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	fc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = fc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func BenchmarkCache_Remove(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()

	for n := 0; n < b.N; n++ {
		var data []byte
		data, ls, err := fc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		fc.Remove(cacheKey + strconv.Itoa(n))

		// this should now return error
		data, ls, err = fc.Retrieve(cacheKey+strconv.Itoa(n), false)
		expectederr := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		if err == nil {
			b.Errorf("expected error for %s", expectederr)
			fc.Close()
		}
		if err.Error() != expectederr {
			b.Errorf("expected error '%s' got '%s'", expectederr, err.Error())
		}

		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}

func TestFilesystemCache_BulkRemove(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	defer os.RemoveAll(cacheConfig.Filesystem.CachePath)
	fc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

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
	data, ls, err := fc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	fc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	_, ls, err = fc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func BenchmarkCache_BulkRemove(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()

	var keyArray []string
	for n := 0; n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	fc.BulkRemove(keyArray, true)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := fc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}
