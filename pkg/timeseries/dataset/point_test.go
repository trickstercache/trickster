/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"testing"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
	"github.com/tricksterproxy/trickster/pkg/timeseries/epoch"
)

func testPoints() Points {

	return Points{
		Point{
			Epoch:  epoch.Epoch(5 * timeseries.Second),
			Size:   27,
			Values: []interface{}{1},
		},
		Point{
			Epoch:  epoch.Epoch(10 * timeseries.Second),
			Size:   27,
			Values: []interface{}{1},
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
