/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package timeseries

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ExtentList is a type of []Extent used for sorting the slice
type ExtentList []Extent

// String returns a string representation of the extentlist
// in the format startEpochSec1-endEpochSec1;startEpochSec2-endEpochSec2
func (el ExtentList) String() string {
	if len(el) == 0 {
		return ""
	}
	lines := make([]string, len(el))
	for i, e := range el {
		lines[i] = e.String()
	}
	return strings.Join(lines, ",")
}

// InsideOf returns true if the provided extent is contained
// completely within boundaries of the subject ExtentList
func (el ExtentList) InsideOf(e Extent) bool {
	x := len(el)
	if x == 0 {
		return false
	}
	return ((!el[0].Start.Before(e.Start)) &&
		(!el[0].Start.After(e.End)) &&
		(!el[x-1].End.Before(e.Start)) &&
		(!el[x-1].End.After(e.End)))
}

// OutsideOf returns true if the provided extent falls completely
// outside of the boundaries of the subject extent list
func (el ExtentList) OutsideOf(e Extent) bool {
	x := len(el)
	if x == 0 {
		return true
	}
	return e.After(el[x-1].End) || el[0].After(e.End)
}

// Crop reduces the ExtentList to the boundaries defined by the provided Extent
func (el ExtentList) Crop(e Extent) ExtentList {
	var startIndex = -1
	var endIndex = -1
	for i, f := range el {
		if startIndex == -1 {
			if f.Includes(e.Start) {
				if !f.StartsAt(e.Start) {
					el[i].Start = e.Start
				}
				startIndex = i
			} else if f.After(e.Start) && !f.After(e.End) {
				startIndex = i
			} else if f.After(e.Start) && f.After(e.End) {
				return make(ExtentList, 0)
			}
		}
		if endIndex == -1 {
			if f.Includes(e.End) {
				if !f.EndsAt(e.End) {
					el[i].End = e.End
				}
				endIndex = i
			}
		}
	}
	if startIndex != -1 {
		if endIndex == -1 {
			endIndex = len(el) - 1
		}
		endIndex++
		if endIndex >= startIndex {
			cropped := make(ExtentList, len(el[startIndex:endIndex]))
			copy(cropped, el[startIndex:endIndex])
			return cropped
		}
	}
	return make(ExtentList, 0)
}

// Compress sorts an ExtentList and merges time-adjacent Extents so that the total extent of
// data is accurately represented in as few Extents as possible
func (el ExtentList) Compress(step time.Duration) ExtentList {
	exc := el.Clone()
	if len(el) == 0 {
		return exc
	}
	l := len(el)
	compressed := make(ExtentList, 0, l)
	sort.Sort(exc)
	e := Extent{}
	extr := Extent{}
	for i := range exc {
		e.LastUsed = exc[i].LastUsed
		if e.Start.IsZero() && !exc[i].Start.IsZero() {
			e.Start = exc[i].Start
			if extr.Start.IsZero() {
				extr.Start = e.Start
			}
		}
		if exc[i].End.Before(extr.End) {
			continue
		}
		if i+1 < l && ((exc[i].End.Add(step).Equal(exc[i+1].Start) ||
			exc[i].End.Equal(exc[i+1].Start)) && exc[i].LastUsed.Equal(exc[i+1].LastUsed) ||
			exc[i].End.Equal(exc[i+1].End) && exc[i].Start.Equal(exc[i+1].Start)) {
			continue
		}
		e.End = exc[i].End
		if e.End.After(extr.End) {
			extr.End = e.End
		}
		compressed = append(compressed, e)
		e = Extent{}
	}
	return compressed
}

// Len returns the length of a slice of type ExtentList
func (el ExtentList) Len() int {
	return len(el)
}

// Less returns true if element i in the ExtentList comes before j
func (el ExtentList) Less(i, j int) bool {
	return el[i].Start.Before(el[j].Start)
}

// Swap modifies an ExtentList by swapping the values in indexes i and j
func (el ExtentList) Swap(i, j int) {
	el[i], el[j] = el[j], el[i]
}

// Clone returns a true copy of the ExtentList
func (el ExtentList) Clone() ExtentList {
	c := make(ExtentList, len(el))
	for i := range el {
		c[i].Start = el[i].Start
		c[i].End = el[i].End
		c[i].LastUsed = el[i].LastUsed
	}
	return c
}

// TimestampCount returns the calculated number of timestamps based on the extents
// in the list and the provided duration
func (el ExtentList) TimestampCount(d time.Duration) int64 {
	var c int64
	for i := range el {
		if el[i].Start.IsZero() || el[i].End.IsZero() {
			continue
		}
		c += ((el[i].End.UnixNano() - el[i].Start.UnixNano()) / d.Nanoseconds()) + 1
	}
	return c
}

// CalculateDeltas provides a list of extents that are not in a cached timeseries,
// when provided a list of extents that are cached.
func (el ExtentList) CalculateDeltas(want Extent, step time.Duration) ExtentList {
	if len(el) == 0 {
		return ExtentList{want}
	}
	misCap := want.End.Sub(want.Start) / step
	if misCap < 0 {
		misCap = 0
	}
	misses := make([]time.Time, 0, misCap)
	for i := want.Start; !want.End.Before(i); i = i.Add(step) {
		found := false
		for j := range el {
			if j == 0 && i.Before(el[j].Start) {
				// our earliest datapoint in cache is after the first point the user wants
				break
			}
			if i.Equal(el[j].Start) || i.Equal(el[j].End) || (i.After(el[j].Start) && el[j].End.After(i)) {
				found = true
				break
			}
		}
		if !found {
			misses = append(misses, i)
		}
	}
	// Find the fill and gap ranges
	ins := ExtentList{}
	var inStart = time.Time{}
	l := len(misses)
	for i := range misses {
		if inStart.IsZero() {
			inStart = misses[i]
		}
		if i+1 == l || !misses[i+1].Equal(misses[i].Add(step)) {
			ins = append(ins, Extent{Start: inStart, End: misses[i]})
			inStart = time.Time{}
		}
	}
	return ins
}

// Size returns the approximate memory utilization in bytes of the timeseries
func (el ExtentList) Size() int {
	return len(el) * 72
}

// ExtentListLRU is a type of []Extent used for sorting the slice by LRU
type ExtentListLRU []Extent

// Len returns the length of an slice of type ExtentListLRU
func (el ExtentListLRU) Len() int {
	return len(el)
}

// Less returns true if element i in the ExtentListLRU comes before j
func (el ExtentListLRU) Less(i, j int) bool {
	return el[i].LastUsed.Before(el[j].LastUsed)
}

// Swap modifies an ExtentListLRU by swapping the values in indexes i and j
func (el ExtentListLRU) Swap(i, j int) {
	el[i], el[j] = el[j], el[i]
}

// Clone returns a true copy of the ExtentListLRU
func (el ExtentListLRU) Clone() ExtentListLRU {
	c := make(ExtentListLRU, len(el))
	for i := range el {
		c[i].Start = el[i].Start
		c[i].End = el[i].End
		c[i].LastUsed = el[i].LastUsed
	}
	return c
}

func (el ExtentListLRU) String() string {
	if len(el) == 0 {
		return ""
	}
	lines := make([]string, 0, len(el))
	for _, e := range el {
		lines = append(lines, fmt.Sprintf("%d-%d:%d", e.Start.Unix(), e.End.Unix(), e.LastUsed.Unix()))
	}
	return strings.Join(lines, ",")
}

// UpdateLastUsed updates the ExtentListLRU's LastUsed field for the provided extent.
// The step is required in order to properly split extents.
func (el ExtentListLRU) UpdateLastUsed(lur Extent, step time.Duration) ExtentListLRU {

	if el == nil {
		return nil
	}

	if len(el) == 0 {
		return ExtentListLRU{}
	}

	now := time.Now().Truncate(time.Second)
	el2 := make(ExtentList, 0, len(el))

	for _, x := range el {

		// This case captures when extent x is sandwiched between the
		// extents in the list containing lur.Start and lur.End
		// So we'll mark its Last Used and move on without splitting.
		if !lur.Start.After(x.Start) && !lur.End.Before(x.End) {
			x.LastUsed = now
			el2 = append(el2, x)
			continue
		}

		// The LastUsed extent is before or after this entire extent
		// so we don't do anything
		if x.Start.After(lur.End) || x.End.Before(lur.Start) {
			el2 = append(el2, x)
			continue
		}

		// The Last Used Range starts in this extent, but not on the starting edge
		// So we'll break it up into two pieces on that start point
		if lur.Start.After(x.Start) && !lur.Start.After(x.End) {
			// v will serve as the left portion of x that we broke off
			// it is outside of the Last Used Range so LU is untouched
			v := Extent{Start: x.Start, End: lur.Start.Add(-step), LastUsed: x.LastUsed}
			x.Start = lur.Start
			el2 = append(el2, v)

			// The right portion may be fully enclosed by the LUR, if so
			// go ahead an mark the usage time, append to our new ExtentList and move on
			if !lur.End.Before(x.End) {
				x.LastUsed = now
				el2 = append(el2, x)
				continue
			}
		}

		// If we got here, the LUR covers a left portion of this extent, break it up and append
		if lur.End.Before(x.End) && !lur.End.Before(x.Start) {
			y := Extent{Start: lur.End.Add(step), End: x.End, LastUsed: x.LastUsed}
			x.End = lur.End
			x.LastUsed = now
			el2 = append(el2, x, y)
			continue
		}
	}
	return ExtentListLRU(el2.Compress(step))
}
