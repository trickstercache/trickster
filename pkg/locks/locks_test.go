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

package locks

import (
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"
)

const testKey = "testKey"

func TestLocks(t *testing.T) {

	var testVal = 0

	Acquire("test")
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		Acquire("test")
		testVal += 10
		Release("test")
		wg.Done()
	}()
	testVal++
	if testVal != 1 {
		t.Errorf("expected 1 got %d", testVal)
	}
	time.Sleep(time.Second * 1)
	Release("test")
	wg.Wait()

	if testVal != 11 {
		t.Errorf("expected 11 got %d", testVal)
	}

	expected := "invalid lock name: "
	err := Acquire("")
	if err.Error() != expected {
		t.Errorf("got %s expected %s", err.Error(), expected)
	}

	err = Release("")
	if err.Error() != expected {
		t.Errorf("got %s expected %s", err.Error(), expected)
	}

	expected = "no such lock name: invalid"
	err = Release("invalid")
	if err.Error() != expected {
		t.Errorf("got %s expected %s", err.Error(), expected)
	}

}

func TestLocksConcurrent(t *testing.T) {

	const size = 10000000

	wg := &sync.WaitGroup{}
	errs := make([]error, 0, size)

	rand.Seed(time.Now().UnixNano())

	for i := 0; i < size; i++ {
		wg.Add(1)
		go func() {
			err := Acquire(testKey)
			if err != nil {
				errs = append(errs, err)
			}
			err = Release(testKey)
			if err != nil {
				errs = append(errs, err)
			}
			wg.Done()
		}()
	}

	wg.Wait()

	for _, err := range errs {
		t.Error(err)
	}

}

func TestLockReadAndWrite(t *testing.T) {

	i := 0
	j := 0

	wg := &sync.WaitGroup{}

	Acquire("test")

	wg.Add(1)
	go func() {
		RAcquire("test")
		j = i
		RRelease("test")
		wg.Done()
	}()

	i = 10
	Release("test")

	wg.Wait()

	if j != 10 {
		t.Errorf("expected 10 got %d", j)
	}

	err := RAcquire("")
	if err == nil || !strings.HasPrefix(err.Error(), "invalid lock name:") {
		t.Error("expected error for invalid lock name")
	}

	err = RRelease("")
	if err == nil || !strings.HasPrefix(err.Error(), "invalid lock name:") {
		t.Error("expected error for invalid lock name")
	}

	err = RRelease("invalid")
	if err == nil || !strings.HasPrefix(err.Error(), "no such lock name:") {
		t.Error("expected error for no such lock name")
	}

}
