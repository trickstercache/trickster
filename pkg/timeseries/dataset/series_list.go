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
	var m map[Hash]int
	updateLookup := func(sl3 SeriesList) {
		m = make(map[Hash]int)
		for i, v := range sl3 {
			h := v.Header.CalculateHash()
			if _, ok := m[h]; !ok {
				m[h] = i
			}
		}
	}
	updateLookup(sl)
	buf := make(SeriesList, len(sl), len(sl)+len(sl2)*2)
	copy(buf, sl)
	var pj int
	for _, v := range sl2 {
		h := v.Header.CalculateHash()
		j, ok := m[h]
		if !ok {
			pj = len(buf)
			buf = append(buf, v)
			updateLookup(buf)
			continue
		}
		if j < pj {
			pj = len(buf) - 1
			buf = append(buf[:j], append(buf[j+1:], buf[j])...)
			updateLookup(buf)
			continue
		}
		pj = j
	}
	return buf
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
