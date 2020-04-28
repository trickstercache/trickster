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

// Package locks provides Named Locks functionality for manging
// mutexes by string name (e.g., cache keys).
package locks

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// NamedLocker provides a locker for handling Named Locks
type NamedLocker interface {
	Acquire(string) (NamedLock, error)
	RAcquire(string) (NamedLock, error)
}

type namedLocker struct {
	locks   map[string]*namedLock
	mapLock *sync.Mutex
}

// NewNamedLocker returns a new Named Locker
func NewNamedLocker() NamedLocker {
	return &namedLocker{
		locks:   make(map[string]*namedLock),
		mapLock: &sync.Mutex{},
	}
}

// NamedLock defines the interface for implementing Named Locks
type NamedLock interface {
	Release() error
	RRelease() error
	WriteLockCounter() int
}

func newNamedLock(name string, locker *namedLocker) *namedLock {
	return &namedLock{
		name:    name,
		RWMutex: &sync.RWMutex{},
		locker:  locker,
	}
}

type namedLock struct {
	*sync.RWMutex
	name           string
	queueSize      int32
	writeLockCount int
	releaser       func()
	locker         *namedLocker
}

func (nl *namedLock) Release() error {

	if nl.name == "" {
		return fmt.Errorf("invalid lock name: %s", nl.name)
	}

	qs := atomic.AddInt32(&nl.queueSize, -1)
	if qs == 0 {
		nl.locker.mapLock.Lock()
		delete(nl.locker.locks, nl.name)
		nl.locker.mapLock.Unlock()
	}

	nl.Unlock()
	return nil
}

func (nl *namedLock) RRelease() error {

	if nl.name == "" {
		return fmt.Errorf("invalid lock name: %s", nl.name)
	}

	qs := atomic.AddInt32(&nl.queueSize, -1)
	if qs == 0 {
		nl.locker.mapLock.Lock()
		delete(nl.locker.locks, nl.name)
		nl.locker.mapLock.Unlock()
	}

	nl.RUnlock()
	return nil
}

// WriteLockCounter returns the number of write locks acquired by the namedLock
// This function should only be called by a goroutine actively holding a write lock,
// as it is otherwise not atomic
func (nl *namedLock) WriteLockCounter() int {
	return nl.writeLockCount
}

// Acquire locks the named lock, and blocks until the lock is acquired
func (lk *namedLocker) Acquire(lockName string) (NamedLock, error) {
	if lockName == "" {
		return nil, fmt.Errorf("invalid lock name: %s", lockName)
	}

	lk.mapLock.Lock()
	nl, ok := lk.locks[lockName]
	if !ok {
		nl = newNamedLock(lockName, lk)
		lk.locks[lockName] = nl
	}

	atomic.AddInt32(&nl.queueSize, 1)
	lk.mapLock.Unlock()

	nl.Lock()
	nl.writeLockCount++
	return nl, nil
}

// RAcquire locks the named lock for reading, and blocks until the rlock is acquired
func (lk *namedLocker) RAcquire(lockName string) (NamedLock, error) {
	if lockName == "" {
		return nil, fmt.Errorf("invalid lock name: %s", lockName)
	}

	lk.mapLock.Lock()
	nl, ok := lk.locks[lockName]
	if !ok {
		nl = newNamedLock(lockName, lk)
		lk.locks[lockName] = nl
	}

	atomic.AddInt32(&nl.queueSize, 1)
	lk.mapLock.Unlock()

	nl.RLock()
	return nl, nil
}
