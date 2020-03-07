/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package locks provides Named Locks functionality for manging
// mutexes by string name (e.g., cache keys).
package locks

import (
	"sync"
)

var locks = make(map[string]*namedLock)
var mapLock = sync.Mutex{}

type namedLock struct {
	name      string
	mtx       *sync.Mutex
	queueSize int
}

func newNamedLock(name string) *namedLock {
	return &namedLock{
		name: name,
		mtx:  &sync.Mutex{},
	}
}

// Acquire returns a named lock, and blocks until it is acquired
func Acquire(lockName string) *sync.Mutex {

	var nl *namedLock
	var ok bool

	if lockName == "" {
		return nil
	}

	mapLock.Lock()
	if nl, ok = locks[lockName]; !ok {
		nl = newNamedLock(lockName)
		locks[lockName] = nl
	}
	nl.queueSize++
	mapLock.Unlock()
	nl.mtx.Lock()
	return nl.mtx
}

// Release unlocks and releases a named lock
func Release(lockName string) {

	if lockName == "" {
		return
	}

	mapLock.Lock()
	if nl, ok := locks[lockName]; ok {
		nl.queueSize--
		if nl.queueSize == 0 {
			delete(locks, lockName)
		}
		nl.mtx.Unlock()
	}
	mapLock.Unlock()
}
