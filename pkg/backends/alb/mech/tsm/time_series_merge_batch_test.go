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
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func batchContributionDataSet(value int) *dataset.DataSet {
	points := dataset.Points{{Epoch: epoch.Epoch(1), Values: []any{strconv.Itoa(value)}}}
	return &dataset.DataSet{
		Results: dataset.Results{{
			StatementID: 0,
			SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{
					Name:           "requests",
					Tags:           dataset.Tags{"service": "api"},
					QueryStatement: "sum by (service) (requests)",
				},
				Points:    points,
				PointSize: points.Size(),
			}},
		}},
	}
}

func TestMergeGatherContributionsBatchesDataSets(t *testing.T) {
	mergeFunc := merge.TimeseriesMergeFuncWithStrategy(nil, int(dataset.MergeStrategySum))
	batchMergeFunc := merge.TimeseriesBatchMergeFuncWithStrategy(
		int(dataset.MergeStrategySum))
	contributions := []*gatherContribution{
		{data: batchContributionDataSet(1), mergeFunc: mergeFunc,
			batchMergeFunc: batchMergeFunc, member: 0},
		nil,
		{data: batchContributionDataSet(2), mergeFunc: mergeFunc,
			batchMergeFunc: batchMergeFunc, member: 2},
		{data: batchContributionDataSet(3), mergeFunc: mergeFunc,
			batchMergeFunc: batchMergeFunc, member: 3},
	}
	accumulator := merge.NewAccumulator()

	if failed := mergeGatherContributions(context.Background(), accumulator, contributions); len(failed) != 0 {
		t.Fatalf("unexpected merge failures: %v", failed)
	}

	got, ok := accumulator.GetTSData().(*dataset.DataSet)
	if !ok || got == nil {
		t.Fatal("expected a merged dataset")
	}
	if accumulator.MergeCount != 3 {
		t.Fatalf("merge count got %d want 3", accumulator.MergeCount)
	}
	if len(got.Results) != 1 || len(got.Results[0].SeriesList) != 1 ||
		len(got.Results[0].SeriesList[0].Points) != 1 {
		t.Fatalf("unexpected merged shape: %#v", got.Results)
	}
	if value := fmt.Sprint(got.Results[0].SeriesList[0].Points[0].Values[0]); value != "6" {
		t.Fatalf("merged value got %q want 6", value)
	}
}

func TestMergeGatherContributionsStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var merged []int
	mergeFunc := func(_ *merge.Accumulator, _ any, member int) error {
		merged = append(merged, member)
		if member == 0 {
			cancel()
		}
		return nil
	}
	contributions := []*gatherContribution{
		{data: "first", mergeFunc: mergeFunc, member: 0},
		{data: "second", mergeFunc: mergeFunc, member: 1},
	}

	failed := mergeGatherContributions(ctx, merge.NewAccumulator(), contributions)
	if len(failed) != 0 {
		t.Fatalf("unexpected merge failures: %v", failed)
	}
	if !reflect.DeepEqual(merged, []int{0}) {
		t.Fatalf("merged members got %v want [0]", merged)
	}
}

func TestMergeGatherContributionsFallbackReportsMember(t *testing.T) {
	var batchMembers []int
	batchMergeFunc := func(accumulator *merge.Accumulator,
		items []merge.BatchItem,
	) (bool, error) {
		if accumulator.GetGeneric() != nil {
			t.Fatal("batch fallback contract requires an untouched accumulator")
		}
		for _, item := range items {
			batchMembers = append(batchMembers, item.Member)
		}
		return false, nil
	}
	mergeFunc := func(accumulator *merge.Accumulator, _ any, member int) error {
		if member == 1 {
			panic("merge panic")
		}
		if member == 2 {
			return errors.New("merge failed")
		}
		members, _ := accumulator.GetGeneric().([]int)
		accumulator.SetGeneric(append(members, member))
		return nil
	}
	contributions := []*gatherContribution{
		{data: "first", mergeFunc: mergeFunc, batchMergeFunc: batchMergeFunc, member: 0},
		{data: "panic", mergeFunc: mergeFunc, batchMergeFunc: batchMergeFunc, member: 1},
		{data: "bad", mergeFunc: mergeFunc, batchMergeFunc: batchMergeFunc, member: 2},
		{data: "last", mergeFunc: mergeFunc, batchMergeFunc: batchMergeFunc, member: 3},
	}
	accumulator := merge.NewAccumulator()

	failed := mergeGatherContributions(context.Background(), accumulator, contributions)
	if !reflect.DeepEqual(batchMembers, []int{0, 1, 2, 3}) {
		t.Fatalf("batch member order got %v want [0 1 2 3]", batchMembers)
	}
	if !reflect.DeepEqual(failed, []int{1, 2}) {
		t.Fatalf("failed members got %v want [1 2]", failed)
	}
	if got := accumulator.GetGeneric(); !reflect.DeepEqual(got, []int{0, 3}) {
		t.Fatalf("merge order got %v want [0 3]", got)
	}
}

func TestMergeGatherContributionsRequiresEveryBatchFunc(t *testing.T) {
	var batchCalled bool
	batchMergeFunc := func(*merge.Accumulator, []merge.BatchItem) (bool, error) {
		batchCalled = true
		return true, nil
	}
	mergeFunc := func(accumulator *merge.Accumulator, _ any, member int) error {
		members, _ := accumulator.GetGeneric().([]int)
		accumulator.SetGeneric(append(members, member))
		return nil
	}
	contributions := []*gatherContribution{
		{data: "first", mergeFunc: mergeFunc, batchMergeFunc: batchMergeFunc, member: 0},
		{data: "second", mergeFunc: mergeFunc, member: 1},
	}
	accumulator := merge.NewAccumulator()

	if failed := mergeGatherContributions(context.Background(), accumulator, contributions); len(failed) != 0 {
		t.Fatalf("unexpected merge failures: %v", failed)
	}
	if batchCalled {
		t.Fatal("batch merge must require every contribution to opt in")
	}
	if got := accumulator.GetGeneric(); !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("merge order got %v want [0 1]", got)
	}
}

func TestMergeGatherContributionsRecoversBatchPanic(t *testing.T) {
	first := batchContributionDataSet(1)
	first.Merger = func(bool, ...timeseries.Timeseries) {
		panic("dataset merge panic")
	}
	mergeFunc := merge.TimeseriesMergeFunc(nil)
	batchMergeFunc := merge.TimeseriesBatchMergeFunc()
	contributions := []*gatherContribution{
		{data: first, mergeFunc: mergeFunc, batchMergeFunc: batchMergeFunc, member: 0},
		{data: batchContributionDataSet(2), mergeFunc: mergeFunc,
			batchMergeFunc: batchMergeFunc, member: 1},
	}
	accumulator := merge.NewAccumulator()

	failed := mergeGatherContributions(context.Background(), accumulator, contributions)
	if !reflect.DeepEqual(failed, []int{0, 1}) {
		t.Fatalf("failed members got %v want [0 1]", failed)
	}
	if accumulator.GetTSData() != nil {
		t.Fatal("panicked batch must not publish a partial accumulator")
	}
}
