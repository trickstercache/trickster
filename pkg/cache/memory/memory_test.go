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

	"github.com/trickstercache/trickster/v2/pkg/cache"
	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

const provider = "memory"
const cacheKey = "cacheKey"

type testReferenceObject struct {
}

var testLocker = locks.NewNamedLocker()

func (r *testReferenceObject) Size() int {
	return 1
}

func storeBenchmark(b *testing.B) *Cache {
	cacheConfig := co.Options{Provider: provider, Index: &io.Options{ReapInterval: 0}}
	mc := &Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

	err := mc.Connect()
	if err != nil {
		b.Error(err)
	}
	defer mc.Close()
	for n := 0; n < b.N; n++ {
		err = mc.Store(cacheKey+strconv.Itoa(n), []byte("data"+strconv.Itoa(n)), time.Duration(60)*time.Second)
		if err != nil {
			b.Error(err)
		}
	}
	return mc
}

func newCacheConfig(t *testing.T) co.Options {
	return co.Options{Provider: provider, Index: &io.Options{ReapInterval: 0}}
}

func TestConfiguration(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}
	cfg := mc.Configuration()
	if cfg.Provider != provider {
		t.Fatalf("expected %s got %s", provider, cfg.Provider)
	}
}

func TestCache_Connect(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}

	// it should connect
	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestCache_StoreReferenceDirect(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
	// it should store a value
	mc.store("test", nil, &testReferenceObject{}, 1*time.Second, true)

	r, _, _ := mc.RetrieveReference("test", true)
	if r == nil {
		t.Errorf("expected %s got nil", r)
	}

	_, _, err = mc.RetrieveReference("test2", true)
	if err == nil {
		t.Errorf("expected nil got %s", err.Error())
	}

}

func TestCache_StoreReference(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

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
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

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

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

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
	data, ls, err = mc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	// expire the object
	mc.SetTTL(cacheKey, -1*time.Hour)

	time.Sleep(time.Millisecond * 10)

	// this should now return error
	data, ls, err = mc.Retrieve(cacheKey, false)
	if err != cache.ErrKNF {
		t.Error("expected error for KNF")
		mc.Close()
	}
	if string(data) != "" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusKeyMiss {
		t.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
	}
}

func BenchmarkCache_Retrieve(b *testing.B) {
	mc := storeBenchmark(b)

	for n := 0; n < b.N; n++ {
		var data []byte
		data, ls, err := mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err != nil {
			b.Error(err)
		}
		if string(data) != "data"+strconv.Itoa(n) {
			b.Errorf("wanted \"%s\". got \"%s\"", "data"+strconv.Itoa(n), data)
		}
		if ls != status.LookupStatusHit {
			b.Errorf("expected %s got %s", status.LookupStatusHit, ls)
		}

		// expire the object
		mc.SetTTL(cacheKey+strconv.Itoa(n), -1*time.Hour)

		// this should now return error
		data, ls, err = mc.Retrieve(cacheKey+strconv.Itoa(n), false)
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
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}

func TestCache_Close(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error")}
	mc.Close()
}

func TestCache_Remove(t *testing.T) {
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

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
	data, ls, err := mc.Retrieve(cacheKey, false)
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
	_, ls, err = mc.Retrieve(cacheKey, false)
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
		data, ls, err := mc.Retrieve(cacheKey+strconv.Itoa(n), false)
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
		data, ls, err = mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		expectederr := `value for key [` + cacheKey + strconv.Itoa(n) + `] not in cache`
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
	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

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
	data, ls, err := mc.Retrieve(cacheKey, false)
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\".", "data", data)
	}
	if ls != status.LookupStatusHit {
		t.Errorf("expected %s got %s", status.LookupStatusHit, ls)
	}

	mc.BulkRemove([]string{cacheKey})

	// it should be a cache miss
	_, ls, err = mc.Retrieve(cacheKey, false)
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

	mc.BulkRemove(keyArray)

	// it should be a cache miss
	for n := 0; n < b.N; n++ {
		_, ls, err := mc.Retrieve(cacheKey+strconv.Itoa(n), false)
		if err == nil {
			b.Errorf("expected key not found error for %s", cacheKey)
		}
		if ls != status.LookupStatusKeyMiss {
			b.Errorf("expected %s got %s", status.LookupStatusKeyMiss, ls)
		}
	}
}

func TestMemoryCache_SetTTL(t *testing.T) {

	cacheConfig := newCacheConfig(t)
	mc := Cache{Config: &cacheConfig, Logger: tl.ConsoleLogger("error"), locker: testLocker}

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

	time.Sleep(time.Millisecond * 10)

	exp1 = mc.Index.GetExpiration(cacheKey)
	if exp1.IsZero() {
		t.Errorf("expected time %d, got zero", int(time.Now().Unix())+60)
	}

	e1 := int(exp1.Unix())

	mc.SetTTL(cacheKey, time.Duration(3600)*time.Second)

	time.Sleep(time.Millisecond * 10)

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

func TestLocker(t *testing.T) {
	cache := Cache{locker: locks.NewNamedLocker()}
	l := cache.Locker()
	cache.SetLocker(locks.NewNamedLocker())
	m := cache.Locker()
	if l == m {
		t.Errorf("error setting locker")
	}
}
