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
	"testing"
	"time"
)

func TestFloorToGrid(t *testing.T) {
	const day = 24 * time.Hour
	// 2026-09-26T13:17:00Z is a Saturday; the Unix epoch began on a Thursday
	sat := time.Date(2026, 9, 26, 13, 17, 0, 0, time.UTC)
	tests := []struct {
		name        string
		value       time.Time
		step, phase time.Duration
		expected    time.Time
	}{
		{"non-positive step returns value", sat, 0, 0, sat},
		{"hourly", sat, time.Hour, 0, time.Date(2026, 9, 26, 13, 0, 0, 0, time.UTC)},
		{
			"hourly with 30m phase", sat, time.Hour, 30 * time.Minute,
			time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC),
		},
		{
			"phase at or beyond step wraps", sat, time.Hour, 90 * time.Minute,
			time.Date(2026, 9, 26, 12, 30, 0, 0, time.UTC),
		},
		{
			"negative phase", sat, time.Hour, -15 * time.Minute,
			time.Date(2026, 9, 26, 12, 45, 0, 0, time.UTC),
		},
		{
			"weekly buckets land on the epoch's Thursday", sat, 7 * day, 0,
			time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
		},
		{
			"steps not dividing the zero-time offset use the Unix epoch", sat, 7 * time.Hour, 0,
			time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC),
		},
		{
			"aligned value is unchanged", time.Unix(3600, 0).UTC(), time.Hour, 0,
			time.Unix(3600, 0).UTC(),
		},
		{
			"negative epoch floors toward the past", time.Unix(-1, 500_000_000).UTC(), time.Second,
			0, time.Unix(-1, 0).UTC(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FloorToGrid(test.value, test.step, test.phase); !got.Equal(test.expected) {
				t.Errorf("expected %s got %s", test.expected, got)
			}
		})
	}
}

func TestCeilToGrid(t *testing.T) {
	step := time.Minute
	aligned := time.Unix(120, 0)
	if got := CeilToGrid(aligned, step, 0); !got.Equal(aligned) {
		t.Errorf("aligned ceil = %v", got)
	}
	if got := CeilToGrid(time.Unix(150, 500), step, 0); !got.Equal(time.Unix(180, 0)) {
		t.Errorf("ceil = %v", got)
	}
	if got := CeilToGrid(time.Unix(100, 0), step, 30*time.Second); !got.Equal(time.Unix(150, 0)) {
		t.Errorf("phased ceil = %v", got)
	}
	if got := CeilToGrid(time.Unix(-61, 500), step, 0); !got.Equal(time.Unix(-60, 0)) {
		t.Errorf("negative ceil = %v", got)
	}
	unaligned := time.Unix(150, 500)
	if got := CeilToGrid(unaligned, 0, 0); !got.Equal(unaligned) {
		t.Errorf("nonpositive step ceil = %v", got)
	}
	loc := time.FixedZone("test", 3600)
	if got := CeilToGrid(unaligned.In(loc), step, 0); got.Location() != loc {
		t.Error("ceil must preserve location")
	}
}

func TestOnGrid(t *testing.T) {
	step := time.Minute
	aligned := time.Unix(120, 0)
	if !OnGrid(aligned, step, 0) || OnGrid(time.Unix(150, 500), step, 0) {
		t.Error("alignment misclassified")
	}
	if OnGrid(aligned, 0, 0) {
		t.Error("nonpositive step must never align")
	}
	if OnGrid(aligned, step, 30*time.Second) {
		t.Error("phase offset ignored")
	}
	if !OnGrid(time.Unix(150, 0), step, 30*time.Second) {
		t.Error("phase-aligned value misclassified")
	}
	if !OnGrid(time.Unix(-120, 0), step, 0) || OnGrid(time.Unix(-61, 0), step, 0) {
		t.Error("negative epoch misclassified")
	}
}

func TestExtentClampToGrid(t *testing.T) {
	step, phase := time.Hour, 30*time.Minute
	at := func(h, m int) time.Time { return time.Date(2024, 1, 1, h, m, 0, 0, time.UTC) }
	tests := []struct {
		name     string
		extent   Extent
		expected Extent
		ok       bool
	}{
		{
			"on grid is unchanged",
			Extent{Start: at(1, 30), End: at(3, 30)},
			Extent{Start: at(1, 30), End: at(3, 30)},
			true,
		},
		{
			"off-grid bounds narrow to whole buckets",
			Extent{Start: at(1, 0), End: at(4, 0)},
			Extent{Start: at(1, 30), End: at(3, 30)},
			true,
		},
		{"no whole bucket inside", Extent{Start: at(1, 40), End: at(2, 20)}, Extent{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := test.extent.ClampToGrid(step, phase)
			if ok != test.ok {
				t.Fatalf("expected ok %t got %t", test.ok, ok)
			}
			if ok && (!got.Start.Equal(test.expected.Start) || !got.End.Equal(test.expected.End)) {
				t.Errorf("expected %s got %s", test.expected, got)
			}
		})
	}
	e := Extent{Start: at(1, 0), End: at(4, 0)}
	if got, ok := e.ClampToGrid(0, 0); !ok || got != e {
		t.Error("a non-positive step must leave the extent unchanged")
	}
}

func TestExtentListClampToGrid(t *testing.T) {
	step := time.Hour
	at := func(h int) time.Time { return time.Date(2024, 1, 1, h, 0, 0, 0, time.UTC) }
	onGrid := ExtentList{{Start: at(1), End: at(2)}, {Start: at(4), End: at(5)}}
	if got, n := onGrid.ClampToGrid(step, 0); n != 0 || &got[0] != &onGrid[0] {
		t.Errorf("an on-grid list must be returned uncopied, got %v (%d)", got, n)
	}
	mixed := ExtentList{
		{Start: at(1), End: at(2)},
		{Start: at(3).Add(time.Minute), End: at(3).Add(2 * time.Minute)},
		{Start: at(4).Add(-time.Minute), End: at(6).Add(time.Minute)},
	}
	got, n := mixed.ClampToGrid(step, 0)
	if n != 2 {
		t.Errorf("expected 2 off-grid extents got %d", n)
	}
	expected := ExtentList{{Start: at(1), End: at(2)}, {Start: at(4), End: at(6)}}
	if len(got) != len(expected) {
		t.Fatalf("expected %v got %v", expected, got)
	}
	for i := range expected {
		if !got[i].Start.Equal(expected[i].Start) || !got[i].End.Equal(expected[i].End) {
			t.Errorf("expected %v got %v", expected, got)
		}
	}
	if got, n := mixed.ClampToGrid(0, 0); n != 0 || len(got) != len(mixed) {
		t.Error("a non-positive step must leave the list unchanged")
	}
}

func TestLastCompleteLabel(t *testing.T) {
	now := time.Date(2024, 1, 1, 12, 20, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2024, 1, 1, h, m, 0, 0, time.UTC) }
	tests := []struct {
		name        string
		model       SampleModel
		step, phase time.Duration
		expected    time.Time
		ok          bool
	}{
		{
			"bucket labels end a step before the live bucket", SampleModelBucket, time.Hour, 0,
			at(11, 0), true,
		},
		{
			"stored buckets are labeled by their start", SampleModelStored, time.Hour, 0,
			at(11, 0), true,
		},
		{
			"stop labels end at the live bucket's start", SampleModelBucketStop, time.Hour, 0,
			at(12, 0), true,
		},
		{"phase shifts the grid", SampleModelBucket, time.Hour, 30 * time.Minute, at(10, 30), true},
		{"instant points have no buckets", SampleModelInstant, time.Hour, 0, time.Time{}, false},
		{"no step has no buckets", SampleModelBucket, 0, 0, time.Time{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq := &TimeRangeQuery{SampleModel: test.model, Step: test.step, Phase: test.phase}
			got, ok := trq.LastCompleteLabel(now)
			if ok != test.ok || !got.Equal(test.expected) {
				t.Errorf("expected %s, %t got %s, %t", test.expected, test.ok, got, ok)
			}
		})
	}
}
