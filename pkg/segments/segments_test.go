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

package segments

import (
	"testing"
	"time"
)

type timeSegment struct {
	start, end time.Time
}

func (s timeSegment) StartVal() time.Time { return s.start }
func (s timeSegment) EndVal() time.Time   { return s.end }
func (s timeSegment) NewSegment(start, end time.Time) Segment[time.Time] {
	return timeSegment{start: start, end: end}
}
func (s timeSegment) String() string { return s.start.String() + "-" + s.end.String() }

type intSegment struct {
	start, end int64
}

func (s intSegment) StartVal() int64 { return s.start }
func (s intSegment) EndVal() int64   { return s.end }
func (s intSegment) NewSegment(start, end int64) Segment[int64] {
	return intSegment{start: start, end: end}
}
func (s intSegment) String() string { return "" }

func TestTimeDiffabble(t *testing.T) {
	d := Time{}
	start := time.Unix(10, 0)
	step := time.Second

	if got := d.Add(start, step); !got.Equal(time.Unix(11, 0)) {
		t.Errorf("Add() = %v; want %v", got, time.Unix(11, 0))
	}
	if !d.Less(start, time.Unix(11, 0)) || d.Less(time.Unix(11, 0), start) {
		t.Error("expected Less() ordering")
	}
	if !d.Equal(start, start) || d.Equal(start, time.Unix(11, 0)) {
		t.Error("expected Equal() behavior")
	}
	if !d.Zero().IsZero() {
		t.Error("expected Zero() to return zero time")
	}
	if d.Neg(step) != -time.Second {
		t.Errorf("Neg() = %v; want %v", d.Neg(step), -time.Second)
	}
}

func TestInt64Diffabble(t *testing.T) {
	d := Int64{}
	if d.Add(10, 2) != 12 {
		t.Errorf("Add() = %d; want 12", d.Add(10, 2))
	}
	if !d.Less(10, 12) || d.Less(12, 10) {
		t.Error("expected Less() ordering")
	}
	if !d.Equal(10, 10) || d.Equal(10, 12) {
		t.Error("expected Equal() behavior")
	}
	if d.Zero() != 0 {
		t.Errorf("Zero() = %d; want 0", d.Zero())
	}
	if d.Neg(3) != -3 {
		t.Errorf("Neg() = %d; want -3", d.Neg(3))
	}
}

func TestDiffTime(t *testing.T) {
	step := time.Second
	ts := func(sec int64) time.Time { return time.Unix(sec, 0) }
	seg := func(start, end int64) timeSegment {
		return timeSegment{start: ts(start), end: ts(end)}
	}

	tests := []struct {
		name     string
		haves    []timeSegment
		needs    []timeSegment
		step     time.Duration
		expected []timeSegment
	}{
		{
			name:     "empty haves returns needs",
			haves:    nil,
			needs:    []timeSegment{seg(1, 100)},
			step:     step,
			expected: []timeSegment{seg(1, 100)},
		},
		{
			name:     "empty needs returns nil",
			haves:    []timeSegment{seg(1, 100)},
			needs:    nil,
			step:     step,
			expected: nil,
		},
		{
			name:     "zero step returns nil",
			haves:    []timeSegment{seg(1, 100)},
			needs:    []timeSegment{seg(1, 100)},
			step:     0,
			expected: nil,
		},
		{
			name:     "partial have needs prefix",
			haves:    []timeSegment{seg(50, 100)},
			needs:    []timeSegment{seg(1, 100)},
			step:     step,
			expected: []timeSegment{seg(1, 49)},
		},
		{
			name:     "multiple haves with gap",
			haves:    []timeSegment{seg(1, 40), seg(60, 100)},
			needs:    []timeSegment{seg(1, 100)},
			step:     step,
			expected: []timeSegment{seg(41, 59)},
		},
		{
			name:     "have completely covers need",
			haves:    []timeSegment{seg(1, 200)},
			needs:    []timeSegment{seg(50, 100)},
			step:     step,
			expected: nil,
		},
		{
			name:     "need entirely before all haves",
			haves:    []timeSegment{seg(200, 300)},
			needs:    []timeSegment{seg(1, 100)},
			step:     step,
			expected: []timeSegment{seg(1, 100)},
		},
		{
			name:     "need entirely after all haves",
			haves:    []timeSegment{seg(1, 50)},
			needs:    []timeSegment{seg(100, 200)},
			step:     step,
			expected: []timeSegment{seg(100, 200)},
		},
		{
			name:     "invalid need is skipped",
			haves:    []timeSegment{seg(1, 100)},
			needs:    []timeSegment{seg(100, 50)},
			step:     step,
			expected: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Diff(test.haves, test.needs, test.step, Time{})
			if !timeSegmentsEqual(got, test.expected) {
				t.Errorf("expected %v got %v", test.expected, got)
			}
		})
	}
}

func TestDiffInt64(t *testing.T) {
	step := int64(1)
	seg := func(start, end int64) intSegment {
		return intSegment{start: start, end: end}
	}

	tests := []struct {
		name     string
		haves    []intSegment
		needs    []intSegment
		step     int64
		expected []intSegment
	}{
		{
			name:     "empty haves returns needs",
			haves:    nil,
			needs:    []intSegment{seg(0, 10), seg(20, 29)},
			step:     step,
			expected: []intSegment{seg(0, 10), seg(20, 29)},
		},
		{
			name:     "empty needs returns nil",
			haves:    []intSegment{seg(0, 10)},
			needs:    nil,
			step:     step,
			expected: nil,
		},
		{
			name:     "zero step returns nil",
			haves:    []intSegment{seg(0, 10)},
			needs:    []intSegment{seg(0, 10)},
			step:     0,
			expected: nil,
		},
		{
			name:     "partial hit returns suffix",
			haves:    []intSegment{seg(1, 9)},
			needs:    []intSegment{seg(5, 20)},
			step:     step,
			expected: []intSegment{seg(10, 20)},
		},
		{
			name:     "full cache hit",
			haves:    []intSegment{seg(0, 10), seg(20, 32)},
			needs:    []intSegment{seg(29, 29)},
			step:     step,
			expected: nil,
		},
		{
			name: "two partial hit areas",
			haves: []intSegment{
				seg(0, 10),
				seg(20, 32),
			},
			needs: []intSegment{
				seg(9, 22),
				seg(28, 60),
			},
			step: step,
			expected: []intSegment{
				seg(11, 19),
				seg(33, 60),
			},
		},
		{
			name:     "need entirely before all haves",
			haves:    []intSegment{seg(200, 300)},
			needs:    []intSegment{seg(1, 100)},
			step:     step,
			expected: []intSegment{seg(1, 100)},
		},
		{
			name:     "need entirely after all haves",
			haves:    []intSegment{seg(1, 50)},
			needs:    []intSegment{seg(100, 200)},
			step:     step,
			expected: []intSegment{seg(100, 200)},
		},
		{
			name:     "prefix and suffix gaps",
			haves:    []intSegment{seg(6, 9)},
			needs:    []intSegment{seg(5, 10)},
			step:     step,
			expected: []intSegment{seg(5, 5), seg(10, 10)},
		},
		{
			name:     "invalid need is skipped",
			haves:    []intSegment{seg(1, 10)},
			needs:    []intSegment{seg(20, 10)},
			step:     step,
			expected: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Diff(test.haves, test.needs, test.step, Int64{})
			if !intSegmentsEqual(got, test.expected) {
				t.Errorf("expected %v got %v", test.expected, got)
			}
		})
	}
}

func timeSegmentsEqual(a, b []timeSegment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].start.Equal(b[i].start) || !a[i].end.Equal(b[i].end) {
			return false
		}
	}
	return true
}

func intSegmentsEqual(a, b []intSegment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].start != b[i].start || a[i].end != b[i].end {
			return false
		}
	}
	return true
}
