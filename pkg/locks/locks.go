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

// Package locks provides Named Locks functionality for managing
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
	mapLock namedLock
}

// NewNamedLocker returns a new Named Locker
func NewNamedLocker() NamedLocker {
	return &namedLocker{
		locks: make(map[string]*namedLock),
	}
}

// NamedLock defines the interface for implementing Named Locks
type NamedLock interface {
	Release() error
	RRelease() error
	Upgrade() bool
}

func newNamedLock(name string, locker *namedLocker) *namedLock {
	return &namedLock{
		name:   name,
		locker: locker,
	}
}

type namedLock struct {
	sync.RWMutex
	name             string
	queueSize        atomic.Int32
	locker           *namedLocker
	subsequentWriter bool
}

func (nl *namedLock) release(unlockFunc func()) {
	qs := nl.queueSize.Add(-1)
	if qs == 0 {
		nl.locker.mapLock.Lock()
		// recheck queue size after getting the lock since another client
		// might have joined since the map lock was acquired
		if nl.queueSize.Load() == 0 {
			delete(nl.locker.locks, nl.name)
		}
		nl.locker.mapLock.Unlock()
	}
	unlockFunc()
}

// Release releases the write lock on the subject Named Lock
func (nl *namedLock) Release() error {
	nl.release(nl.Unlock)
	return nil
}

// RRelease releases the read lock on the subject Named Lock
func (nl *namedLock) RRelease() error {
	nl.release(nl.RUnlock)
	return nil
}

// Upgrade will upgrade the current read lock to a write lock. This method will
// always succeed unless a read lock did not already exist, which panics like a
// normal mutex. the return value indicates whether the requesting goroutine was
// first to receive a lock, and will be false when multiple goroutines upgraded
// concurrently and the caller was not the first in the queue to receive it.
// This helps the caller know if any extra state checks are required
// (e.g., re-querying a cache that might have changed) before proceeding.
func (nl *namedLock) Upgrade() bool {
	nl.RUnlock()
	nl.Lock()
	if nl.subsequentWriter {
		return false
	}
	nl.subsequentWriter = true
	return true
}

func (lk *namedLocker) acquire(lockName string, isWrite bool) (NamedLock, error) {
	if lockName == "" {
		return nil, errInvalidLockName(lockName)
	}
	lk.mapLock.RLock()
	nl, ok := lk.locks[lockName]
	mapUnlockFunc := lk.mapLock.RUnlock
	if !ok {
		mapUnlockFunc = lk.mapLock.Unlock
		lk.mapLock.Upgrade()
		// check again in case we weren't the first to upgrade
		nl, ok = lk.locks[lockName]
		if !ok {
			nl = newNamedLock(lockName, lk)
		}
		lk.locks[lockName] = nl
	}
	nl.queueSize.Add(1)
	mapUnlockFunc()

	if isWrite {
		nl.Lock()
	} else {
		nl.RLock()
		// if the Named Lock was previously a Write lock but is now a Read lock again,
		// meaning RAcquires queued up while it was write-locked, this goes back to false
		nl.subsequentWriter = false
	}
	return nl, nil
}

// Acquire locks the named lock for writing, and blocks until the wlock is acquired
func (lk *namedLocker) Acquire(lockName string) (NamedLock, error) {
	return lk.acquire(lockName, true)
}

// RAcquire locks the named lock for reading, and blocks until the rlock is acquired
func (lk *namedLocker) RAcquire(lockName string) (NamedLock, error) {
	return lk.acquire(lockName, false)
}

func errInvalidLockName(name string) error {
	return fmt.Errorf("invalid lock name: %s", name)
}
