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
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func testPoints() Points {

	return Points{
		Point{
			Epoch:  epoch.Epoch(5 * timeseries.Second),
			Size:   27,
			Values: []interface{}{1, 37},
		},
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Size:   27,
			Values: []interface{}{1, 24},
		},
	}
}

func TestPointClone(t *testing.T) {
	p := &Point{
		Epoch:  epoch.Epoch(1),
		Size:   27,
		Values: []interface{}{1},
	}
	p2 := p.Clone()
	if p2.Epoch != p.Epoch || p2.Values[0] != p.Values[0] || p2.Size != p.Size {
		t.Error("clone mismatch")
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

func TestPointsSort(t *testing.T) {
	pts := testPoints()
	pts[0].Epoch = 100 * timeseries.Second
	sort.Sort(pts)
	p := pts[0]
	if p.Epoch != 10*timeseries.Second {
		t.Error("sort mismatch")
	}
}

func TestOnOrJustAfter(t *testing.T) {

	pts := testPoints()
	i := pts.onOrJustAfter(0, 0, len(pts)-1)
	if i != 0 {
		t.Errorf("expected %d got %d", 0, i)
	}

	i = pts.onOrJustAfter(epoch.Epoch(6*time.Second), 0, 0)
	if i != 1 {
		t.Errorf("expected %d got %d", 1, i)
	}

	i = pts.onOrJustAfter(epoch.Epoch(6*time.Second), 0, 1)
	if i != 1 {
		t.Errorf("expected %d got %d", 1, i)
	}

}

func TestOnOrJustBefore(t *testing.T) {

	pts := testPoints()
	i := pts.onOrJustBefore(0, 0, len(pts)-1)
	if i != -1 {
		t.Errorf("expected %d got %d", -1, i)
	}

	i = pts.onOrJustBefore(epoch.Epoch(6*time.Second), 0, 0)
	if i != 0 {
		t.Errorf("expected %d got %d", 0, i)
	}

	i = pts.onOrJustAfter(epoch.Epoch(15*time.Second), 0, 1)
	if i != 2 {
		t.Errorf("expected %d got %d", 2, i)
	}

	i = pts.onOrJustAfter(epoch.Epoch(6*time.Second), 0, 1)
	if i != 1 {
		t.Errorf("expected %d got %d", 1, i)
	}

}
