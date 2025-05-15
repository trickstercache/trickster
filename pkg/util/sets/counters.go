/*
 * Copyright 2018 The Trickster Authors
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

package sets

import "github.com/trickstercache/trickster/v2/pkg/util/numbers"

// A CounterSet is map with keys of type T and values representing a count.
// The Increment() function is used to change the count value.
// A CounterSet it is not safe for concurrency and its order is not guaranteed.
type CounterSet[T comparable] map[T]int

// Increment increments the CounterSet value for 'key' by 'cnt' and returns the
// old and new value. The bool returns true if the key was pre-existing.
// Negative cnt values are ok. Increment is not safe for concurrency.
func (s CounterSet[T]) Increment(key T, cnt int) (int, int, bool) {
	if i, ok := s[key]; ok {
		if j, ok := numbers.SafeAdd(i, cnt); ok {
			s[key] = j
			return i, j, true
		}
		return i, i, true
	}
	s[key] = cnt
	return 0, cnt, false
}

// Reset resets the CounterSet value for 'key' to 0. If the key is not present
// in the map, it is added and set to 0. Reset is not safe for concurrency.
func (s CounterSet[T]) Reset(key T) {
	s[key] = 0
}

// Value returns the value for the provided key, or false if it doesn't exist.
func (s CounterSet[T]) Value(key T) (int, bool) {
	if i, ok := s[key]; ok {
		return i, true
	}
	return -1, false
}

// StringCounterSet is CounterSet with string keys
type StringCounterSet = CounterSet[string]

// NewStringCounterSet returns a new StringCounterSet
func NewStringCounterSet() StringCounterSet {
	return make(StringCounterSet)
}

// NewStringCounterSetCap returns a new StringCounterSet with a capacity set.
func NewStringCounterSetCap(capacity int) StringCounterSet {
	return make(StringCounterSet, capacity)
}
