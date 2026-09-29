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

package timeseries

import (
	"errors"
	"testing"
	"time"
)

func TestParseStepAlignment(t *testing.T) {
	tests := []struct {
		name string
		want StepAlignment
	}{
		{"", 0},
		{"  ", 0},
		{"off", StepAlignmentOff},
		{"truncate", StepAlignmentTruncate},
		{"drop", StepAlignmentDrop},
		{"partial", StepAlignmentPartial},
		{"partial_start", StepAlignmentPartialStart},
		{" Partial_End ", StepAlignmentPartialEnd},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseStepAlignment(test.name)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("got %v want %v", got, test.want)
			}
		})
	}
	for _, name := range []string{"exact", "partial-end", "leaves"} {
		if _, err := ParseStepAlignment(name); !errors.Is(err, ErrInvalidStepAlignment) {
			t.Errorf("%q: expected ErrInvalidStepAlignment, got %v", name, err)
		}
	}
}

func TestStepAlignmentNamesRoundTrip(t *testing.T) {
	for i := range stepAlignmentModeCount {
		mode := StepAlignment(1 << i)
		if !mode.IsMode() {
			t.Fatalf("%d is not a mode", mode)
		}
		b, err := mode.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var got StepAlignment
		if err := got.UnmarshalText(b); err != nil {
			t.Fatal(err)
		}
		if got != mode {
			t.Errorf("round trip of %s gave %s", mode, got)
		}
	}
	var sa StepAlignment
	if err := sa.UnmarshalText([]byte("bogus")); err == nil {
		t.Error("expected an error for an unknown name")
	}
}

func TestStepAlignmentString(t *testing.T) {
	if got := (StepAlignmentOff | StepAlignmentDrop | StepAlignmentPartialEnd).String(); got != "off, drop, partial_end" {
		t.Errorf("set: got %q", got)
	}
	if got := StepAlignment(0).String(); got != "" {
		t.Errorf("zero: got %q", got)
	}
	for _, sa := range []StepAlignment{0, StepAlignmentOff | StepAlignmentDrop, 1 << stepAlignmentModeCount} {
		if sa.IsMode() {
			t.Errorf("%d should not be a mode", sa)
		}
	}
}

func TestStepAlignmentEdges(t *testing.T) {
	tests := []struct {
		mode       StepAlignment
		start, end EdgePolicy
	}{
		{0, EdgeNone, EdgeNone},
		{StepAlignmentOff, EdgeNone, EdgeNone},
		{StepAlignmentTruncate, EdgeTruncate, EdgeTruncate},
		{StepAlignmentDrop, EdgeDrop, EdgeDrop},
		{StepAlignmentPartial, EdgePartial, EdgePartial},
		{StepAlignmentPartialStart, EdgePartial, EdgeTruncate},
		{StepAlignmentPartialEnd, EdgeTruncate, EdgePartial},
	}
	for _, test := range tests {
		start, end := test.mode.Edges()
		if start != test.start || end != test.end {
			t.Errorf("%s: got (%d, %d) want (%d, %d)", test.mode, start, end, test.start, test.end)
		}
		if start == EdgePartial || end == EdgePartial {
			if test.mode&StepAlignmentPartialModes == 0 {
				t.Errorf("%s fetches partial buckets but is not in StepAlignmentPartialModes", test.mode)
			}
		} else if test.mode&StepAlignmentPartialModes != 0 {
			t.Errorf("%s is in StepAlignmentPartialModes but fetches no partial buckets", test.mode)
		}
	}
}

func TestBucketEdgeString(t *testing.T) {
	if BucketEdgeStart.String() != "start" || BucketEdgeEnd.String() != "end" {
		t.Errorf("got %q and %q", BucketEdgeStart, BucketEdgeEnd)
	}
}

func TestResolveStepAlignment(t *testing.T) {
	const supported = StepAlignmentTruncate | StepAlignmentPartialEnd | StepAlignmentOff
	tests := []struct {
		name                 string
		override, configured StepAlignment
		want, unsupported    StepAlignment
	}{
		{"nothing requested keeps the default", 0, 0, StepAlignmentPartialEnd, 0},
		{"configured mode applies", 0, StepAlignmentTruncate, StepAlignmentTruncate, 0},
		{"override wins over configured", StepAlignmentOff, StepAlignmentTruncate, StepAlignmentOff, 0},
		{
			"unsupported override keeps the default", StepAlignmentDrop, StepAlignmentTruncate,
			StepAlignmentPartialEnd, StepAlignmentDrop,
		},
		{
			"unsupported configured keeps the default", 0, StepAlignmentPartial,
			StepAlignmentPartialEnd, StepAlignmentPartial,
		},
		{
			"a set is never a mode", 0, StepAlignmentTruncate | StepAlignmentOff,
			StepAlignmentPartialEnd, StepAlignmentTruncate | StepAlignmentOff,
		},
		{"requesting the default is not a fallback", 0, StepAlignmentPartialEnd, StepAlignmentPartialEnd, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq := &TimeRangeQuery{StepAlignments: supported, StepAlignment: StepAlignmentPartialEnd}
			if got := trq.ResolveStepAlignment(test.override, test.configured); got != test.unsupported {
				t.Errorf("unsupported: got %v want %v", got, test.unsupported)
			}
			if trq.StepAlignment != test.want {
				t.Errorf("mode: got %v want %v", trq.StepAlignment, test.want)
			}
		})
	}
}

func TestRequestedExtent(t *testing.T) {
	ext := Extent{Start: time.Unix(60, 0), End: time.Unix(120, 0)}
	trq := &TimeRangeQuery{Extent: ext}
	if got := trq.RequestedExtent(); got != ext {
		t.Errorf("an unrecorded range returns the extent: got %s", got)
	}
	trq.Requested = RequestedRange{Start: time.Unix(45, 0), End: time.Unix(135, 0), EndInclusive: true}
	if got := trq.RequestedExtent(); got != (Extent{Start: time.Unix(45, 0), End: time.Unix(135, 0)}) {
		t.Errorf("recorded range: got %s", got)
	}
}
