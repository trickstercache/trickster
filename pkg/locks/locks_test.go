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

}

func TestLocksConcurrent(t *testing.T) {

	const size = 10000000

	lk := NewNamedLocker()

	wg := &sync.WaitGroup{}
	errs := make([]error, 0, size)

	rand.Seed(time.Now().UnixNano())

	wg.Add(size)

	for i := 0; i < size; i++ {
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

}

func TestUpgrade(t *testing.T) {

	locker := NewNamedLocker()
	nl, _ := locker.RAcquire("test")

	b := nl.Upgrade()
	nl.Release()
	if !b {
		t.Errorf("expected firstWrite to be true")
	}

	nl, _ = locker.RAcquire("test2")
	nl1 := nl.(*namedLock)
	nl1.subsequentWriter = true
	b = nl.Upgrade()
	nl.Release()
	if b {
		t.Errorf("expected firstWrite to be false")
	}

}
