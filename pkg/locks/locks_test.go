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

	lk := NewNamedLocker()

	nl, _ := lk.Acquire("test")
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		nl2, _ := lk.Acquire("test")
		testVal += 10
		nl2.Release()
		wg.Done()
	}()
	testVal++
	if testVal != 1 {
		t.Errorf("expected 1 got %d", testVal)
	}
	time.Sleep(time.Second * 1)
	nl.Release()
	wg.Wait()

	if testVal != 11 {
		t.Errorf("expected 11 got %d", testVal)
	}

	expected := "invalid lock name: "
	_, err := lk.Acquire("")
	if err.Error() != expected {
		t.Errorf("got %s expected %s", err.Error(), expected)
	}

	nl3, _ := lk.Acquire("test1")
	nl3.(*namedLock).name = ""
	err = nl3.RRelease()
	if err.Error() != expected {
		t.Errorf("got %s expected %s", err.Error(), expected)
	}

	nl = &namedLock{}

	err = nl.Release()
	if err.Error() != expected {
		t.Errorf("got %s expected %s", err.Error(), expected)
	}
}

func TestLocksConcurrent(t *testing.T) {

	const size = 10000000

	lk := NewNamedLocker()

	wg := &sync.WaitGroup{}
	errs := make([]error, 0, size)

	rand.Seed(time.Now().UnixNano())

	for i := 0; i < size; i++ {
		wg.Add(1)
		go func() {
			nl, err := lk.Acquire(testKey)
			if err != nil {
				errs = append(errs, err)
			}
			err = nl.Release()
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

	lk := NewNamedLocker()

	i := 0
	j := 0

	wg := &sync.WaitGroup{}

	nl, _ := lk.Acquire("test")

	_, err := lk.RAcquire("")
	if err == nil {
		t.Error("expected error for invalid key name")
	}

	wg.Add(1)
	go func() {
		nl1, _ := lk.RAcquire("test")
		j = i
		nl1.RRelease()
		wg.Done()
	}()

	i = 10
	nl.Release()

	wg.Wait()

	if j != 10 {
		t.Errorf("expected 10 got %d", j)
	}

	_, err = lk.Acquire("")
	if err == nil || !strings.HasPrefix(err.Error(), "invalid lock name:") {
		t.Error("expected error for invalid lock name")
	}

	nl, _ = lk.RAcquire("testKeyReadOnly")
	nl.(*namedLock).name = ""

	err = nl.Release()
	if err == nil || !strings.HasPrefix(err.Error(), "invalid lock name:") {
		t.Error("expected error for invalid lock name")
	}
}

func TestWriteLockCounter(t *testing.T) {

	const expected = 50
	nl := newNamedLock("testKey", nil)
	nl.writeLockCount = expected
	v := nl.WriteLockCounter()
	if v != expected {
		t.Errorf("expected %d got %d", expected, v)
	}

}
