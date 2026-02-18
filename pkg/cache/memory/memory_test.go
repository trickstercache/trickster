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

package memory

import (
	"strconv"
	"testing"
	"time"

	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const (
	provider = "memory"
	cacheKey = "cacheKey"
)

type testReferenceObject struct{}

func (r *testReferenceObject) Size() int {
	return 1
}

func storeBenchmark(b *testing.B) *Cache {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := co.Options{Provider: provider, Index: &io.Options{ReapInterval: 0}}
	mc := New(b.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		b.Error(err)
	}
	// Note: don't close the cache here, callers use the cache after this for testing purposes
	for n := 0; n < b.N; n++ {
		err = mc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return mc
}

func newCacheConfig() co.Options {
	return co.Options{Provider: provider, Index: &io.Options{ReapInterval: 0}}
}

func TestCache_Connect(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	// it should connect
	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestCache_StoreReferenceDirect(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	// it should store a value
	mc.StoreReference("test", &testReferenceObject{}, 1*time.Second)

	r, _, _ := mc.RetrieveReference("test")
	if r == nil {
		t.Errorf("expected %s got nil", r)
	}

	_, _, err = mc.RetrieveReference("test2")
	if err == nil {
		t.Errorf("expected non-nil error")
	}
}

func TestCache_StoreReference(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	// it should store a value
	err = mc.StoreReference(cacheKey, nil, time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Store(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}
}

func BenchmarkCache_Store(b *testing.B) {
	storeBenchmark(b)
}

func TestCache_Retrieve(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	time.Sleep(time.Millisecond * 10)

	// it should retrieve a value
	var data []byte
	var ls status.LookupStatus
	data, ls, err = mc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	mc := storeBenchmark(b)

	for n := 0; n < b.N; n++ {
		var data []byte
		data, ls, err := mc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}
	}
}

func TestCache_Close(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)
	mc.Close()
}

func TestCache_Remove(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer mc.Close()

	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := mc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	mc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = mc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func BenchmarkCache_Remove(b *testing.B) {
	mc := storeBenchmark(b)

	for n := 0; n < b.N; n++ {
		var data []byte
		data, ls, err := mc.Retrieve(cacheKey + strconv.Itoa(n))
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		mc.Remove(cacheKey + strconv.Itoa(n))

		// this should now return error
		data, ls, err = mc.Retrieve(cacheKey + strconv.Itoa(n))
		expectederr := `key not found in cache` // cache.ErrKNF
		if err == nil {
			b.Errorf("expected error for %s", expectederr)
			mc.Close()
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

func TestCache_BulkRemove(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	cacheConfig := newCacheConfig()
	mc := New(t.Name(), &cacheConfig)

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer mc.Close()

	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	data, ls, err := mc.Retrieve(cacheKey)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	mc.Remove(cacheKey)

	// it should be a cache miss
	_, ls, err = mc.Retrieve(cacheKey)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func BenchmarkCache_BulkRemove(b *testing.B) {
	var keyArray []string
	for n := 0; n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	mc := storeBenchmark(b)

	mc.Remove(keyArray...)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := mc.Retrieve(cacheKey + strconv.Itoa(n))
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}
