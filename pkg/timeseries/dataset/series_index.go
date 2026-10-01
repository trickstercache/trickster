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

package dataset

type seriesIndex[T any] struct {
	header func(T) *SeriesHeader
	first  map[Hash]T
	spill  map[Hash][]T
}

func newSeriesIndex[T any](size int, header func(T) *SeriesHeader) *seriesIndex[T] {
	return &seriesIndex[T]{header: header, first: make(map[Hash]T, size)}
}

func (x *seriesIndex[T]) find(h Hash, sh *SeriesHeader) (T, bool) {
	// the hash narrows the search and the header comparison decides it, so series
	// whose hashes collide are never treated as one
	if v, ok := x.first[h]; ok {
		if sameSeries(x.header(v), sh) {
			return v, true
		}
		for _, v := range x.spill[h] {
			if sameSeries(x.header(v), sh) {
				return v, true
			}
		}
	}
	var zero T
	return zero, false
}

func (x *seriesIndex[T]) add(h Hash, v T) {
	if _, ok := x.first[h]; !ok {
		x.first[h] = v
		return
	}
	if x.spill == nil {
		x.spill = make(map[Hash][]T)
	}
	x.spill[h] = append(x.spill[h], v)
}

func (x *seriesIndex[T]) reset() {
	clear(x.first)
	clear(x.spill)
}

func headerOf(s *Series) *SeriesHeader {
	return &s.Header
}
