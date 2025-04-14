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

//go:generate msgp

package dataset

import (
	"slices"
	"sort"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
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
		switch t := v.(type) {
		case float64:
			if f, ok := p2.Values[i].(float64); !ok || t != f {
				return false
			} else {
				continue
			}
		case int64:
			if i, ok := p2.Values[i].(int64); !ok || t != i {
				return false
			} else {
				continue
			}
		case bool:
			if t2, ok := p2.Values[i].(bool); !ok || t != t2 {
				return false
			} else {
				continue
			}
		case float32:
			if f, ok := p2.Values[i].(float32); !ok || t != f {
				return false
			} else {
				continue
			}
		case int:
			if i, ok := p2.Values[i].(int); !ok || t != i {
				return false
			} else {
				continue
			}
		case string:
			if s, ok := p2.Values[i].(string); !ok || t != s {
				return false
			} else {
				continue
			}
		}
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
	for i := 0; i < size; i++ {
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

// Merge returns a new Points slice of p and p2 merged together. If sort is true
// the new slice is sorted and dupe-killed before being returned
func MergePoints(p, p2 Points, sortPoints bool) Points {
	if p == nil && p2 == nil {
		return nil
	}
	if len(p) == 0 && len(p2) == 0 {
		return Points{}
	}
	sortDedupe := func(in Points) int {
		n := len(in)
		sort.Sort(in)
		var k int
		for l := range n {
			if l+1 == n || in[l].Epoch != in[l+1].Epoch {
				in[k] = in[l]
				k++
			}
		}
		return k
	}

	if len(p2) == 0 {
		out := p.Clone()
		if sortPoints && len(out) > 1 {
			i := sortDedupe(out)
			out = out[:i]
		}
		return out
	} else if len(p) == 0 {
		// if p has no items, clone p2 into p
		out := p2.Clone()
		if sortPoints && len(out) > 1 {
			i := sortDedupe(out)
			out = out[:i]
		}
		return out
	}
	// otherwise, we merge the points from the two slices
	out := make(Points, len(p)+len(p2))
	copy(out, p)
	copy(out[len(p):], p2)
	if sortPoints {
		i := sortDedupe(out)
		out = out[:i]
	}
	return out
}
