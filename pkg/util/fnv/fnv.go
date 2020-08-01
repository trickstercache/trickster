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

// Package fnv implements the Fowler Noll Vo hash function (version a)
// taken with much appreciation from, and giving all credit to:
// https://github.com/influxdata/influxdb/blob/v1.8.0/models/inline_fnv.go
package fnv

// from stdlib hash/fnv/fnv.go
const (
	prime64  = 1099511628211
	offset64 = 14695981039346656037
)

// InlineFNV64a is an alloc-free port of the standard library's fnv64a.
// See https://en.wikipedia.org/wiki/Fowler%E2%80%93Noll%E2%80%93Vo_hash_function.
type InlineFNV64a uint64

// NewInlineFNV64a returns a new instance of InlineFNV64a.
func NewInlineFNV64a() InlineFNV64a {
	return offset64
}

// Write adds data to the running hash.
func (s *InlineFNV64a) Write(data []byte) (int, error) {
	hash := uint64(*s)
	for _, c := range data {
		hash ^= uint64(c)
		hash *= prime64
	}
	*s = InlineFNV64a(hash)
	return len(data), nil
}

// Sum64 returns the uint64 of the current resulting hash.
func (s *InlineFNV64a) Sum64() uint64 {
	return uint64(*s)
}
