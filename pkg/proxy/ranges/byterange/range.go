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
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:generate msgp

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

func (br Range) String() string {

	var start string
	var end string
	if br.Start >= 0 {
		start = strconv.FormatInt(br.Start, 10)
	}
	if br.End >= 0 {
		end = strconv.FormatInt(br.End, 10)
	}
	return start + "-" + end
}

// ContentRangeHeader returns a 'Content-Range' header representing the extent of the subject range
func (br Range) ContentRangeHeader(contentLength int64) string {
	var start string
	var end string
	cl := "*"
	if br.Start >= 0 {
		start = strconv.FormatInt(br.Start, 10)
	}
	if br.End >= 0 {
		end = strconv.FormatInt(br.End, 10)
	}
	if contentLength > 0 {
		cl = strconv.FormatInt(contentLength, 10)
	}
	return byteResponsRangePrefix + start + "-" + end + "/" + cl
}

func (br Range) Mod(i int64) Range {
	return Range{
		Start: br.Start % i,
		End:   br.End % i,
	}
}

// Crop a byte slice to this byterange.
// Generally equal to b[br.Start-offset:br.End-offset+1], but will automatically adjust the end to avoid overflow.
// Use offset if b is a part of a whole.
func (br Range) CropByteSlice(b []byte) ([]byte, Range) {
	over := (br.End + 1) - int64(len(b))
	if over < 0 {
		over = 0
	}
	return b[br.Start : br.End+1-over], Range{Start: br.Start, End: br.End - over}
}

// Copy a source byte slice, whose data range is represented by br, into dst in the range of br.
// If src is smaller than br, Copy assumes that br.End should be reduced by the overage.
func (br Range) Copy(dst []byte, src []byte) int {
	over := br.End - br.Start + 1 - int64(len(src))
	if over < 0 {
		over = 0
	}
	return copy(dst[br.Start:br.End+1-over], src)
}

func (brs Ranges) String() string {
	if len(brs) == 0 {
		return ""
	}
	sb := strings.Builder{}
	sb.WriteString(byteRequestRangePrefix)
	var sep string
	for _, r := range brs {
		sb.WriteString(fmt.Sprintf("%s%s", sep, r.String()))
		sep = ", "
	}
	return sb.String()
}

// CalculateDelta calculates the delta between two Ranges
func (brs Ranges) CalculateDelta(haves Ranges, fullContentLength int64) Ranges {

	checkpoint := int64(-1)
	if len(brs) == 0 {
		return haves
	}
	if haves == nil || fullContentLength < 1 || len(haves) == 0 {
		return brs
	}
	if brs.Equal(haves) {
		return Ranges{}
	}

	sort.Sort(brs)
	sort.Sort(haves)
	need := make(Ranges, 0, len(brs)+len(haves))

	deltaRange := func() Range {
		return Range{Start: -1, End: -1}
	}
	nr := deltaRange()

	for i, want := range brs {

		// adjust any prefix/suffix ranges to known start/ends
		if want.Start == -1 || want.End == -1 {
			if want.Start == -1 {
				want.Start = fullContentLength - want.End
			}
			want.End = fullContentLength - 1
			brs[i] = want
		}
		if want.End > fullContentLength {
			// end is out of bounds, consider a full miss
			return brs
		}

		checked := false
		// now compare to any cached ranges to determine any ranges that are not in cache
		for _, have := range haves {

			if have.End < checkpoint {
				continue
			}

			if have.Start > want.End {
				if nr.Start > -1 && nr.End == -1 {
					nr.End = want.End
					checkpoint = nr.End
					need = append(need, nr)
					checked = true
					nr = deltaRange()
				}
				break
			}
			if want.Start > have.End {
				if i < len(haves) {
					nr.Start = want.Start
				}
				continue
			}
			if want.Start >= have.Start && want.Start <= have.End &&
				want.End <= have.End && want.End >= have.Start {
				checked = true
				nr = deltaRange()
				continue
			}
			if nr.Start == -1 {
				// want and have share mutual start and/or ends
				if want.Start >= have.Start {
					// they are identical, break and move on
					if want.End <= have.End {
						break
					}
					nr.Start = have.End + 1
					continue
				}
				nr.Start = want.Start
			}
			if want.End <= have.End {

				if nr.Start > -1 && have.Start > 0 {
					nr.End = have.Start - 1
					need = append(need, nr)
				}
				checked = true
				nr = deltaRange()
				continue
			}
			if want.Start < have.Start && want.End > have.End {
				nr.End = have.Start - 1
				checkpoint = nr.End
				need = append(need, nr)
				checked = true
				nr = deltaRange()
				nr.Start = have.End + 1
			}
			if want.Start >= have.Start && want.Start <= have.End && want.End > have.End {
				nr.Start = have.End + 1
			}
		}
		if !checked {
			if nr.Start > -1 {
				want.Start = nr.Start
			}
			need = append(need, want)
			nr = deltaRange()
		}
	}

	if nr.Start != -1 && nr.End == -1 {
		nr.End = brs[len(brs)-1].End
		need = append(need, nr)
	}
	sort.Sort(need)
	return need
}

func (brs Ranges) Clone() Ranges {
	brs2 := make(Ranges, len(brs))
	copy(brs2, brs)
	return brs2
}

// Crop a byte slice to a series of ranges.
// This results in a byte slice of a length equal to the maximum value within brs, where all values within brs are set
// and all others are zero.
// Use offset if b is part of a whole.
func (brs Ranges) FilterByteSlice(b []byte) []byte {
	sort.Sort(brs)
	out := make([]byte, brs[len(brs)-1].End)
	for _, br := range brs {
		content, _ := br.CropByteSlice(b)
		br.Copy(out, content)
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
	input = strings.Replace(input, " ", "", -1)[6:]
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
func (brs Ranges) Equal(brs2 Ranges) bool {
	if brs2 == nil {
		return false
	}
	if len(brs) != len(brs2) {
		return false
	}
	for i := range brs {
		if brs[i] != brs2[i] {
			return false
		}
	}
	return true
}

// sort.Interface required functions for Ranges

// Len returns the length of an slice of type Ranges
func (brs Ranges) Len() int {
	return len(brs)
}

// Less returns true if element i in the Ranges comes before j
func (brs Ranges) Less(i, j int) bool {
	return brs[i].Start < (brs[j].Start)
}

// Swap modifies an Ranges by swapping the values in indexes i and j
func (brs Ranges) Swap(i, j int) {
	brs[i], brs[j] = brs[j], brs[i]
}

// Less returns true if element i in the Ranges comes before j
func (br Range) Less(br2 Range) bool {
	return br.Start < br2.Start
}
