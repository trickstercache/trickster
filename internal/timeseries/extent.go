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
	var notime time.Time
	l := len(extents)
	sort.Sort(ExtentList(extents))
	compressed := make([]Extent, 0, l)
	e := Extent{}
	for i := range extents {
		if e.Start == notime {
			e.Start = extents[i].Start
		}
		if i+1 < l && (extents[i].End.Add(step) == extents[i+1].Start || extents[i].End == extents[i+1].Start) {
			continue
		}
		e.End = extents[i].End
		compressed = append(compressed, e)
		e = Extent{}
	}
	return compressed
}

// ExtentList is a type of []Extent used for sorting the slice
type ExtentList []Extent

// Len returns the length of an array of type []Extent
func (e ExtentList) Len() int {
	return len(e)
}

// Less returns true if element i in the []Extent comes before j
func (e ExtentList) Less(i, j int) bool {
	return e[i].Start.Before(e[j].Start)
}

// Swap modifies an array by of Prometheus model.Times swapping the values in indexes i and j
func (e ExtentList) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}
