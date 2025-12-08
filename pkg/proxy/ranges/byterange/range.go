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

// Package byterange provides acceleration functions for Byte Ranges
// for use with HTTP Range Requests
package byterange

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/segments"
)

//go:generate go tool msgp

// Range represents the start and end for a byte range object
type Range struct {
	Start int64 `msg:"start"`
	End   int64 `msg:"end"`
}

// Ranges represents a slice of type Range
// The objects in the slice may not overlap value coverage,
// meaning any given value can be covered no more than once by the slice
// Good: [ 1-10, 21-30, 35-40 ]; Bad: [ 1-10, 10-20 ]; Bad: [ 1-10, 5-20 ]
type Ranges []Range

const (
	byteRequestRangePrefix = "bytes="
	byteResponsRangePrefix = "bytes "
)

func (r Range) StartVal() int64 { return r.Start }
func (r Range) EndVal() int64   { return r.End }
func (r Range) NewSegment(start, end int64) segments.Segment[int64] {
	return Range{Start: start, End: end}
}

// ContentRangeHeader returns a 'Content-Range' header representing the extent of the subject range
func (r Range) ContentRangeHeader(contentLength int64) string {
	var start string
	var end string
	cl := "*"
	if r.Start >= 0 {
		start = strconv.FormatInt(r.Start, 10)
	}
	if r.End >= 0 {
		end = strconv.FormatInt(r.End, 10)
	}
	if contentLength > 0 {
		cl = strconv.FormatInt(contentLength, 10)
	}
	return byteResponsRangePrefix + start + "-" + end + "/" + cl
}

func (r Range) Mod(i int64) Range {
	return Range{
		Start: r.Start % i,
		End:   r.End % i,
	}
}

// Crop a byte slice to this byterange.
// Generally equal to b[r.Start-offset:r.End-offset+1], but will automatically adjust the end to avoid overflow.
// Use offset if b is a part of a whole.
func (r Range) CropByteSlice(b []byte) ([]byte, Range) {
	over := max((r.End+1)-int64(len(b)), 0)
	return b[r.Start : r.End+1-over], Range{Start: r.Start, End: r.End - over}
}

// Copy a source byte slice, whose data range is represented by r, into dst in the range of r.
// If src is smaller than r, Copy assumes that r.End should be reduced by the overage.
func (r Range) Copy(dst []byte, src []byte) int {
	over := max(r.End-r.Start+1-int64(len(src)), 0)
	return copy(dst[r.Start:r.End+1-over], src)
}

// CalculateDeltas calculates the delta between two Ranges
func (rs Ranges) CalculateDeltas(needs Ranges, fullContentLength int64) Ranges {
	if len(rs) == 0 || fullContentLength <= 0 {
		return needs
	}
	needs = needs.Clone()
	for i, n := range needs {
		if n.Start >= 0 && n.End >= 0 {
			continue
		}
		if n.Start < 0 {
			needs[i].Start = fullContentLength - n.End
		}
		needs[i].End = fullContentLength - 1
	}
	sort.Sort(rs)
	sort.Sort(needs)
	out := Ranges(segments.Diff(rs, needs, int64(1), segments.Int64{}))
	return out.Compress()
}

func (rs Ranges) Clone() Ranges {
	rs2 := make(Ranges, len(rs))
	copy(rs2, rs)
	return rs2
}

// Crop a byte slice to a series of ranges.
// This results in a byte slice of a length equal to the maximum value within rs,
// where all values within rs are set and all others are zero.
// Use offset if b is part of a whole.
func (rs Ranges) FilterByteSlice(b []byte) []byte {
	sort.Sort(rs)
	out := make([]byte, rs[len(rs)-1].End)
	for _, r := range rs {
		content, _ := r.CropByteSlice(b)
		r.Copy(out, content)
	}
	return out
}

// ParseContentRangeHeader returns a Ranges list from the provided input,
// which must be a properly-formatted HTTP 'Content-Range' Response Header value
func ParseContentRangeHeader(input string) (Range, int64, error) {
	errorResponse := func() (Range, int64, error) {
		return Range{}, -1, errors.New("invalid input format")
	}
	if !strings.HasPrefix(input, byteResponsRangePrefix) {
		return errorResponse()
	}
	input = strings.ReplaceAll(input[6:], " ", "")
	var haveStart bool
	var j int
	r := Range{}
	for i := range len(input) {
		if input[i] < '0' || input[i] > '9' {
			// closes out the 'start' section of the range string
			if !haveStart && input[i] == '-' {
				r.Start, _ = strconv.ParseInt(input[j:i], 10, 64)
				j = i + 1
				haveStart = true
				continue
			}
			// closes out the 'end' section of the range string
			if haveStart && input[i] == '/' {
				r.End, _ = strconv.ParseInt(input[j:i], 10, 64)
				j = i + 1
				break
			}
			return errorResponse()
		}
	}
	if input[j:] == "*" {
		return errorResponse()
	}
	cl, err := strconv.ParseInt(input[j:], 10, 64)
	if err != nil {
		return errorResponse()
	}
	return r, cl, nil
}

// ParseRangeHeader returns a Ranges list from the provided input,
// which must be a properly-formatted HTTP 'Range' Request Header value
func ParseRangeHeader(input string) Ranges {
	if input == "" || !strings.HasPrefix(input, byteRequestRangePrefix) ||
		input == byteRequestRangePrefix {
		return nil
	}
	input = strings.ReplaceAll(input, " ", "")[6:]
	parts := strings.Split(input, ",")
	ranges := make(Ranges, len(parts))

	for i, p := range parts {
		j := strings.Index(p, "-")
		if j < 0 {
			return nil
		}

		start := int64(-1)
		end := int64(-1)
		var err error

		if j > 0 {
			start, err = strconv.ParseInt(p[0:j], 10, 64)
			if err != nil {
				return nil
			}
		}

		if j < len(p)-1 {
			end, err = strconv.ParseInt(p[j+1:], 10, 64)
			if err != nil {
				return nil
			}
		}

		ranges[i].Start = start
		ranges[i].End = end
	}

	sort.Sort(ranges)
	return ranges
}

// Equal returns true if the compared byte range slices are equal
// and assumes that the Ranges are sorted
func (rs Ranges) Equal(rs2 Ranges) bool {
	if rs2 == nil {
		return false
	}
	if len(rs) != len(rs2) {
		return false
	}
	for i := range rs {
		if rs[i] != rs2[i] {
			return false
		}
	}
	return true
}

// sort.Interface required functions for Ranges

// Len returns the length of an slice of type Ranges
func (rs Ranges) Len() int {
	return len(rs)
}

// Less returns true if element i in the Ranges comes before j
func (rs Ranges) Less(i, j int) bool {
	return rs[i].Start < (rs[j].Start)
}

// Swap modifies an Ranges by swapping the values in indexes i and j
func (rs Ranges) Swap(i, j int) {
	rs[i], rs[j] = rs[j], rs[i]
}

// Less returns true if element i in the Ranges comes before j
func (r Range) Less(r2 Range) bool {
	return r.Start < r2.Start
}

func (r Range) String() string {
	var start string
	var end string
	if r.Start >= 0 {
		start = strconv.FormatInt(r.Start, 10)
	}
	if r.End >= 0 {
		end = strconv.FormatInt(r.End, 10)
	}
	return start + "-" + end
}

func (rs Ranges) String() string {
	if len(rs) == 0 {
		return ""
	}
	s := make([]string, len(rs))
	for i, v := range rs {
		s[i] = v.String()
	}
	return byteRequestRangePrefix + strings.Join(s, ", ")
}

func (rs Ranges) Compress() Ranges {
	if len(rs) == 0 {
		return Ranges{}
	}
	rs2 := rs.Clone()
	sort.Sort(rs2)
	current := rs2[0]
	if rs2[0].Start < 0 || rs2[0].End < 0 {
		// don't compress Ranges that include suffix byte values
		return rs
	}
	out := make(Ranges, len(rs))
	var k int
	for i := 1; i < len(rs); i++ {
		next := rs2[i]
		if next.Start < 0 || next.End < 0 {
			// don't compress Ranges that include suffix byte values
			return rs
		}
		if next.Start < current.End+1 {
			if next.End > current.End {
				current.End = next.End
			}
			continue
		}
		out[k] = current
		k++
		current = next
	}
	out[k] = current
	k++
	return out[:k]
}
