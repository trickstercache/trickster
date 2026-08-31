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

// Package slices provides slice helpers that complement the standard
// library's slices package.
package slices

import "iter"

// SlicesChunk yields successive n-sized chunks of s, so a caller can honor
// an API's per-request batch limit without index arithmetic at each site --
// ECS DescribeTasks accepting at most 100 task ARNs, say.
//
// The chunks are subslices of s, not copies, so they share its backing
// array and must not be retained past the iteration if s may be mutated.
// A non-positive n yields nothing rather than looping forever.
func SlicesChunk[T any](s []T, n int) iter.Seq[[]T] {
	return func(yield func([]T) bool) {
		if n <= 0 {
			return
		}
		for i := 0; i < len(s); i += n {
			if !yield(s[i:min(i+n, len(s))]) {
				return
			}
		}
	}
}
