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

package tsm

import (
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestPruneUnpairedWeightedAvgSeries(t *testing.T) {
	t.Run("drops series missing from count side and warns", func(t *testing.T) {
		sumDS := loadGoldenDataSet(t, "weighted_avg/drops_unpaired_sum")
		countDS := loadGoldenDataSet(t, "weighted_avg/drops_unpaired_count")

		pruneUnpairedWeightedAvgSeries(sumDS, countDS, "")
		sumDS.FinalizeWeightedAvg(countDS, "")

		series := sumDS.Results[0].SeriesList
		if len(series) != 1 {
			t.Fatalf("expected 1 paired series after prune, got %d", len(series))
		}
		if got := series[0].Header.Tags["region"]; got != "us-east-1" {
			t.Errorf("expected surviving series region=us-east-1, got %q", got)
		}
		if got := series[0].Points[0].Values[0]; got != "10" {
			t.Errorf("paired series should finalize to 100/10=10, got %v", got)
		}
		if len(sumDS.Warnings) == 0 {
			t.Fatal("expected a warning naming the dropped series")
		}
		var warned bool
		for _, w := range sumDS.Warnings {
			if strings.Contains(w, "weighted-avg") && strings.Contains(w, "rps") {
				warned = true
				break
			}
		}
		if !warned {
			t.Errorf("warning did not mention dropped series; got %v", sumDS.Warnings)
		}
	})

	t.Run("baseline: without prune, unpaired series silently keeps raw sum", func(t *testing.T) {
		sumDS := loadGoldenDataSet(t, "weighted_avg/baseline_no_prune_sum")
		countDS := loadGoldenDataSet(t, "weighted_avg/baseline_no_prune_count")
		sumDS.FinalizeWeightedAvg(countDS, "")
		got := sumDS.Results[0].SeriesList[0].Points[0].Values[0]
		if got != "200" {
			t.Fatalf("baseline assumption invalid: expected raw sum 200, got %v", got)
		}
		if len(sumDS.Warnings) != 0 {
			t.Errorf("baseline: FinalizeWeightedAvg alone should not warn; got %v", sumDS.Warnings)
		}
	})

	t.Run("nil inputs are no-op", func(t *testing.T) {
		pruneUnpairedWeightedAvgSeries(nil, nil, "")
		sumDS := &dataset.DataSet{}
		pruneUnpairedWeightedAvgSeries(sumDS, nil, "")
		pruneUnpairedWeightedAvgSeries(nil, &dataset.DataSet{}, "")
	})

	t.Run("warnings append is locked", func(t *testing.T) {
		// Reproduces the data race where pruneUnpairedWeightedAvgSeries appends to
		// sumDS.Warnings after releasing sumDS.UpdateLock. A concurrent locked
		// writer sees an unsynchronized append on the same slice, which -race
		// flags. The fix moves the Warnings append inside the locked block.
		mkSumDS := func() *dataset.DataSet {
			return &dataset.DataSet{
				Results: dataset.Results{{
					StatementID: 0,
					SeriesList: dataset.SeriesList{{
						Header: dataset.SeriesHeader{
							Name: "rps", Tags: dataset.Tags{"region": "us-west-2"},
						},
						Points: dataset.Points{{Epoch: epoch.Epoch(100), Size: 32, Values: []any{"200"}}},
					}},
				}},
			}
		}
		countDS := &dataset.DataSet{
			Results: dataset.Results{{StatementID: 0, SeriesList: dataset.SeriesList{}}},
		}

		sumDS := mkSumDS()
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				sumDS.UpdateLock.Lock()
				sumDS.Warnings = append(sumDS.Warnings, "concurrent")
				sumDS.UpdateLock.Unlock()
			}
		})

		for range 500 {
			fresh := mkSumDS()
			sumDS.UpdateLock.Lock()
			sumDS.Results[0].SeriesList = fresh.Results[0].SeriesList
			sumDS.UpdateLock.Unlock()
			pruneUnpairedWeightedAvgSeries(sumDS, countDS, "")
		}
		close(stop)
		wg.Wait()
	})

	t.Run("series paired by hash but with subset of epochs unpaired", func(t *testing.T) {
		sumDS := loadGoldenDataSet(t, "weighted_avg/subset_epochs_sum")
		countDS := loadGoldenDataSet(t, "weighted_avg/subset_epochs_count")

		pruneUnpairedWeightedAvgSeries(sumDS, countDS, "")
		if len(sumDS.Results[0].SeriesList) != 1 {
			t.Fatalf("prune should keep paired series, got %d", len(sumDS.Results[0].SeriesList))
		}
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
		var hasFinalizeWarn bool
		for _, w := range sumDS.Warnings {
			if strings.Contains(w, "weighted-avg series rps") && strings.Contains(w, "no matching count") {
				hasFinalizeWarn = true
				break
			}
		}
		if !hasFinalizeWarn {
			t.Errorf("expected finalize warning naming series rps; got warnings=%v", sumDS.Warnings)
		}
		for _, w := range sumDS.Warnings {
			if strings.Contains(w, "dropped 1 series with no matching count side") {
				t.Errorf("prune should not warn when series is paired; got %q", w)
			}
		}
	})

	t.Run("pairing query statement aligns differing sum/count statements", func(t *testing.T) {
		sumDS := loadGoldenDataSet(t, "weighted_avg/pairing_statement_sum")
		countDS := loadGoldenDataSet(t, "weighted_avg/pairing_statement_count")
		pruneUnpairedWeightedAvgSeries(sumDS, countDS, "avg(rps)")
		if len(sumDS.Results[0].SeriesList) != 1 {
			t.Fatalf("expected paired series preserved, got %d", len(sumDS.Results[0].SeriesList))
		}
		if len(sumDS.Warnings) != 0 {
			t.Errorf("no series should be dropped when paired via pairing statement; got warnings %v", sumDS.Warnings)
		}
	})
}
