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
)

var locks = make(map[string]*namedLock)
var mapLock = sync.Mutex{}

type namedLock struct {
	*sync.Mutex
	name      string
	queueSize int
}

func newNamedLock(name string) *namedLock {
	return &namedLock{
		name:  name,
		Mutex: &sync.Mutex{},
	}
}

// Acquire returns a named lock, and blocks until it is acquired
func Acquire(lockName string) error {
	if lockName == "" {
		return fmt.Errorf("invalid lock name: %s", lockName)
	}

	mapLock.Lock()
	nl, ok := locks[lockName]
	if !ok {
		nl = newNamedLock(lockName)
		locks[lockName] = nl
	}
	nl.queueSize++
	mapLock.Unlock()

	nl.Lock()
	return nil
}

// Release unlocks and releases a named lock
func Release(lockName string) error {
	if lockName == "" {
		return fmt.Errorf("invalid lock name: %s", lockName)
	}
	mapLock.Lock()
	if nl, ok := locks[lockName]; ok {
		nl.queueSize--
		if nl.queueSize == 0 {
			delete(locks, lockName)
		}
		mapLock.Unlock()
		nl.Unlock()
		return nil
	}
	mapLock.Unlock()
	return fmt.Errorf("no such lock name: %s", lockName)
}
