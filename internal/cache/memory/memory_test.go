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
	"testing"
)

func TestCache_Connect(t *testing.T) {
	mc := Cache{}

	// it should connect
	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Store(t *testing.T) {
	mc := Cache{}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	// it should store a value
	err = mc.Store("cacheKey", []byte("data"), 60000)
	if err != nil {
		t.Error(err)
	}
}

func TestCache_Retrieve(t *testing.T) {
	mc := Cache{}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	err = mc.Store("cacheKey", []byte("data"), 60000)
	if err != nil {
		t.Error(err)
	}

	// it should retrieve a value
	var data []byte
	data, err = mc.Retrieve("cacheKey")
	if err != nil {
		t.Error(err)
	}
	if string(data) != "data" {
		t.Errorf("wanted \"%s\". got \"%s\"", "data", data)
	}
}

func TestCache_ReapOnce(t *testing.T) {
	mc := Cache{}

	err := mc.Connect()
	if err != nil {
		t.Error(err)
	}

	// fake an expired entry
	mc.Store("cacheKey", []byte("data"), -1000)

	mc.ReapOnce()

	if _, err := mc.Retrieve("cacheKey"); err == nil {
		t.Errorf("expected cache entry to be removed")
	}

}

func TestCache_Close(t *testing.T) {
	mc := Cache{}
	mc.Close()
}
