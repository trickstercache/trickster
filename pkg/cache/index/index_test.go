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

package index

import (
	"sort"
	"testing"
	"time"

	io "github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

var testLogger = tl.ConsoleLogger("error")

var testBulkIndex *Index

func testBulkRemoveFunc(cacheKeys []string) {
}
func fakeFlusherFunc(string, []byte) {}

type testReferenceObject struct {
}

func (r *testReferenceObject) Size() int {
	return 1
}

func TestNewIndex(t *testing.T) {
	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Millisecond * 100,
			FlushInterval: time.Millisecond * 100}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)

	// this gives a chance for the reaper to run through for test coverage
	time.Sleep(1 * time.Second)

	if idx.name != "test" {
		t.Errorf("expected test got %s", idx.name)
	}

	idx.flushOnce(testLogger)

	idx.Close()
	time.Sleep(500 * time.Millisecond)
	if !idx.reaperExited {
		t.Error("expected true")
	}
	if !idx.flusherExited {
		t.Error("expected true")
	}

	idx2 := NewIndex("test", "test", idx.ToBytes(), cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)
	if idx2 == nil {
		t.Errorf("nil cache index")
	}

	cacheConfig.Index.FlushInterval = 0
	cacheConfig.Index.ReapInterval = 0
	idx3 := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)
	if idx3 == nil {
		t.Errorf("nil cache index")
	}

}

func TestReap(t *testing.T) {

	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Second * time.Duration(10),
			FlushInterval: time.Second * time.Duration(10)}}
	cacheConfig.Index.MaxSizeObjects = 5
	cacheConfig.Index.MaxSizeBackoffObjects = 3
	cacheConfig.Index.MaxSizeBytes = 100
	cacheConfig.Index.MaxSizeBackoffBytes = 30

	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)
	if idx.name != "test" {
		t.Errorf("expected test got %s", idx.name)
	}

	testBulkIndex = idx

	// add fake index key to cover the case that the reaper must skip it
	idx.UpdateObject(&Object{Key: "cache.index", Value: []byte("test_value")})

	// add expired key to cover the case that the reaper remove it
	idx.UpdateObject(&Object{Key: "test.1", Value: []byte("test_value"), Expiration: time.Now().Add(-time.Minute)})

	// add key with no expiration which should not be reaped
	idx.UpdateObject(&Object{Key: "test.2", Value: []byte("test_value")})

	// add key with future expiration which should not be reaped
	idx.UpdateObject(&Object{Key: "test.3", Value: []byte("test_value"), Expiration: time.Now().Add(time.Minute)})

	// trigger a reap that will only remove expired elements but not size down the full cache
	idx.reap(testLogger)

	// add key with future expiration which should not be reaped
	idx.UpdateObject(&Object{Key: "test.4", Value: []byte("test_value"), Expiration: time.Now().Add(time.Minute)})

	// add key with future expiration which should not be reaped
	idx.UpdateObject(&Object{Key: "test.5", Value: []byte("test_value"), Expiration: time.Now().Add(time.Minute)})

	// add key with future expiration which should not be reaped
	idx.UpdateObject(&Object{Key: "test.6", Value: []byte("test_value"), Expiration: time.Now().Add(time.Minute)})

	// trigger size-based reap eviction of some elements
	idx.reap(testLogger)

	if _, ok := idx.Objects["test.1"]; ok {
		t.Errorf("expected key %s to be missing", "test.1")
	}

	if _, ok := idx.Objects["test.2"]; ok {
		t.Errorf("expected key %s to be missing", "test.2")
	}

	if _, ok := idx.Objects["test.3"]; ok {
		t.Errorf("expected key %s to be missing", "test.3")
	}

	if _, ok := idx.Objects["test.4"]; ok {
		t.Errorf("expected key %s to be missing", "test.4")
	}

	if _, ok := idx.Objects["test.5"]; ok {
		t.Errorf("expected key %s to be missing", "test.5")
	}

	if _, ok := idx.Objects["test.6"]; !ok {
		t.Errorf("expected key %s to be present", "test.6")
	}

	// add key with large body to reach byte size threshold
	idx.UpdateObject(&Object{Key: "test.7",
		Value:      []byte("test_value00000000000000000000000000000000000000000000000000000000000000000000000000000"),
		Expiration: time.Now().Add(time.Minute)})

	// trigger a byte-based reap
	idx.reap(testLogger)

	// only cache index should be left

	if _, ok := idx.Objects["test.6"]; ok {
		t.Errorf("expected key %s to be missing", "test.6")
	}

	if _, ok := idx.Objects["test.7"]; ok {
		t.Errorf("expected key %s to be missing", "test.7")
	}

}

func TestObjectFromBytes(t *testing.T) {

	obj := &Object{}
	b := obj.ToBytes()
	obj2, err := ObjectFromBytes(b)
	if err != nil {
		t.Error(err)
	}

	if obj2 == nil {
		t.Errorf("nil cache index")
	}

}

func TestUpdateObject(t *testing.T) {

	obj := Object{Key: "", Value: []byte("test_value")}
	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Second * time.Duration(10),
			FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)

	idx.UpdateObject(&obj)
	if _, ok := idx.Objects["test"]; ok {
		t.Errorf("test object should be missing from index")
	}

	obj.Key = "test"

	idx.UpdateObject(&obj)
	if _, ok := idx.Objects["test"]; !ok {
		t.Errorf("test object missing from index")
	}

	// do it again to cover the index hit case
	idx.UpdateObject(&obj)
	if _, ok := idx.Objects["test"]; !ok {
		t.Errorf("test object missing from index")
	}

	idx.Objects["test"].LastAccess = time.Time{}
	idx.UpdateObjectAccessTime("test")

	if idx.Objects["test"].LastAccess.IsZero() {
		t.Errorf("test object last access time is wrong")
	}

	obj = Object{Key: "test2", ReferenceValue: &testReferenceObject{}}

	idx.UpdateObject(&obj)
	if _, ok := idx.Objects["test2"]; !ok {
		t.Errorf("test object missing from index")
	}

}

func TestRemoveObject(t *testing.T) {

	obj := Object{Key: "test", Value: []byte("test_value")}
	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Second * time.Duration(10),
			FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)

	idx.UpdateObject(&obj)
	if _, ok := idx.Objects["test"]; !ok {
		t.Errorf("test object missing from index")
	}

	idx.RemoveObject("test")
	if _, ok := idx.Objects["test"]; ok {
		t.Errorf("test object should be missing from index")
	}

}

func TestSort(t *testing.T) {

	o := objectsAtime{
		&Object{
			Key:        "3",
			LastAccess: time.Unix(3, 0),
		},
		&Object{
			Key:        "1",
			LastAccess: time.Unix(1, 0),
		},
		&Object{
			Key:        "2",
			LastAccess: time.Unix(2, 0),
		},
	}
	sort.Sort(o)

	if o[0].Key != "1" {
		t.Errorf("expected %s got %s", "1", o[0].Key)
	}

	if o[1].Key != "2" {
		t.Errorf("expected %s got %s", "2", o[1].Key)
	}

	if o[2].Key != "3" {
		t.Errorf("expected %s got %s", "3", o[2].Key)
	}

}

func TestUpdateObjectTTL(t *testing.T) {

	cacheKey := "test-ttl-key"
	obj := Object{Key: cacheKey, Value: []byte("test_value")}
	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Second * time.Duration(10),
			FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)

	exp := idx.GetExpiration(cacheKey)
	if !exp.IsZero() {
		t.Errorf("expected zero time, got %v", exp)
	}

	idx.UpdateObject(&obj)

	idx.UpdateObjectTTL(cacheKey, time.Duration(3600)*time.Second)

	if obj.Expiration.IsZero() {
		t.Errorf("expected non-zero time, got %v", obj.Expiration)
	}

	exp = idx.GetExpiration(cacheKey)
	if exp.IsZero() {
		t.Errorf("expected non-zero time, got %v", obj.Expiration)
	}

}

func TestUpdateOptions(t *testing.T) {

	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Second * time.Duration(10),
			FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)

	options := io.New()
	options.MaxSizeBytes = 5
	idx.UpdateOptions(options)

	if idx.options.MaxSizeBytes != 5 {
		t.Errorf("expected %d got %d", 5, idx.options.MaxSizeBytes)
	}
}

func TestRemoveObjects(t *testing.T) {
	cacheConfig := &co.Options{Provider: "test",
		Index: &io.Options{ReapInterval: time.Second * time.Duration(10),
			FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, testBulkRemoveFunc, fakeFlusherFunc, testLogger)
	obj := &Object{Key: "test", Value: []byte("test_value")}
	idx.UpdateObject(obj)
	idx.RemoveObjects([]string{"test"}, false)
	if _, ok := idx.Objects["test"]; ok {
		t.Error("key should not be in map")
	}
}
