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

package dataset

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/stretchr/testify/require"
)

func testPoints() Points {
	return Points{
		Point{
			Epoch:  epoch.Epoch(5 * timeseries.Second),
			Values: []any{int64(1), int64(37)},
		},
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Values: []any{int64(1), int64(24)},
		},
	}
}

func testPoints2() Points {
	return Points{
		Point{
			Epoch:  epoch.Epoch(5 * timeseries.Second),
			Values: []any{int64(1), int64(37)},
		},
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Values: []any{int64(1), int64(25)},
		},
		Point{
			Epoch:  epoch.Epoch(15 * timeseries.Second),
			Values: []any{int64(1), int64(34)},
		},
	}
}

func testPoints3() Points {
	return Points{
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Values: []any{int64(1), int64(24)},
		},
	}
}

func genTestPoints(baseEpoch, n int) Points {
	points := make(Points, n)
	for i := range n {
		points[i] = Point{
			Epoch:  epoch.Epoch((i * 10 * timeseries.Second) + baseEpoch),
			Values: []any{1, 24 + (i * 5)},
		}
	}
	return points
}

func TestPointEqual(t *testing.T) {
	p := testPoints()
	p1 := p[0]
	p2 := p[1]
	b := PointsAreEqual(p1, p2)
	if b {
		t.Error("expected false")
	}
	p2.Epoch = p1.Epoch
	b = PointsAreEqual(p1, p2)
	if b {
		t.Error("expected false")
	}
	p2.Values = []any{int64(1), int64(37)}
	p2.Epoch = p1.Epoch
	b = PointsAreEqual(p1, p2)
	if !b {
		t.Error("expected true")
	}
}

func BenchmarkPointsAreEqual(b *testing.B) {
	p1 := testPoints()[0]
	p2 := testPoints()[1]
	for b.Loop() {
		PointsAreEqual(p1, p2)
	}
}

func TestPointClone(t *testing.T) {
	p := &Point{
		Epoch:  epoch.Epoch(1),
		Values: []any{1},
	}
	p2 := p.Clone()
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] {
		t.Error("clone mismatch")
	}
}

func BenchmarkPointClone(b *testing.B) {
	p := &Point{
		Epoch:  epoch.Epoch(1),
		Values: []any{1},
	}
	for b.Loop() {
		p.Clone()
	}
}

func TestPointsClone(t *testing.T) {
	pts := testPoints()
	pts2 := pts.Clone()

	if len(pts) != len(pts2) {
		t.Error("clone mismatch")
	}

	p := pts[0]
	p2 := pts2[0]
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] {
		t.Error("clone mismatch")
	}

	p = pts[1]
	p2 = pts2[1]
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] {
		t.Error("clone mismatch")
	}

	if len(pts2) != 2 {
		t.Error("clone mismatch")
	}
}

func TestFindRange(t *testing.T) {
	pts := Points{
		Point{Epoch: epoch.Epoch(1 * time.Second), Values: []any{int64(1)}},
		Point{Epoch: epoch.Epoch(3 * time.Second), Values: []any{int64(2)}},
		Point{Epoch: epoch.Epoch(5 * time.Second), Values: []any{int64(3)}},
		Point{Epoch: epoch.Epoch(7 * time.Second), Values: []any{int64(4)}},
		Point{Epoch: epoch.Epoch(9 * time.Second), Values: []any{int64(5)}},
	}

	tests := []struct {
		name       string
		startEpoch epoch.Epoch
		endEpoch   epoch.Epoch
		wantStart  int
		wantEnd    int
	}{
		{
			name:       "exact match both ends",
			startEpoch: epoch.Epoch(3 * time.Second),
			endEpoch:   epoch.Epoch(7 * time.Second),
			wantStart:  1,
			wantEnd:    4,
		},
		{
			name:       "start before data, end in middle",
			startEpoch: epoch.Epoch(0),
			endEpoch:   epoch.Epoch(5 * time.Second),
			wantStart:  0,
			wantEnd:    3,
		},
		{
			name:       "start in middle, end after data",
			startEpoch: epoch.Epoch(6 * time.Second),
			endEpoch:   epoch.Epoch(15 * time.Second),
			wantStart:  3,
			wantEnd:    5,
		},
		{
			name:       "range entirely before data",
			startEpoch: epoch.Epoch(-5 * time.Second),
			endEpoch:   epoch.Epoch(-1 * time.Second),
			wantStart:  0,
			wantEnd:    0,
		},
		{
			name:       "range entirely after data",
			startEpoch: epoch.Epoch(15 * time.Second),
			endEpoch:   epoch.Epoch(20 * time.Second),
			wantStart:  5,
			wantEnd:    5,
		},
		{
			name:       "single point range",
			startEpoch: epoch.Epoch(5 * time.Second),
			endEpoch:   epoch.Epoch(5 * time.Second),
			wantStart:  2,
			wantEnd:    3,
		},
		{
			name:       "gap in data - start in gap",
			startEpoch: epoch.Epoch(4 * time.Second),
			endEpoch:   epoch.Epoch(6 * time.Second),
			wantStart:  2,
			wantEnd:    3,
		},
	}
	segs := segmentsFromPoints(pts)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewSeriesOf(SeriesHeader{}, segs.View(tt.startEpoch, tt.endEpoch)).Points()
			if tt.wantStart == tt.wantEnd {
				require.Empty(t, got)
				return
			}
			require.Equal(t, pts[tt.wantStart:tt.wantEnd], got)
		})
	}

	t.Run("empty points", func(t *testing.T) {
		require.Zero(t, Segments(nil).View(epoch.Epoch(1*time.Second), epoch.Epoch(5*time.Second)).Len())
	})
}

func TestMergePoints(t *testing.T) {
	tests := []struct {
		name             string
		p1, p2, expected Points
		sortPoints       bool
	}{
		{
			name: "p1 shorter, p2 has overlap and extension",
			p1:   testPoints(),
			p2:   testPoints2(),
			expected: Points{
				{Epoch: epoch.Epoch(5 * timeseries.Second), Values: []any{int64(1), int64(37)}},
				{Epoch: epoch.Epoch(10 * timeseries.Second), Values: []any{int64(1), int64(25)}},
				{Epoch: epoch.Epoch(15 * timeseries.Second), Values: []any{int64(1), int64(34)}},
			},
			sortPoints: true,
		},
		{
			name: "p2 shorter, p1 has overlap and extension",
			p1:   testPoints2(),
			p2:   testPoints(),
			expected: Points{
				{Epoch: epoch.Epoch(5 * timeseries.Second), Values: []any{int64(1), int64(37)}},
				{Epoch: epoch.Epoch(10 * timeseries.Second), Values: []any{int64(1), int64(24)}},
				{Epoch: epoch.Epoch(15 * timeseries.Second), Values: []any{int64(1), int64(34)}},
			},
			sortPoints: true,
		},
		{
			name:       "p1 non-nil, p2 nil",
			p1:         testPoints(),
			p2:         nil,
			expected:   testPoints(),
			sortPoints: true,
		},
		{
			name:       "p1 nil, p2 non-nil",
			p1:         nil,
			p2:         testPoints(),
			expected:   testPoints(),
			sortPoints: true,
		},
		{
			name:       "p1 subset of p2",
			p1:         testPoints3(),
			p2:         testPoints2(),
			expected:   testPoints2(),
			sortPoints: true,
		},
		{
			name: "p2 subset of p1",
			p1:   testPoints2(),
			p2:   testPoints3(),
			expected: Points{
				{Epoch: epoch.Epoch(5 * timeseries.Second), Values: []any{int64(1), int64(37)}},
				{Epoch: epoch.Epoch(10 * timeseries.Second), Values: []any{int64(1), int64(24)}},
				{Epoch: epoch.Epoch(15 * timeseries.Second), Values: []any{int64(1), int64(34)}},
			},
			sortPoints: true,
		},
		{
			name:       "both nil",
			p1:         nil,
			p2:         nil,
			expected:   nil,
			sortPoints: true,
		},
		{
			name:       "both empty",
			p1:         Points{},
			p2:         Points{},
			expected:   Points{},
			sortPoints: true,
		},
		{
			name: "no sort — concatenated order preserved",
			p1: Points{
				{Epoch: epoch.Epoch(10 * timeseries.Second), Values: []any{1}},
			},
			p2: Points{
				{Epoch: epoch.Epoch(5 * timeseries.Second), Values: []any{2}},
			},
			// without sorting, p2 comes after p1 in concatenation order
			expected: Points{
				{Epoch: epoch.Epoch(10 * timeseries.Second), Values: []any{int64(1)}},
				{Epoch: epoch.Epoch(5 * timeseries.Second), Values: []any{int64(2)}},
			},
			sortPoints: false,
		},
		{
			name: "identical points deduped",
			p1:   testPoints(),
			p2:   testPoints(),
			expected: Points{
				{Epoch: epoch.Epoch(5 * timeseries.Second), Values: []any{int64(1), int64(37)}},
				{Epoch: epoch.Epoch(10 * timeseries.Second), Values: []any{int64(1), int64(24)}},
			},
			sortPoints: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := mergePoints(test.p1, test.p2, MergeOpts{SortPoints: test.sortPoints})
			if !out.Equal(test.expected) {
				t.Errorf("expected:\n%v\ngot:\n%v\n", test.expected, out)
			}
		})
	}
}

func TestPointsCloneOwnsValues(t *testing.T) {
	p := Points{
		{Epoch: 1, Values: []any{"a", 1.5}},
		{Epoch: 2, Values: nil},
		{Epoch: 3, Values: []any{"b", 2.5}},
	}
	clone := p.Clone()
	require.True(t, clone.Equal(p))
	require.Nil(t, clone[1].Values)
	clone[2].Values[0] = "changed"
	require.Equal(t, "b", p[2].Values[0], "the clone shares values with its source")
	require.Nil(t, Points(nil).Clone())
}
