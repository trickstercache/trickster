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
	"cmp"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// points a response adds before or after a series' own, like partial buckets, are held as its head
// and tail rather than copied into Points; PointAt and Rows read all three

// PointParts returns the series' points in order: its head, its own Points and its tail
func (s *Series) PointParts() [3]Points {
	return [3]Points{s.head, s.Points, s.tail}
}

// IsSorted reports whether the series' points are in epoch order across its parts
func (s *Series) IsSorted() bool {
	var last *Point
	for _, part := range s.PointParts() {
		if len(part) == 0 {
			continue
		}
		if last != nil && part[0].Epoch < last.Epoch {
			return false
		}
		if !slices.IsSortedFunc(part, pointCmp) {
			return false
		}
		last = &part[len(part)-1]
	}
	return true
}

// PointCount returns the number of points across the series' parts
func (s *Series) PointCount() int {
	return len(s.head) + len(s.Points) + len(s.tail)
}

// HasParts reports whether the series holds points beside its own Points
func (s *Series) HasParts() bool {
	return len(s.head) > 0 || len(s.tail) > 0
}

// FlatPoints returns the series' points as one slice: Points itself when it has no other parts, and
// otherwise a new slice joining them, which shares their values
func (s *Series) FlatPoints() Points {
	if !s.HasParts() {
		return s.Points
	}
	return slices.Concat(s.head, s.Points, s.tail)
}

// PointAt returns the series' ith point across its parts
func (s *Series) PointAt(i int) *Point {
	if i < len(s.head) {
		return &s.head[i]
	}
	i -= len(s.head)
	if i < len(s.Points) {
		return &s.Points[i]
	}
	return &s.tail[i-len(s.Points)]
}

// folds the parts into Points; only for a series its caller owns, as it changes the series
func (s *Series) flatten() {
	if s.HasParts() {
		s.Points = s.FlatPoints()
		s.head, s.tail = nil, nil
	}
}

// HasParts reports whether any series holds points beside its own Points
func (ds *DataSet) HasParts() bool {
	if ds == nil {
		return false
	}
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s != nil && s.HasParts() {
				return true
			}
		}
	}
	return false
}

// Flat returns ds or, when a series holds parts, a view of it whose series hold their points in one
// slice each, for a reader that reads Points alone
func (ds *DataSet) Flat() *DataSet {
	if !ds.HasParts() {
		return ds
	}
	out := ds.FullView()
	for _, r := range out.Results {
		for _, s := range r.SeriesList {
			if s != nil {
				s.flatten()
			}
		}
	}
	// the view's envelope leaves out what a response carries, which a reader of it still needs
	out.Status, out.ErrorType, out.Warnings = ds.Status, ds.ErrorType, ds.Warnings
	return out
}

// MergeParts is Merge, but new points wholly before or after a series' Points become parts beside
// them; flattened, the result is Merge's. ds must be a view or the caller's own
func (ds *DataSet) MergeParts(sortPoints bool, collection ...timeseries.Timeseries) {
	if ds.Merger != nil {
		ds.Merger(sortPoints, collection...)
		return
	}
	ds.UpdateLock.Lock()
	defer ds.UpdateLock.Unlock()
	ds.inheritValueOperations(collection)
	ds.mergeDataSets(collection, MergeOpts{SortPoints: sortPoints, parts: true}, true)
}

// merges next's points into cs's as MergePoints would, keeping cs's Points in place when next's fall
// wholly before or after them, and otherwise joining them all into a new slice
func mergeSeriesParts(cs, next *Series, sortPoints bool) {
	pts := next.FlatPoints()
	if !sortPoints {
		// without a sort, a merge joins next's points after cs's
		switch {
		case len(pts) == 0:
		case cs.PointCount() == 0:
			cs.Points, cs.head, cs.tail = pts.Clone(), nil, nil
		default:
			cs.tail = slices.Concat(cs.tail, pts)
		}
		cs.PointSize = cs.pointSize()
		return
	}
	if strictlyIncreasing(cs.Points) && len(cs.Points) > 0 {
		lo, hi := cs.Points[0].Epoch, cs.Points[len(cs.Points)-1].Epoch
		others := slices.Concat(cs.head, cs.tail, pts)
		outside := true
		for i := range others {
			if others[i].Epoch >= lo && others[i].Epoch <= hi {
				outside = false
				break
			}
		}
		if outside {
			// the others sort and dedupe among themselves exactly as they would with Points among them
			others = sortAndDedupeTolerant(others, 0)
			k, _ := slices.BinarySearchFunc(others, lo, func(p Point, e epoch.Epoch) int {
				return cmp.Compare(p.Epoch, e)
			})
			cs.head, cs.tail = slices.Clip(others[:k]), others[k:]
			cs.PointSize = cs.pointSize()
			return
		}
	}
	// otherwise all of them join into a new slice, as Merge joins them
	cs.Points = MergePointsWithOpts(slices.Concat(cs.head, cs.Points, cs.tail), pts, MergeOpts{SortPoints: true})
	cs.head, cs.tail = nil, nil
	cs.PointSize = cs.Points.Size()
}

func strictlyIncreasing(p Points) bool {
	for i := 1; i < len(p); i++ {
		if p[i].Epoch <= p[i-1].Epoch {
			return false
		}
	}
	return true
}

// what Points.Size gives for the series' points joined
func (s *Series) pointSize() int64 {
	size := s.Points.Size()
	for _, part := range [2]Points{s.head, s.tail} {
		for i := range part {
			size += int64(part[i].Size)
		}
	}
	return size
}
