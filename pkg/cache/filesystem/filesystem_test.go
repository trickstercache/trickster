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

package filesystem

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	flo "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const cacheProvider = "filesystem"
const cacheKey = "cacheKey"

func storeBenchmark(b *testing.B) CacheClient {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	dir := b.TempDir() + "/cache/" + cacheProvider
	cacheConfig := co.Options{Provider: cacheProvider,
		Filesystem: &flo.Options{CachePath: dir}, Index: &io.Options{ReapInterval: time.Second}}
	fc := NewCache(b.Name(), &cacheConfig)

	err := fc.Connect()
	if err != nil {
		b.Error(err)
	}

	// it should store a value
	for n := 0; n < b.N; n++ {
		err = fc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)),
			time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return *fc
}

func newCacheConfig(t *testing.T) co.Options {
	dir := t.TempDir() + "/cache/" + cacheProvider
	return co.Options{Provider: cacheProvider, Filesystem: &flo.Options{CachePath: dir},
		Index: &io.Options{ReapInterval: time.Second}}
}

func TestFilesystemCache_Connect(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = t.TempDir() + "/cache"
	fc := NewCache(t.Name(), &cacheConfig)

	// it should connect
	err := fc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestFilesystemCache_ConnectFailed(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const expected = `[/root/noaccess.trickster.filesystem.cache] directory is not writeable by trickster:`
	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = "/root/noaccess.trickster.filesystem.cache"
	fc := NewCache(t.Name(), &cacheConfig)
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
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	const expected1 = "invalid ttl: -1"
	const expected2 = "open /root/noaccess.trickster.filesystem.cache/cacheKeydata:"

	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = t.TempDir() + "/cache"
	fc := NewCache(t.Name(), &cacheConfig)

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

func TestFilesystemCache_Retrieve(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = t.TempDir() + "/cache"
	fc := NewCache(t.Name(), &cacheConfig)

	err := fc.Connect()
	require.NoError(t, err)

	err = fc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	require.NoError(t, err)

	// it should retrieve a value
	data, ls, err := fc.Retrieve(cacheKey)
	require.NoError(t, err)
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	require.Equal(t, status.LookupStatusHit, ls)

	data, ls, err = fc.Retrieve(cacheKey)
	require.NoError(t, err)

	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	fc := storeBenchmark(b)
	defer fc.Close()

	for n := 0; n < b.N; n++ {
		expected1 := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		expected2 := `value for key [` + cacheKey + strconv.Itoa(n) + `] could not be deserialized from cache`

		data, ls, err := fc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\".", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		// this should now return error
		data, ls, err = fc.Retrieve(cacheKey + strconv.Itoa(n))
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
		os.WriteFile(filename, []byte("junk"), os.FileMode(0777))

		// it should fail to retrieve a value
		data, ls, err = fc.Retrieve(cacheKey + strconv.Itoa(n))
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
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = t.TempDir() + "/cache"
	fc := NewCache(t.Name(), &cacheConfig)

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
	data, ls, err := fc.Retrieve(cacheKey)
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
	_, ls, err = fc.Retrieve(cacheKey)
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
		data, ls, err := fc.Retrieve(cacheKey + strconv.Itoa(n))
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
		data, ls, err = fc.Retrieve(cacheKey + strconv.Itoa(n))
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
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig(t)
	cacheConfig.Filesystem.CachePath = t.TempDir() + "/cache"
	fc := NewCache(t.Name(), &cacheConfig)

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
	data, ls, err := fc.Retrieve(cacheKey)
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
	_, ls, err = fc.Retrieve(cacheKey)
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

	fc.Remove(keyArray...)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := fc.Retrieve(cacheKey + strconv.Itoa(n))
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}
