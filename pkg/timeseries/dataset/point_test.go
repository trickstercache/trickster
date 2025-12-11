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
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func testPoints() Points {
	return Points{
		Point{
			Epoch:  epoch.Epoch(5 * timeseries.Second),
			Size:   27,
			Values: []any{1, 37},
		},
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Size:   27,
			Values: []any{1, 24},
		},
	}
}

func testPoints2() Points {
	return Points{
		Point{
			Epoch:  epoch.Epoch(5 * timeseries.Second),
			Size:   27,
			Values: []any{1, 37},
		},
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Size:   27,
			Values: []any{1, 25},
		},
		Point{
			Epoch:  epoch.Epoch(15 * timeseries.Second),
			Size:   27,
			Values: []any{1, 34},
		},
	}
}

func testPoints3() Points {
	return Points{
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Size:   27,
			Values: []any{1, 24},
		},
	}
}

func genTestPoints(baseEpoch, n int) Points {
	points := make(Points, n)
	for i := 0; i < n; i++ {
		points[i] = Point{
			Epoch:  epoch.Epoch((i * 10 * timeseries.Second) + baseEpoch),
			Size:   27,
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
	p2.Values = []any{1, 37}
	p2.Epoch = p1.Epoch
	b = PointsAreEqual(p1, p2)
	if !b {
		t.Error("expected true")
	}
}

func BenchmarkPointsAreEqual(b *testing.B) {
	p1 := testPoints()[0]
	p2 := testPoints()[1]
	for i := 0; i < b.N; i++ {
		PointsAreEqual(p1, p2)
	}
}

func TestPointClone(t *testing.T) {
	p := &Point{
		Epoch:  epoch.Epoch(1),
		Size:   27,
		Values: []any{1},
	}
	p2 := p.Clone()
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] || p2.Size != p.Size {
		t.Error("clone mismatch")
	}
}

func BenchmarkPointClone(b *testing.B) {
	p := &Point{
		Epoch:  epoch.Epoch(1),
		Size:   27,
		Values: []any{1},
	}
	for i := 0; i < b.N; i++ {
		p.Clone()
	}
}

func TestPointsCloneRange(t *testing.T) {
	tests := []struct {
		start, end, expLen, epoch int
	}{
		{0, 1, 1, 5 * timeseries.Second},
		{0, 2, 2, 5 * timeseries.Second},
		{1, 1, 0, 0},
		{1, 2, 1, 10 * timeseries.Second},
		{2, 1, 0, 0},
		{0, 3, 0, 0},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			pts := testPoints().CloneRange(test.start, test.end)
			if len(pts) != test.expLen {
				t.Errorf("expected %d got %d", test.expLen, len(pts))
			}
			if len(pts) > 0 {
				if pts[0].Epoch != epoch.Epoch(test.epoch) {
					t.Errorf("expected %d got %d", test.epoch, pts[0].Epoch)
				}
			}
		})
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
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] || p2.Size != p.Size {
		t.Error("clone mismatch")
	}

	p = pts[1]
	p2 = pts2[1]
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] || p2.Size != p.Size {
		t.Error("clone mismatch")
	}

	if len(pts2) != 2 {
		t.Error("clone mismatch")
	}
}

func TestPointsSize(t *testing.T) {
	pts := testPoints()
	size := pts.Size()
	require.Equal(t, int64(70), size)
}

func BenchmarkPointsSize(b *testing.B) {
	for i := range b.N {
		b.StopTimer()
		pts := genTestPoints(i, 1000)
		b.StartTimer()
		pts.Size()
	}
}

func TestPointsSort(t *testing.T) {
	pts := testPoints()
	pts[0].Epoch = 100 * timeseries.Second
	sort.Sort(pts)
	p := pts[0]
	if p.Epoch != 10*timeseries.Second {
		t.Error("sort mismatch")
	}
}

func TestFindRange(t *testing.T) {
	pts := Points{
		Point{Epoch: epoch.Epoch(1 * time.Second), Size: 1, Values: []any{1}},
		Point{Epoch: epoch.Epoch(3 * time.Second), Size: 1, Values: []any{2}},
		Point{Epoch: epoch.Epoch(5 * time.Second), Size: 1, Values: []any{3}},
		Point{Epoch: epoch.Epoch(7 * time.Second), Size: 1, Values: []any{4}},
		Point{Epoch: epoch.Epoch(9 * time.Second), Size: 1, Values: []any{5}},
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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd := pts.findRange(tt.startEpoch, tt.endEpoch, 0, len(pts)-1)
			require.Equal(t, tt.wantStart, gotStart, "start value not expected")
			require.Equal(t, tt.wantEnd, gotEnd, "end value not expected")
			require.LessOrEqual(t, gotStart, gotEnd)
			require.False(t, gotStart < 0 || gotStart > len(pts), "start index out of bounds")
			require.False(t, gotEnd < 0 || gotEnd > len(pts), "end index out of bounds")
		})
	}

	t.Run("empty points", func(t *testing.T) {
		start, end := Points{}.findRange(epoch.Epoch(1*time.Second), epoch.Epoch(5*time.Second), 0, -1)
		require.False(t, start != 0 || end != 0, "should return 0,0")
	})
}

func BenchmarkFindRange(b *testing.B) {
	pts := genTestPoints(0, 10000) // Create a large dataset for meaningful benchmarks
	startEpoch := epoch.Epoch(2500 * time.Second)
	endEpoch := epoch.Epoch(7500 * time.Second)
	for i := 0; i < b.N; i++ {
		_, _ = pts.findRange(startEpoch, endEpoch, 0, len(pts)-1)
	}
}

func TestMergePoints(t *testing.T) {
	tests := []struct {
		p1, p2, expected Points
	}{
		{
			p1: testPoints(),
			p2: testPoints2(),
			expected: Points{
				Point{
					Epoch:  epoch.Epoch(5 * timeseries.Second),
					Size:   27,
					Values: []any{1, 37},
				},
				Point{
					Epoch:  epoch.Epoch(10 * timeseries.Second),
					Size:   27,
					Values: []any{1, 25},
				},
				Point{
					Epoch:  epoch.Epoch(15 * timeseries.Second),
					Size:   27,
					Values: []any{1, 34},
				},
			},
		},
		{
			p1: testPoints2(),
			p2: testPoints(),
			expected: Points{
				Point{
					Epoch:  epoch.Epoch(5 * timeseries.Second),
					Size:   27,
					Values: []any{1, 37},
				},
				Point{
					Epoch:  epoch.Epoch(10 * timeseries.Second),
					Size:   27,
					Values: []any{1, 24},
				},
				Point{
					Epoch:  epoch.Epoch(15 * timeseries.Second),
					Size:   27,
					Values: []any{1, 34},
				},
			},
		},
		{
			p1:       testPoints(),
			p2:       nil,
			expected: testPoints(),
		},
		{
			p1:       nil,
			p2:       testPoints(),
			expected: testPoints(),
		},
		{
			p1:       testPoints3(),
			p2:       testPoints2(),
			expected: testPoints2(),
		},
		{
			p1: testPoints2(),
			p2: testPoints3(),
			expected: Points{
				Point{
					Epoch:  epoch.Epoch(5 * timeseries.Second),
					Size:   27,
					Values: []any{1, 37},
				},
				Point{
					Epoch:  epoch.Epoch(10 * timeseries.Second),
					Size:   27,
					Values: []any{1, 24},
				},
				Point{
					Epoch:  epoch.Epoch(15 * timeseries.Second),
					Size:   27,
					Values: []any{1, 34},
				},
			},
		},
	}
	for i, test := range tests {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			out := MergePoints(test.p1, test.p2, true)
			if !out.Equal(test.expected) {
				t.Errorf("expected:\n%v\ngot:\n%v\n", test.expected, out)
			}
		})
	}
}
