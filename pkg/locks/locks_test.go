/**
* Copyright 2018 The Trickster Authors
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

func TestLocksOrdering(t *testing.T) {

	lk := NewNamedLocker()
	var wg sync.WaitGroup
	wg.Add(4)

	go func() {
		nl, _ := lk.RAcquire("testLock")
		time.Sleep(500 * time.Millisecond)
		b := nl.Upgrade()
		if !b {
			t.Error("expected true")
		}
		nl.Release()
		wg.Done()
	}()

	go func() {
		time.Sleep(300 * time.Millisecond)
		nl, _ := lk.RAcquire("testLock")
		time.Sleep(300 * time.Millisecond)
		b := nl.Upgrade()
		if b {
			t.Error("expected false")
		}
		nl.Release()
		wg.Done()
	}()

	go func() {
		time.Sleep(300 * time.Millisecond)
		nl, _ := lk.RAcquire("testLock")
		time.Sleep(300 * time.Millisecond)
		b := nl.Upgrade()
		if b {
			t.Error("expected false")
		}
		nl.Release()
		wg.Done()
	}()

	go func() {
		time.Sleep(200 * time.Millisecond)
		nl, _ := lk.RAcquire("testLock")
		time.Sleep(300 * time.Millisecond)
		nl.RRelease()
		wg.Done()
	}()

	wg.Wait()

}

func TestLocksUpgradePileup(t *testing.T) {

	const size = 2500

	lk := NewNamedLocker()
	wg := &sync.WaitGroup{}
	wg.Add(size)

	errs := make([]error, 0, size)
	var errLock sync.Mutex
	addErr := func(err error) {
		errLock.Lock()
		errs = append(errs, err)
		errLock.Unlock()
	}

	for i := 0; i < size; i++ {
		go func() {
			nl, err := lk.RAcquire(testKey)
			if err != nil {
				addErr(err)
			}
			time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
			nl.Upgrade()
			time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
			nl.Release()
			wg.Done()
		}()
	}

	wg.Wait()

	for _, err := range errs {
		t.Error(err)
	}
}

func TestLocksConcurrent(t *testing.T) {

	const size = 10000
	rand.Seed(time.Now().UnixNano())

	lk := NewNamedLocker()
	wg := &sync.WaitGroup{}
	wg.Add(size)

	errs := make([]error, 0, size)
	var errLock sync.Mutex
	addErr := func(err error) {
		errLock.Lock()
		errs = append(errs, err)
		errLock.Unlock()
	}

	for i := 0; i < size; i++ {
		go func(j int) {
			time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
			if j%3 == 0 {
				// every third routine (group 0) acquires a Write Lock and releases it
				// after a random sleep time between 0 and 5ms (measured in ns)
				nl, err := lk.Acquire(testKey)
				if err != nil {
					addErr(err)
				}
				time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
				nl.Release()
			} else if j%3 == 1 {
				// every third routine (group 1) acquires a Read Lock,
				// waits a random sleep time between 0 and 5ms (measured in ns),
				// then upgrades to a Write Lock and releases it
				// after a random sleep time between 0 and 5ms (measured in ns)
				nl, err := lk.RAcquire(testKey)
				if err != nil {
					errLock.Lock()
					errs = append(errs, err)
					errLock.Unlock()
				}
				time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
				nl.Upgrade()
				time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
				nl.Release()
			} else {
				// every third routine (group 2) acquires a Read Lock, and releases it
				// after a random sleep time between 0 and 5ms (measured in ns)
				nl, err := lk.RAcquire(testKey)
				if err != nil {
					errLock.Lock()
					errs = append(errs, err)
					errLock.Unlock()
				}
				time.Sleep(time.Duration(rand.Int63()%5000000) * time.Nanosecond)
				nl.RRelease()
			}
			wg.Done()
		}(i)
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
