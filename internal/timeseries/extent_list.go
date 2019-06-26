/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

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
	lines := make([]string, 0, len(el))
	for _, e := range el {
		lines = append(lines, fmt.Sprintf("%d-%d", e.Start.Unix(), e.End.Unix()))
	}
	return strings.Join(lines, ";")
}

// Contains ...
func (el ExtentList) Contains(e Extent) bool {
	x := len(el)
	if x == 0 {
		return false
	}
	return ((!el[0].Start.Before(e.Start)) &&
		(!el[0].Start.After(e.End)) &&
		(!el[x-1].End.Before(e.Start)) &&
		(!el[x-1].End.After(e.End)) &&
		(!el[0].Start.After(el[x-1].End)))
}

// OutsideOf ...
func (el ExtentList) OutsideOf(e Extent) bool {
	x := len(el)
	if x == 0 {
		return true
	}
	return e.After(el[x-1].End) || el[0].After(e.End)
}

// Crop ...
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
				return make(ExtentList, 0, 0)
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
			return el[startIndex:endIndex]
		}
	}
	return make(ExtentList, 0, 0)
}

// Compress sorts an ExtentList and merges time-adjacent Extents so that the total extent of
// data is accurately represented in as few Extents as possible
func (el ExtentList) Compress(step time.Duration) ExtentList {
	exc := ExtentList(el).Copy()
	if len(el) == 0 {
		return exc
	}
	l := len(el)
	compressed := make(ExtentList, 0, l)
	sort.Sort(exc)
	e := Extent{}
	for i := range exc {
		if e.Start.IsZero() {
			e.Start = exc[i].Start
		}
		if i+1 < l && (exc[i].End.Add(step).Equal(exc[i+1].Start) || exc[i].End.Equal(exc[i+1].Start)) {
			continue
		}
		e.End = exc[i].End
		compressed = append(compressed, e)
		e = Extent{}
	}
	return compressed
}

// Len returns the length of an array of type ExtentList
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

// Copy returns a true copy of the ExtentList
func (el ExtentList) Copy() ExtentList {
	c := make(ExtentList, len(el))
	for i := range el {
		c[i].Start = el[i].Start
		c[i].End = el[i].End
	}
	return c
}
