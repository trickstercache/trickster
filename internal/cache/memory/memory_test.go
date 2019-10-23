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

package memory

import (
	"io/ioutil"
	"strconv"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

const cacheType = "memory"
const cacheKey = "cacheKey"

func storeBenchmark(b *testing.B) Cache {
	cacheConfig := config.CachingConfig{CacheType: cacheType, Index: config.CacheIndexConfig{ReapInterval: 0}}
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		b.Error(err)
	}
	defer mc.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		err = mc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return mc
}

func newCacheConfig(t *testing.T) config.CachingConfig {
	dir, err := ioutil.TempDir("/tmp", cacheType)
	if err != nil {
		t.Fatalf("could not create temp directory (%s): %s", dir, err)
	}
	return config.CachingConfig{CacheType: cacheType, Index: config.CacheIndexConfig{ReapInterval: 0}}
}

func TestConfiguration(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}
	cfg := mc.Configuration()
	if cfg.CacheType != cacheType {
		t.Fatalf("expected %s got %s", cacheType, cfg.CacheType)
	}
}

func TestCache_Connect(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	// it should connect
	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Store(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

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

	const expected1 = `value for key [cacheKey] not in cache`

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	var data []byte
	data, err = mc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}

	// expire the object
	mc.SetTTL(cacheKey, -1*time.Hour)

	// this should now return error
	data, err = mc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected error for %s", expected1)
		mc.Close()
	}
	if err.Error() != expected1 {
		t.Errorf("expected error '%s' got '%s'", expected1, err.Error())
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

}

func BenchmarkCache_Retrieve(b *testing.B) {
	mc := storeBenchmark(b)

	for n := 0; n < b.N; n++ {
		var data []byte
		data, err := mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}

		// expire the object
		mc.SetTTL(cacheKey+strconv.Itoa(n), -1*time.Hour)

		// this should now return error
		data, err = mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		expectederr := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		if err == nil {
			b.Errorf("expected error for %s", expectederr)
			mc.Close()
		}
		if err.Error() != expectederr {
			b.Errorf("expected error '%s' got '%s'", expectederr, err.Error())
		}

		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}
	}
}

func TestCache_Close(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}
	mc.Close()
}

func TestCache_Remove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

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
	data, err := mc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	mc.Remove(cacheKey)

	// it should be a cache miss
	_, err = mc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func BenchmarkCache_Remove(b *testing.B) {
	mc := storeBenchmark(b)

	for n := 0; n < b.N; n++ {
		var data []byte
		data, err := mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}

		mc.Remove(cacheKey + strconv.Itoa(n))

		// this should now return error
		data, err = mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		expectederr := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
		if err == nil {
			b.Errorf("expected error for %s", expectederr)
			mc.Close()
		}
		if err.Error() != expectederr {
			b.Errorf("expected error '%s' got '%s'", expectederr, err.Error())
		}

		if string(data) != "" {
			b.Errorf("wanted \"%s\". got \"%s\".", "data", data)
		}
	}
}

func TestCache_BulkRemove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

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
	data, err := mc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}

	mc.BulkRemove([]string{cacheKey}, true)

	// it should be a cache miss
	_, err = mc.Retrieve(cacheKey, false)
	if err == nil {
		t.Errorf("expected key not found error for %s", cacheKey)
	}

}

func BenchmarkCache_BulkRemove(b *testing.B) {
	var keyArray []string
	for n := 0; n < b.N; n++ {
		keyArray = append(keyArray, cacheKey+strconv.Itoa(n))
	}

	mc := storeBenchmark(b)

	mc.BulkRemove(keyArray, true)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, err := mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
	}
}

func TestMemoryCache_SetTTL(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	defer mc.Close()

	exp1 := mc.Index.GetExpiration(cacheKey)
	if !exp1.IsZero() {
		t.Errorf("expected Zero time, got %v", exp1)
	}

	// it should store a value
	err = mc.Store(cacheKey, []byte("data"), time.Duration(60)*time.Second)
	if err != nil {
		t.Error(err)
	}

	exp1 = mc.Index.GetExpiration(cacheKey)
	if exp1.IsZero() {
		t.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
	}

	e1 := int(exp1.Unix())

	mc.SetTTL(cacheKey, time.Duration(3600)*time.Second)

	exp2 := mc.Index.GetExpiration(cacheKey)
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
	mc := storeBenchmark(b)

	for n := 0; n < b.N; n++ {
		exp1 := mc.Index.GetExpiration(cacheKey + strconv.Itoa(n))
		if exp1.IsZero() {
			b.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
		}

		e1 := int(exp1.Unix())

		mc.SetTTL(cacheKey+strconv.Itoa(n), time.Duration(3600)*time.Second)

		exp2 := mc.Index.GetExpiration(cacheKey + strconv.Itoa(n))
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
