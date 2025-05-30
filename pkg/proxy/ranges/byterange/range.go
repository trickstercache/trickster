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
	"regexp"
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
// meaning values are contained within <= 1 Range in the slice
// Good: [ 1-10, 21-30, 35-40 ]; Bad: [ 1-10, 10-20 ]; Bad: [ 1-10, 5-20 ]
type Ranges []Range

const byteRequestRangePrefix = "bytes="
const byteResponsRangePrefix = "bytes "

var respRE *regexp.Regexp

func init() {
	respRE = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)\/([0-9]+)$`)
}

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
	over := (r.End + 1) - int64(len(b))
	if over < 0 {
		over = 0
	}
	return b[r.Start : r.End+1-over], Range{Start: r.Start, End: r.End - over}
}

// Copy a source byte slice, whose data range is represented by r, into dst in the range of r.
// If src is smaller than r, Copy assumes that r.End should be reduced by the overage.
func (r Range) Copy(dst []byte, src []byte) int {
	over := r.End - r.Start + 1 - int64(len(src))
	if over < 0 {
		over = 0
	}
	return copy(dst[r.Start:r.End+1-over], src)
}

// CalculateDeltas calculates the delta between two Ranges
func (rs Ranges) CalculateDeltas(need Ranges, fullContentLength int64) Ranges {
	return segments.Diff(rs, need, int64(1), segments.Int64{})
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
	parts := respRE.FindAllStringSubmatch(input, -1)
	if len(parts) == 1 && len(parts[0]) == 4 {

		r := Range{}
		r.Start, _ = strconv.ParseInt(parts[0][1], 10, 64)
		r.End, _ = strconv.ParseInt(parts[0][2], 10, 64)
		if parts[0][3] == "*" {
			return r, -1, nil
		}
		cl, _ := strconv.ParseInt(parts[0][3], 10, 64)
		return r, cl, nil
	}
	return Range{}, -1, errors.New("invalid input format")
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

		var start = int64(-1)
		var end = int64(-1)
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
	return strings.Join(s, ",")
}
