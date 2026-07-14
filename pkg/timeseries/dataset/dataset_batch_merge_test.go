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
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func batchMergeSeries(name, host string, values ...string) *Series {
	points := make(Points, len(values))
	for i, value := range values {
		points[i] = Point{
			Epoch:  epoch.Epoch((i + 1) * int(timeseries.Second)),
			Values: []any{value},
		}
	}
	return &Series{
		Header: SeriesHeader{
			Name:           name,
			Tags:           Tags{"host": host},
			QueryStatement: "fixture",
		},
		Points:    points,
		PointSize: points.Size(),
	}
}

func batchMergeFixture() []*DataSet {
	step := 15 * time.Second
	makeDataSet := func(status, warning string, series ...*Series) *DataSet {
		return &DataSet{
			Status:         status,
			Warnings:       []string{warning},
			TimeRangeQuery: &timeseries.TimeRangeQuery{Step: step},
			ExtentList: timeseries.ExtentList{{
				Start: time.Unix(0, 0),
				End:   time.Unix(60, 0),
			}},
			Results: Results{{StatementID: 0, SeriesList: series}},
		}
	}
	return []*DataSet{
		makeDataSet("error", "member-0",
			batchMergeSeries("cpu", "a", "1", "2"),
			batchMergeSeries("cpu", "base-only", "7")),
		makeDataSet("success", "member-1",
			batchMergeSeries("cpu", "a", "3", "4"),
			batchMergeSeries("cpu", "b", "5"),
			// Repeated hashes within one member are ignored by the existing merge.
			batchMergeSeries("cpu", "b", "999")),
		makeDataSet("success", "member-2",
			batchMergeSeries("cpu", "a", "6", "8"),
			batchMergeSeries("cpu", "c", "9")),
		makeDataSet("success", "member-3",
			batchMergeSeries("cpu", "b", "10"),
			batchMergeSeries("cpu", "d", "11")),
	}
}

func batchMergeSnapshot(ds *DataSet) []string {
	out := make([]string, 0, ds.SeriesCount())
	for _, result := range ds.Results {
		if result == nil {
			continue
		}
		for _, series := range result.SeriesList {
			if series == nil {
				continue
			}
			out = append(out, fmt.Sprintf("%d:%s", result.StatementID, series.String()))
		}
	}
	return out
}

func TestBatchMergeMatchesSequentialMerge(t *testing.T) {
	tests := []struct {
		name string
		opts MergeOpts
	}{
		{name: "dedup", opts: MergeOpts{SortPoints: true}},
		{name: "dedup_with_tolerance", opts: MergeOpts{
			SortPoints: true, Strategy: MergeStrategyDedup, ToleranceNanos: 1,
		}},
		{name: "sum", opts: MergeOpts{SortPoints: true, Strategy: MergeStrategySum}},
		{name: "min", opts: MergeOpts{SortPoints: true, Strategy: MergeStrategyMin}},
		{name: "max", opts: MergeOpts{SortPoints: true, Strategy: MergeStrategyMax}},
		{name: "avg", opts: MergeOpts{SortPoints: true, Strategy: MergeStrategyAvg}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sequentialInputs := batchMergeFixture()
			sequential := sequentialInputs[0]
			for _, next := range sequentialInputs[1:] {
				sequential.MergeWithOpts(test.opts, next)
			}

			batchInputs := batchMergeFixture()
			batch := batchInputs[0]
			collection := make([]timeseries.Timeseries, len(batchInputs)-1)
			for i := 1; i < len(batchInputs); i++ {
				collection[i-1] = batchInputs[i]
			}
			batch.MergeWithOpts(test.opts, collection...)

			if got, want := batchMergeSnapshot(batch), batchMergeSnapshot(sequential); !reflect.DeepEqual(got, want) {
				t.Fatalf("batch result differs from sequential merge\ngot:  %v\nwant: %v", got, want)
			}
			if !reflect.DeepEqual(batch.ExtentList, sequential.ExtentList) {
				t.Fatalf("extent list differs: got %v want %v", batch.ExtentList, sequential.ExtentList)
			}
			if test.opts.Strategy == MergeStrategyDedup && test.opts.ToleranceNanos == 0 {
				if batch.Status != sequential.Status {
					t.Fatalf("status differs: got %q want %q", batch.Status, sequential.Status)
				}
				if !reflect.DeepEqual(batch.Warnings, sequential.Warnings) {
					t.Fatalf("warnings differ: got %v want %v", batch.Warnings, sequential.Warnings)
				}
			}
		})
	}
}

func TestBatchMergePreservesAliasedSeriesOrder(t *testing.T) {
	merge := func(batch bool) []string {
		shared := batchMergeSeries("cpu", "shared", "2")
		base := &DataSet{Results: Results{{StatementID: 0, SeriesList: SeriesList{shared}}}}
		members := []timeseries.Timeseries{
			&DataSet{Results: Results{{StatementID: 0, SeriesList: SeriesList{shared}}}},
			&DataSet{Results: Results{{StatementID: 0, SeriesList: SeriesList{shared}}}},
		}
		if batch {
			base.MergeWithStrategy(true, int(MergeStrategySum), members...)
		} else {
			for _, member := range members {
				base.MergeWithStrategy(true, int(MergeStrategySum), member)
			}
		}
		return batchMergeSnapshot(base)
	}

	if got, want := merge(true), merge(false); !reflect.DeepEqual(got, want) {
		t.Fatalf("aliased batch result differs\ngot:  %v\nwant: %v", got, want)
	}
}

func benchmarkMergeFixture(members, seriesPerMember int, overlap bool) []*DataSet {
	out := make([]*DataSet, members)
	for member := range members {
		series := make(SeriesList, seriesPerMember)
		for i := range seriesPerMember {
			id := i
			if !overlap {
				id += member * seriesPerMember
			}
			series[i] = batchMergeSeries("metric", strconv.Itoa(id), strconv.Itoa(member+1))
		}
		out[member] = &DataSet{
			TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 15 * time.Second},
			Results:        Results{{StatementID: 0, SeriesList: series}},
		}
	}
	return out
}

func BenchmarkFanoutDataSetMerge(b *testing.B) {
	for _, overlap := range []bool{false, true} {
		name := "disjoint"
		if overlap {
			name = "overlap"
		}
		b.Run(name+"/sequential", func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				inputs := benchmarkMergeFixture(64, 1000, overlap)
				b.StartTimer()
				for _, next := range inputs[1:] {
					inputs[0].MergeWithStrategy(true, int(MergeStrategySum), next)
				}
			}
		})
		b.Run(name+"/batch", func(b *testing.B) {
			for b.Loop() {
				b.StopTimer()
				inputs := benchmarkMergeFixture(64, 1000, overlap)
				collection := make([]timeseries.Timeseries, len(inputs)-1)
				for i := 1; i < len(inputs); i++ {
					collection[i-1] = inputs[i]
				}
				b.StartTimer()
				inputs[0].MergeWithStrategy(true, int(MergeStrategySum), collection...)
			}
		})
	}
}
