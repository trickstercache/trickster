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

import (
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// SeriesList is an ordered list of Series
type SeriesList []*Series

// merge merges sl2 into the subject SeriesList, using sl2's authoritative order
// to adaptively reorder the existing+merged list such that it best emulates
// the fully constituted series order as it would be served by the origin.
// merge merges two []series, not individual series into a single series.
// It assumes that a series in both lists, having the identical header hash,
// are completely identical, including all data points, because they were already
// merged just prior to merging the actual lists.
func (sl SeriesList) merge(sl2 SeriesList) SeriesList {
	if sl.Equal(sl2) || len(sl2) == 0 {
		return sl
	}
	m := make(map[Hash]int)
	updateLookup := func(sl3 SeriesList) {
		for i, v := range sl3 {
			h := v.Header.CalculateHash()
			if _, ok := m[h]; !ok {
				m[h] = i
			}
		}
	}
	out := make(SeriesList, len(sl)+len(sl2))
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if _, ok := m[h]; ok {
			continue
		}
		out[k] = s
		m[h] = k
		k++
	}
	seen := make(sets.Set[Hash], len(sl2))
	var pj int
	for _, v := range sl2 {
		if v == nil {
			continue
		}
		h := v.Header.CalculateHash()
		if seen.Contains(h) {
			continue
		}
		seen.Add(h)
		j, ok := m[h]
		if !ok {
			out[k] = v
			pj = k
			k++
			updateLookup(out[:k])
			continue
		}
		if j < pj {
			pj = k - 1
			copy(out[j:], out[j+1:])
			out[pj] = v
			updateLookup(out[:k])
			continue
		}
		pj = j
	}
	return out[:k]
}

// Equal returns true if the slices contain identical values in the identical order
func (sl SeriesList) Equal(sl2 SeriesList) bool {
	if sl2 == nil || len(sl) != len(sl2) {
		return false
	}
	for i, v := range sl {
		if v == nil {
			continue
		}
		if v.Header.CalculateHash() != sl2[i].Header.CalculateHash() {
			return false
		}
	}
	return true
}

func (sl SeriesList) String() string {
	hashes := make([]string, len(sl))
	for i, v := range sl {
		hashes[i] = fmt.Sprintf("%d", v.Header.CalculateHash())
	}
	return "[" + strings.Join(hashes, ",") + "]"
}
