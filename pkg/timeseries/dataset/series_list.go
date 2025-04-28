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

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
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
	m := make(map[Hash]*Series, len(sl)+len(sl2))
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
		m[h] = s
		k++
	}
	seen := make(sets.Set[Hash], len(sl2))
	var wg sync.WaitGroup
	for _, s := range sl2 {
		if s == nil {
			continue
		}
		h := s.Header.CalculateHash()
		if seen.Contains(h) {
			continue
		}
		seen.Add(h)
		if cs, ok := m[h]; !ok {
			// this series does not exist in sl1; add it into out
			out[k] = s
			m[h] = s
			k++
		} else {
			// series is in both sl1 and sl2; merge their points
			wg.Add(1)
			func(s1, s2 *Series) {
				s1.Points = MergePoints(s1.Points, s2.Points, sortPoints)
				s1.PointSize = s1.Points.Size()
				wg.Done()
			}(cs, s)
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
	lkp := make(map[string]*Series, len(sl))
	keys := make([]string, len(sl))
	var i int
	for _, s := range sl {
		if s == nil {
			continue
		}
		key := fmt.Sprintf("%s.%s", s.Header.Tags, s.Header.Name)
		lkp[key] = s
		keys[i] = key
		i++
	}
	keys = keys[:i]
	slices.Sort(keys)
	for i, key := range keys {
		sl[i] = lkp[key]
	}
}

func (sl SeriesList) SortPoints() {
	var wg sync.WaitGroup
	wg.Add(len(sl))
	for _, s := range sl {
		go func(gs *Series) {
			sort.Sort(gs.Points)
			wg.Done()
		}(s)
	}
	wg.Wait()
}
