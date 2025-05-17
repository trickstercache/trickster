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

//go:generate go tool msgp

package dataset

import (
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/cmp"
)

// Point represents a timeseries data point
type Point struct {
	Epoch  epoch.Epoch `msg:"epoch"`
	Size   int         `msg:"size"`
	Values []any       `msg:"values"`
}

// Points is a slice of type *Point
type Points []Point

// Clone returns a perfect copy of the Point
func (p *Point) Clone() Point {
	clone := Point{
		Epoch: p.Epoch,
		Size:  p.Size,
	}
	if p.Values != nil {
		clone.Values = make([]any, len(p.Values))
		copy(clone.Values, p.Values)
	}
	return clone
}

// Equal returns true if p and p2 are exactly equal
func PointsAreEqual(p1, p2 Point) bool {
	if p1.Epoch != p2.Epoch || p1.Size != p2.Size ||
		len(p1.Values) != len(p2.Values) {
		return false
	}
	for i, v := range p1.Values {
		if !cmp.Equal(p2.Values[i], v) {
			return false
		}
		continue
	}

	return true
}

// Equal returns true if both slices are exactly equal
func (p Points) Equal(p2 Points) bool {
	return slices.EqualFunc(p, p2, PointsAreEqual)
}

// Size returns the memory utilization of the Points in bytes
func (p Points) Size() int64 {
	var c int64 = 16
	for _, pt := range p {
		c += int64(pt.Size)
	}
	return c
}

// Clone returns a perfect copy of the Points
func (p Points) Clone() Points {
	clone := make(Points, len(p))
	for i, pt := range p {
		clone[i] = pt.Clone()
	}
	return clone
}

// CloneRange returns a perfect copy of the Points, cloning only the
// points in the provided index range (upper-bound exclusive)
func (p Points) CloneRange(start, end int) Points {
	if end < start {
		return nil
	}
	size := end - start
	if size > len(p) {
		return nil
	}
	clone := make(Points, size, size+10)
	j := start
	for i := range size {
		clone[i] = p[j].Clone()
		j++
	}
	return clone
}

// Len returns the length of a slice of time series data points
func (p Points) Len() int {
	return len(p)
}

// Less returns true if i comes before j
func (p Points) Less(i, j int) bool {
	return p[i].Epoch < (p[j].Epoch)
}

// Swap modifies a slice of time series data points by swapping the values in indexes i and j
func (p Points) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

// onOrJustAfter returns the index of the element having a value of ts. if the value of ts
// is not in p, the index of the first element whose value is greater than ts is returned.
// onOrJustafter requires p to be sorted chronologically from earliest to latest epoch.
// This is a variation of justGreater found @ https://stackoverflow.com/a/56815151
func (p Points) onOrJustAfter(ts epoch.Epoch, s, e int) int {
	if s >= e {
		if p[s].Epoch < ts {
			return s + 1
		}
		return s
	}
	mid := (s + e) >> 1
	if p[mid].Epoch < ts {
		return p.onOrJustAfter(ts, mid+1, e)
	}
	return p.onOrJustAfter(ts, s, mid)
}

// onOrJustBefore returns the index of the element having a value of ts. if the value of ts
// is not in p, the index of the last element whose value is less than ts is returned.
// onOrJustBefore requires p to be sorted chronologically from earliest to latest epoch.
// This is a variation of justGreater found @ https://stackoverflow.com/a/56815151
func (p Points) onOrJustBefore(ts epoch.Epoch, s, e int) int {
	if s >= e {
		if p[s].Epoch > ts {
			return s - 1
		}
		return s
	}
	mid := (s + e) >> 1
	if p[mid].Epoch < ts {
		return p.onOrJustBefore(ts, mid+1, e)
	}
	return p.onOrJustBefore(ts, s, mid)
}

// sortAndDedupe sorts and deduplicates p in-place. Because deduplication can
// shorten p, the version of p used to call sortAndDedupe is no longer valid.
// Set p to the call's return value, as in:   p = sortAndDedupe(p)
// In the event of duplicates, the highest index wins.
func sortAndDedupe(p Points) Points {
	// sort, keeping order between equal elements
	slices.SortStableFunc(p, func(a, b Point) int {
		if a.Epoch < b.Epoch {
			return -1
		} else if a.Epoch > b.Epoch {
			return 1
		}
		return 0
	})
	var k int
	for i := range p {
		if i == 0 {
			continue // skip first iteration since there's nothing to compare
		}
		// if Epochs match, the higher-index (latest) version wins de-duplication
		if p[k].Epoch == p[i].Epoch {
			p[k] = p[i]
		} else {
			// at a new Epoch; advance the index
			k++
			// if previous points were deduped, this one must must shift forward
			if k < i {
				p[k] = p[i]
			}
		}
	}
	return p[:k+1]
}

// Merge returns a new Points slice of p and p2 merged together. If sort is true
// the new slice is sorted and dupe-killed before being returned
func MergePoints(p, p2 Points, sortPoints bool) Points {
	if p == nil && p2 == nil {
		return nil
	}
	if len(p) == 0 && len(p2) == 0 {
		return Points{}
	}
	// finalize will check if the output should be sorted+deduped, and if so
	// do that before ultimately returning the output. From this point, all
	// returns include calls to finalize
	finalize := func(out Points) Points {
		if sortPoints && len(out) > 1 {
			out = sortAndDedupe(out)
		}
		return out
	}
	if len(p2) == 0 {
		// if p2 has no items, return a clone of p
		return finalize(p.Clone())
	} else if len(p) == 0 {
		// if p has no items, return a clone of p2
		return finalize(p2.Clone())
	}
	// otherwise, merge the two Points slices
	out := make(Points, len(p)+len(p2))
	copy(out, p)
	copy(out[len(p):], p2)
	return finalize(out)
}
