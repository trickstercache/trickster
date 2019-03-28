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
	"sort"
	"time"
)

// Extent describes the start and end times for a given range of data
type Extent struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// CompressExtents takes an []Extent slice, sorts it, and returns a version with
// any time-adjacent Extents merged into a single element in the slice
func CompressExtents(extents []Extent, step time.Duration) []Extent {

	if len(extents) == 0 {
		return extents
	}

	var notime time.Time
	l := len(extents)
	exc := ExtentList(extents).Copy()
	compressed := make([]Extent, 0, l)
	sort.Sort(exc)
	e := Extent{}
	for i := range exc {
		if e.Start == notime {
			e.Start = exc[i].Start
		}
		if i+1 < l && (exc[i].End.Add(step) == exc[i+1].Start || exc[i].End == exc[i+1].Start) {
			continue
		}
		e.End = exc[i].End
		compressed = append(compressed, e)
		e = Extent{}
	}
	return compressed
}

// ExtentList is a type of []Extent used for sorting the slice
type ExtentList []Extent

// Copy returns a true copy of the ExtentList
func (el ExtentList) Copy() ExtentList {
	c := make(ExtentList, len(el))
	for i := range el {
		c[i].Start = el[i].Start
		c[i].End = el[i].End
	}
	return c
}

// Len returns the length of an array of type []Extent
func (el ExtentList) Len() int {
	return len(el)
}

// Less returns true if element i in the []Extent comes before j
func (el ExtentList) Less(i, j int) bool {
	return el[i].Start.Before(el[j].Start)
}

// Swap modifies an array by of Prometheus model.Times swapping the values in indexes i and j
func (el ExtentList) Swap(i, j int) {
	el[i], el[j] = el[j], el[i]
}
