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
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// SeriesList.Merge allocates maps and sets for series deduplication during timeseries
// merging. These pools reduce GC pressure on the critical Prometheus query path.

const (
	maxSeriesHashMapSize = 10000 // reject maps larger than this to prevent pool bloat
	maxSeriesHashSetSize = 10000 // reject sets larger than this to prevent pool bloat
)

var (
	seriesHashMapPool = sync.Pool{
		New: func() any {
			return make(map[Hash]*Series)
		},
	}

	seriesHashSetPool = sync.Pool{
		New: func() any {
			return make(sets.Set[Hash])
		},
	}
)

// getSeriesHashMap retrieves a map[Hash]*Series from the pool.
// The map is cleared and ready for use.
func getSeriesHashMap() map[Hash]*Series {
	m := seriesHashMapPool.Get().(map[Hash]*Series)
	// Clear all entries to prevent memory retention
	for k := range m {
		delete(m, k)
	}
	return m
}

// putSeriesHashMap returns a map[Hash]*Series to the pool.
// Oversized maps are discarded to prevent pool bloat.
func putSeriesHashMap(m map[Hash]*Series) {
	if m == nil {
		return
	}
	if len(m) > maxSeriesHashMapSize {
		return
	}
	// Clear all entries
	for k := range m {
		delete(m, k)
	}
	seriesHashMapPool.Put(m)
}

// getSeriesHashSet retrieves a sets.Set[Hash] from the pool.
// The set is cleared and ready for use.
func getSeriesHashSet() sets.Set[Hash] {
	s := seriesHashSetPool.Get().(sets.Set[Hash])
	// Clear all entries to prevent memory retention
	for k := range s {
		delete(s, k)
	}
	return s
}

// putSeriesHashSet returns a sets.Set[Hash] to the pool.
// Oversized sets are discarded to prevent pool bloat.
func putSeriesHashSet(s sets.Set[Hash]) {
	if s == nil {
		return
	}
	if len(s) > maxSeriesHashSetSize {
		return
	}
	// Clear all entries
	for k := range s {
		delete(s, k)
	}
	seriesHashSetPool.Put(s)
}
