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

package index

import (
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	metrics.Init()
}

func fakeBulkRemoveFunc([]string, bool) {}
func fakeFlusherFunc(string, []byte)    {}

func TestNewIndex(t *testing.T) {
	cacheConfig := &config.CachingConfig{Type: "test", Index: config.CacheIndexConfig{ReapInterval: time.Second * time.Duration(10), FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, fakeBulkRemoveFunc, fakeFlusherFunc)

	if idx.name != "test" {
		t.Errorf("expected test got %s", idx.name)
	}
}

func TestIndexFromBytes(t *testing.T) {

	idx := Index{}
	b := idx.ToBytes()
	idx2, err := IndexFromBytes(b)
	if err != nil {
		t.Error(err)
	}

	if idx2 == nil {
		t.Errorf("nil cache index")
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
	cacheConfig := &config.CachingConfig{Type: "test", Index: config.CacheIndexConfig{ReapInterval: time.Second * time.Duration(10), FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, fakeBulkRemoveFunc, fakeFlusherFunc)

	idx.UpdateObject(obj)
	if _, ok := idx.Objects["test"]; ok {
		t.Errorf("test object should be missing from index")
	}

	obj.Key = "test"

	idx.UpdateObject(obj)
	if _, ok := idx.Objects["test"]; !ok {
		t.Errorf("test object missing from index")
	}

	// do it again to cover the index hit case
	idx.UpdateObject(obj)
	if _, ok := idx.Objects["test"]; !ok {
		t.Errorf("test object missing from index")
	}

	idx.Objects["test"].LastAccess = time.Time{}
	idx.UpdateObjectAccessTime("test")

	if idx.Objects["test"].LastAccess.IsZero() {
		t.Errorf("test object last access time is wrong")
	}

}

func TestRemoveObject(t *testing.T) {

	obj := Object{Key: "test", Value: []byte("test_value")}
	cacheConfig := &config.CachingConfig{Type: "test", Index: config.CacheIndexConfig{ReapInterval: time.Second * time.Duration(10), FlushInterval: time.Second * time.Duration(10)}}
	idx := NewIndex("test", "test", nil, cacheConfig.Index, fakeBulkRemoveFunc, fakeFlusherFunc)

	idx.UpdateObject(obj)
	if _, ok := idx.Objects["test"]; !ok {
		t.Errorf("test object missing from index")
	}

	idx.RemoveObject("test", false)
	if _, ok := idx.Objects["test"]; ok {
		t.Errorf("test object should be missing from index")
	}

}
