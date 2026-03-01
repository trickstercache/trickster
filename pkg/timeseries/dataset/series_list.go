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
	"slices"
	"sort"
	"strings"
	"sync"
)

//go:generate go tool msgp

// SeriesList is an ordered list of Series
type SeriesList []*Series

// Merge merges sl2 into the subject SeriesList, using sl2's authoritative order
// to adaptively reorder the existing+merged list such that it best emulates
// the fully constituted series order as it would be served by the origin.
// Merge assumes that a *Series in both lists, having the identical header hash,
// are the same series and will merge sl2[i].Points into sl.Points
func (sl SeriesList) Merge(sl2 SeriesList, sortPoints bool) SeriesList {
	if len(sl2) == 0 {
		return sl.Clone()
	}
	if len(sl) == 0 {
		return sl2.Clone()
	}
	m := getSeriesHashMap()
	defer putSeriesHashMap(m)
	seen := getSeriesHashSet()
	defer putSeriesHashSet(seen)

	out := make(SeriesList, len(sl)+len(sl2))
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash(true)
		if _, ok := m[h]; ok {
			continue
		}
		out[k] = s
		m[h] = s
		k++
	}
	var wg sync.WaitGroup
	for _, s := range sl2 {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash(true)
		if seen.Contains(h) {
			continue
		}
		seen.Set(h)
		if cs, ok := m[h]; !ok {
			// this series does not exist in sl1; add it into out
			out[k] = s
			m[h] = s
			k++
		} else {
			// series is in both sl1 and sl2; merge their points
			wg.Go(func() {
				cs.Points = MergePoints(cs.Points, s.Points, sortPoints)
				cs.PointSize = cs.Points.Size()
			})
		}
	}
	wg.Wait()
	out = out[:k]
	out.SortByTags()
	return out
}

// EqualHeader returns true if the slice elements contain identical header
// values in the identical order.
func (sl SeriesList) EqualHeader(sl2 SeriesList) bool {
	if sl2 == nil || len(sl) != len(sl2) {
		return false
	}
	for i, v := range sl {
		if v == nil && sl2[i] == nil {
			continue
		}
		if v == nil || sl2[i] == nil {
			return false
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

func (sl SeriesList) Clone() SeriesList {
	out := make(SeriesList, len(sl))
	var k int
	for _, s := range sl {
		if s == nil {
			continue
		}
		out[k] = s.Clone()
		k++
	}
	return out[:k]
}

func (sl SeriesList) SortByTags() {
	lkp := getSeriesKeyMap()
	defer putSeriesKeyMap(lkp)
	keys := getSeriesKeySlice()
	defer putSeriesKeySlice(keys)

	// Ensure keys has sufficient capacity to avoid append reallocations
	if cap(keys) < len(sl) {
		keys = make([]string, 0, len(sl))
	}

	for _, s := range sl {
		if s == nil {
			continue
		}

		key := s.Header.Tags.String() + "." + s.Header.Name
		lkp[key] = s
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for i, key := range keys {
		sl[i] = lkp[key]
	}
}

func (sl SeriesList) SortPoints() {
	var wg sync.WaitGroup
	for _, s := range sl {
		wg.Go(func() {
			sort.Sort(s.Points)
		})
	}
	wg.Wait()
}
