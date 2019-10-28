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
	"sync"
)

var locks = make(map[string]*sync.Mutex)
var mapLock = sync.Mutex{}

// Acquire returns a named lock, and blocks until it is acquired
func Acquire(lockName string) *sync.Mutex {

	var l *sync.Mutex
	var ok bool

	mapLock.Lock()
	if l, ok = locks[lockName]; !ok {
		l = &sync.Mutex{}
		locks[lockName] = l
	}
	mapLock.Unlock()
	l.Lock()

	return l
}

// Release unlocks and releases a named lock
func Release(lockName string) {
	mapLock.Lock()
	if l, ok := locks[lockName]; ok {
		delete(locks, lockName)
		l.Unlock()
	}
	mapLock.Unlock()
}
