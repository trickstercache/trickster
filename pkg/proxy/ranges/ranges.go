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

package ranges

import (
	"strings"
	"time"
)

type Datumv2[d Datum, i Interval] interface {
	Add(i) d
	Sub(d) i
}

type Int64datumn int64

func (i Int64datumn) Add(d int64) Int64datumn {
	return Int64datumn(int64(i) + int64(d))
}

func (i Int64datumn) Sub(o Int64datumn) int64 {
	return int64(i) - int64(o)
}

/// ^^^ wip

type Interval interface {
	time.Duration | int64
}

type Datum interface {
	Int64datumn | time.Time | int64
}

type Extent[d Datum] interface {
	// Includes returns true if the Extent includes the provided Time
	Includes(d) bool
	// After returns true if the range of the Extent is completely after the provided time
	After(d) bool
	// Before returns true if the range of the Extent is completely before the provided time
	Before(d) bool

	// Crop returns a new Extent with the provided start and end times
	Crop(d, d) Extent[d]

	// StartIndex returns the start time of the Extent
	StartIndex() d
	// StartsAt returns true if t is equal to the Extent's start time
	StartsAt(d) bool
	// StartsAfter returns true if t is after the Extent's start time
	StartsAfter(d) bool
	// StartsBefore returns true if t is before the Extent's start time
	StartsBefore(d) bool
	// StartsAtOrBefore returns true if t is equal or before to the Extent's start time
	StartsAtOrBefore(d) bool
	// StartsAtOrAfter returns true if t is equal to or after the Extent's start time
	StartsAtOrAfter(d) bool

	// EndIndex returns the end time of the Extent
	EndIndex() d
	// EndsAt returns true if t is equal to the Extent's end time
	EndsAt(d) bool
	// EndsAfter returns true if t is after the Extent's end time
	EndsAfter(d) bool
	// EndsBefore returns true if t is before the Extent's end time
	EndsBefore(d) bool
	// EndsAtOrBefore returns true if t is equal to or earlier than the Extent's end time
	EndsAtOrBefore(d) bool
	// EndsAtOrAfter returns true if t is equal to or after the Extent's end time
	EndsAtOrAfter(d) bool

	// String returns the string representation of the Extent
	String() string
}

type ExtentList[d Datum] []Extent[d]

func (el ExtentList[d]) String() string {
	if len(el) == 0 {
		return ""
	}
	lines := make([]string, len(el))
	for i, e := range el {
		lines[i] = e.String()
	}
	return strings.Join(lines, ",")
}

// Encompasses returns true if the provided extent is contained
// completely within boundaries of the subject ExtentList
func (el ExtentList[d]) Encompasses(e Extent[d]) bool {
	x := len(el)
	if x == 0 {
		return false
	}
	return el[0].StartsAtOrBefore(e.StartIndex()) && el[x-1].EndsAtOrAfter(e.EndIndex())
}

// EncompassedBy returns true if the provided extent completely
// surrounds the boundaries of the subject ExtentList
func (el ExtentList[d]) EncompassedBy(e Extent[d]) bool {
	x := len(el)
	if x == 0 {
		return false
	}
	return e.StartsAtOrBefore(el[0].StartIndex()) && e.EndsAtOrAfter(el[x-1].EndIndex())
}

// OutsideOf returns true if the provided extent falls completely
// outside of the boundaries of the subject extent list
func (el ExtentList[d]) OutsideOf(e Extent[d]) bool {
	x := len(el)
	if x == 0 {
		return true
	}
	return e.After(el[x-1].EndIndex()) || el[0].After(e.EndIndex())
}

// Crop reduces the ExtentList to the boundaries defined by the provided Extent
func (el ExtentList[d]) Crop(ex Extent[d]) ExtentList[d] {
	if len(el) == 0 {
		return ExtentList[d]{}
	}
	out := make(ExtentList[d], len(el))
	var k int
	for _, e := range el {
		if e.Before(ex.StartIndex()) || e.After(ex.EndIndex()) {
			continue
		}
		start := e.StartIndex()
		end := e.EndIndex()
		if ex.StartsAfter(start) && ex.StartsBefore(end) {
			start = ex.StartIndex()
		} else if ex.StartsAt(end) {
			start = ex.StartIndex()
			end = ex.StartIndex()
		}
		if ex.Before(end) && ex.EndsAfter(start) {
			end = ex.EndIndex()
		} else if ex.EndsAt(start) {
			start = ex.EndIndex()
			end = ex.EndIndex()
		}
		out[k] = e.Crop(start, end) // FIXME: last used not set / exposed
		k++
	}
	return out[:k]
}

// Clone returns a true copy of the ExtentList
func (el ExtentList[d]) Clone() ExtentList[d] {
	out := make(ExtentList[d], len(el))
	// this is safe because all fields in an Extent are by value
	copy(out, el)
	return out
}

// Merge returns an Extentlist of el2 appended to el, sorted, and compressed
func (el ExtentList[d]) Merge(el2 ExtentList[d]) ExtentList[d] {
	if len(el2) == 0 {
		return el.Clone()
	}
	if len(el) == 0 {
		return el2.Clone()
	}
	out := make(ExtentList[d], len(el)+len(el2))
	copy(out, el)
	copy(out[len(el):], el2)

	// slices.Sort(out)
	// return out.Compress(step)
	return out
}
