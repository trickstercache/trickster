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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestFinalizeWeightedAvgUnpairedEpoch(t *testing.T) {
	t.Parallel()
	mkPoint := func(e int64, v string) Point {
		return Point{Epoch: epoch.Epoch(e), Size: 32, Values: []any{v}}
	}
	mkDS := func(name string, pts ...Point) *DataSet {
		return &DataSet{
			Results: Results{
				{StatementID: 0, SeriesList: SeriesList{
					{Header: SeriesHeader{Name: name, Tags: Tags{}}, Points: append(Points{}, pts...)},
				}},
			},
		}
	}

	t.Run("epoch missing from count drops the sum point", func(t *testing.T) {
		sumDS := mkDS("m", mkPoint(100, "10"), mkPoint(200, "20"))
		countDS := mkDS("m", mkPoint(100, "2"))
		sumDS.FinalizeWeightedAvg(countDS, "")

		pts := sumDS.Results[0].SeriesList[0].Points
		if len(pts) != 1 {
			t.Fatalf("expected 1 point after dropping unpaired epoch, got %d (%v)", len(pts), pts)
		}
		if pts[0].Epoch != 100 {
			t.Errorf("expected surviving point at epoch 100, got %d", pts[0].Epoch)
		}
		if pts[0].Values[0] != "5" {
			t.Errorf("epoch 100: got %v, want 5 (10/2)", pts[0].Values[0])
		}
		if len(sumDS.Warnings) == 0 {
			t.Fatal("expected a warning naming the affected series, got none")
		}
		found := false
		for _, w := range sumDS.Warnings {
			if strings.Contains(w, "m") && strings.Contains(strings.ToLower(w), "weighted-avg") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warning should mention series name and weighted-avg; got %v", sumDS.Warnings)
		}
	})

	t.Run("count-only epoch is ignored without panic", func(t *testing.T) {
		sumDS := mkDS("m", mkPoint(100, "10"))
		countDS := mkDS("m", mkPoint(100, "2"), mkPoint(200, "4"))
		sumDS.FinalizeWeightedAvg(countDS, "")

		pts := sumDS.Results[0].SeriesList[0].Points
		if len(pts) != 1 {
			t.Fatalf("expected 1 point, got %d", len(pts))
		}
		if pts[0].Values[0] != "5" {
			t.Errorf("epoch 100: got %v, want 5", pts[0].Values[0])
		}
	})

	t.Run("zero-count epoch is treated as unpaired and dropped", func(t *testing.T) {
		sumDS := mkDS("m", mkPoint(100, "10"), mkPoint(200, "20"))
		countDS := mkDS("m", mkPoint(100, "2"), mkPoint(200, "0"))
		sumDS.FinalizeWeightedAvg(countDS, "")

		pts := sumDS.Results[0].SeriesList[0].Points
		if len(pts) != 1 {
			t.Fatalf("expected 1 point (zero-count dropped), got %d (%v)", len(pts), pts)
		}
		if pts[0].Epoch != 100 {
			t.Errorf("expected epoch 100 surviving, got %d", pts[0].Epoch)
		}
	})
}
