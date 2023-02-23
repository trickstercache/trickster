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

package byterange

import (
	"fmt"
	"sort"
	"strconv"
	"testing"
)

func TestRanges_CalculateDelta(t *testing.T) {

	tests := []struct {
		want, have, expected Ranges
		cl                   int64
	}{
		{
			// case 0  where we need both outer permiters of the wanted range
			want:     Ranges{Range{Start: 5, End: 10}},
			have:     Ranges{Range{Start: 6, End: 9}},
			expected: Ranges{Range{Start: 5, End: 5}, Range{Start: 10, End: 10}},
			cl:       62,
		},
		{
			// case 1  where the needed range is out of known bounds
			want:     Ranges{Range{Start: 100, End: 100}},
			have:     Ranges{Range{Start: 6, End: 9}},
			expected: Ranges{Range{Start: 100, End: 100}},
			cl:       62,
		},
		{
			// case 2  where the needed range is identical to have range
			want:     Ranges{Range{Start: 6, End: 9}},
			have:     Ranges{Range{Start: 6, End: 9}},
			expected: Ranges{},
			cl:       62,
		},
		{
			// case 3  where we want a suffix range ("bytes=-50")
			want:     Ranges{Range{Start: -1, End: 50}},
			have:     Ranges{Range{Start: 0, End: 30}},
			expected: Ranges{Range{Start: 31, End: 69}},
			cl:       70,
		},
		{
			// case 4  where we want a prefix range ("bytes=50-")
			want:     Ranges{Range{Start: 30, End: -1}},
			have:     Ranges{Range{Start: 0, End: 40}},
			expected: Ranges{Range{Start: 41, End: 69}},
			cl:       70,
		},
		{
			// case 5  where we have a few absolute ranges #1
			want:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 29}},
			have:     Ranges{Range{Start: 0, End: 25}},
			expected: Ranges{Range{Start: 26, End: 29}},
			cl:       70,
		},
		{
			// case 6  where we have a few absolute ranges #2
			want:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 29}},
			have:     Ranges{Range{Start: 0, End: 6}, Range{Start: 17, End: 32}},
			expected: Ranges{Range{Start: 7, End: 10}},
			cl:       70,
		},
		{
			// case 7  where we have a few absolute ranges #3
			want:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 29}},
			have:     Ranges{Range{Start: 0, End: 6}, Range{Start: 25, End: 32}},
			expected: Ranges{Range{Start: 7, End: 10}, Range{Start: 20, End: 24}},
			cl:       70,
		},
		{
			// case 8  where we have a few absolute ranges #4
			want:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 29}},
			have:     Ranges{Range{Start: 0, End: 6}, Range{Start: 20, End: 27}},
			expected: Ranges{Range{Start: 7, End: 10}, Range{Start: 28, End: 29}},
			cl:       70,
		},
		{
			// case 9 where we have all empty ranges
			want:     Ranges{},
			have:     Ranges{},
			expected: Ranges{},
			cl:       1,
		},
		{
			// case 10 where we have no saved ranges
			want:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 29}},
			have:     nil,
			expected: Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 29}},
			cl:       1,
		},
		{
			// case 11 partial hit between 2 ranges
			want:     Ranges{Range{Start: 5, End: 20}},
			have:     Ranges{Range{Start: 1, End: 9}},
			expected: Ranges{Range{Start: 10, End: 20}},
			cl:       21,
		},
		{
			// case 12 full range miss
			want:     Ranges{Range{Start: 15, End: 20}},
			have:     Ranges{Range{Start: 1, End: 9}},
			expected: Ranges{Range{Start: 15, End: 20}},
			cl:       21,
		},
		{
			// case 13 cache hit
			want:     Ranges{Range{Start: 29, End: 29}},
			have:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 32}},
			expected: Ranges{},
			cl:       70,
		},
		// case 14 two separate partial hit areas in the same request
		{
			want:     Ranges{Range{Start: 9, End: 22}, Range{Start: 28, End: 60}},
			have:     Ranges{Range{Start: 0, End: 10}, Range{Start: 20, End: 32}},
			expected: Ranges{Range{Start: 11, End: 19}, Range{Start: 33, End: 60}},
			cl:       70,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := test.want.CalculateDelta(test.have, test.cl)
			if !res.Equal(test.expected) {
				t.Errorf("got     : %s\nexpected: %s", res, test.expected)
			}
		})
	}
}

func TestRangesString(t *testing.T) {

	tests := []struct {
		out, expected string
	}{
		{
			out:      Ranges{}.String(),
			expected: "",
		},
		{
			out:      Ranges{Range{Start: 0, End: 50}}.String(),
			expected: "bytes=0-50",
		},
		{
			out:      Ranges{Range{Start: -1, End: 50}}.String(),
			expected: "bytes=-50",
		},
		{
			out:      Ranges{Range{Start: 50, End: -1}}.String(),
			expected: "bytes=50-",
		},
		{
			out:      Ranges{Range{Start: 0, End: 20}, Range{Start: 50, End: -1}}.String(),
			expected: "bytes=0-20, 50-",
		},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			if test.out != test.expected {
				t.Errorf("expected: %s\ngot:      %s", test.out, test.expected)
			}
		})
	}

}

func TestParseContentRangeHeader(t *testing.T) {
	er := Range{Start: 0, End: 20}
	el := int64(100)
	r, cl, err := ParseContentRangeHeader("bytes 0-20/100")
	if err != nil {
		t.Error(err)
	}
	if er != r {
		t.Errorf("expected %s, got %s", er.String(), r.String())
	}
	if cl != el {
		t.Errorf("expected %d, got %d", el, cl)
	}

	// trickster does not support caching raanges with  * content lengths
	er = Range{Start: 0, End: 20}
	r, _, err = ParseContentRangeHeader("bytes 0-20/*")
	if err == nil || err.Error() != "invalid input format" {
		t.Errorf("expected error: %s", "invalid input format")
	}

	er = Range{}
	el = -1
	r, cl, err = ParseContentRangeHeader("bytes a-20/*")
	if err == nil || err.Error() != "invalid input format" {
		t.Errorf("expected error: %s", "invalid input format")
	}
	if er != r {
		t.Errorf("expected %s, got %s", er.String(), r.String())
	}
	if cl != el {
		t.Errorf("expected %d, got %d", el, cl)
	}
}

func TestRangesFilter(t *testing.T) {
	rs := Ranges{
		Range{1, 2},
		Range{4, 5},
	}
	s := []byte{0, 1, 2, 3, 4}
	cmp := func(bs0, bs1 []byte) error {
		if len(bs0) != len(bs1) {
			return fmt.Errorf("slice lengths %d and %d not eq", len(bs0), len(bs1))
		}
		for i := 0; i < len(bs0); i++ {
			if bs0[i] != bs1[i] {
				return fmt.Errorf("slices not eq at %d, got %b and %b", i, bs0[i], bs1[i])
			}
		}
		return nil
	}
	if err := cmp(rs.FilterByteSlice(s), []byte{0, 1, 2, 0, 4}); err != nil {
		t.Error(err)
	}
}

func TestRangesEqual(t *testing.T) {

	want := Ranges{Range{Start: 0, End: 20}}
	if want.Equal(nil) {
		t.Errorf("expected %t got %t", false, true)
	}

}

func TestRangeSort(t *testing.T) {
	r := Ranges{Range{Start: 10, End: 20}, Range{Start: 0, End: 8}}
	sort.Sort(r)
	if r[0].Start != 0 || r[1].End != 20 {
		t.Errorf("sort failed on %s", r.String())
	}
}

func TestRangeLess(t *testing.T) {
	r1 := Range{Start: 10, End: 20}
	r2 := Range{Start: 22, End: 30}
	if !r1.Less(r2) {
		t.Errorf("expected %t got %t", true, r1.Less(r2))
	}
}

func TestContentRangeHeader(t *testing.T) {

	const expected = "bytes 0-20/100"

	r := Range{Start: 0, End: 20}
	h := r.ContentRangeHeader(100)

	if h != expected {
		t.Errorf("expected %s got %s", expected, h)
	}

}

func TestParseRangeHeader_EmptyString(t *testing.T) {
	r := ParseRangeHeader("")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
}

func TestParseRangeHeader_InvalidRange(t *testing.T) {
	r := ParseRangeHeader("bytes=abc-def")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
	r = ParseRangeHeader("bytes0-100")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
	r = ParseRangeHeader("0-100")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
	r = ParseRangeHeader("100")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
	r = ParseRangeHeader("-")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
	r = ParseRangeHeader("bytes=20-30-40-50")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
	r = ParseRangeHeader("bytes=20-blah")
	if r != nil {
		t.Errorf("expected empty byte range")
	}
}

func TestParseRangeHeader_SingleRange(t *testing.T) {
	byteRange := "bytes=0-50"
	res := ParseRangeHeader(byteRange)
	if res == nil {
		t.Errorf("expected a non empty byte range, but got an empty range")
	}
	if res[0].Start != 0 || res[0].End != 50 {
		t.Errorf("expected start %d end %d, got start %d end %d", 0, 50, res[0].Start, res[0].End)
	}
}

func TestParseRangeHeader_Ends(t *testing.T) {
	byteRange := "bytes=500-"
	res := ParseRangeHeader(byteRange)
	if res == nil {
		t.Errorf("expected a non empty byte range, but got an empty range")
	}
	if res[0].Start != 500 || res[0].End != -1 {
		t.Errorf("expected start %d end %d, got start %d end %d", 500, -1, res[0].Start, res[0].End)
	}

	byteRange = "bytes=10-20, 500-"
	res = ParseRangeHeader(byteRange)
	if res == nil {
		t.Errorf("expected a non empty byte range, but got an empty range")
	}
	if res[0].Start != 10 || res[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, res[0].Start, res[0].End)
	}
	if res[1].Start != 500 || res[1].End != -1 {
		t.Errorf("expected start %d end %d, got start %d end %d", -1, 500, res[0].Start, res[0].End)
	}

	byteRange = "bytes=-500"
	res = ParseRangeHeader(byteRange)
	if res == nil {
		t.Errorf("expected a non empty byte range, but got an empty range")
	}
	if res[0].Start != -1 || res[0].End != 500 {
		t.Errorf("expected start %d end %d, got start %d end %d", 500, -1, res[0].Start, res[0].End)
	}

}

func TestParseRangeHeader_MultiRange(t *testing.T) {
	byteRange := "bytes=0-50, 100-150"
	res := ParseRangeHeader(byteRange)
	if res == nil {
		t.Errorf("expected a non empty byte range, but got an empty range")
	}
	if res[0].Start != 0 || res[0].End != 50 {
		t.Errorf("expected start %d end %d, got start %d end %d", 0, 50, res[0].Start, res[0].End)
	}
	if res[1].Start != 100 || res[1].End != 150 {
		t.Errorf("expected start %d end %d, got start %d end %d", 100, 150, res[1].Start, res[1].End)
	}
}

func TestRangeCrop(t *testing.T) {
	r1 := Range{0, 1}
	r2 := Range{0, 3}
	b := []byte{0, 1}
	if cr, _ := r1.CropByteSlice(b); len(cr) != 2 || cr[0] != 0 || cr[1] != 1 {
		t.Error(cr)
	}
	if cr, _ := r2.CropByteSlice(b); len(cr) != 2 || cr[0] != 0 || cr[1] != 1 {
		t.Error(cr)
	}
}
