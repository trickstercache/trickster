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
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func pooledVarianceTestDataSet(query string, tags dataset.Tags, values ...any) *dataset.DataSet {
	points := make(dataset.Points, 0, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		points = append(points, dataset.Point{
			Epoch:  epoch.Epoch(values[i].(int64)),
			Values: []any{values[i+1]},
		})
	}
	return &dataset.DataSet{
		Status:         "success",
		Warnings:       []string{"origin warning"},
		TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: query},
		Results: dataset.Results{{
			StatementID: 4,
			Name:        "result",
			SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{
					Name:                tags["__name__"],
					Tags:                tags,
					QueryStatement:      query,
					ValueFieldsList:     timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.String}},
					TimestampField:      timeseries.FieldDefinition{Name: "time"},
					UntrackedFieldsList: nil,
				},
				Points: points,
			}},
		}},
	}
}

func pooledVarianceTestPlan() *tsmerge.TSMMergePlan {
	names := tsmerge.TSMReductionPooledVarianceVariants()
	return &tsmerge.TSMMergePlan{
		OriginalQuery: "stdvar by (job) (up)",
		Variants: []tsmerge.TSMQueryVariant{
			{Name: names[0]},
			{Name: names[1]},
			{Name: names[2]},
		},
		Reduction: tsmerge.TSMReductionSpec{
			Kind:          tsmerge.TSMReductionPooledVariance,
			InputVariants: names,
		},
	}
}

func pooledVarianceTestExecutions(members [][3]any) []planVariantExecution {
	executions := make([]planVariantExecution, 3)
	for variant := range executions {
		executions[variant].contributions = make([]*gatherContribution, len(members))
		for member := range members {
			if members[member][variant] == nil {
				continue
			}
			executions[variant].contributions[member] = &gatherContribution{
				data:   members[member][variant],
				member: member,
			}
		}
	}
	return executions
}

func TestReducePooledVariancePlan(t *testing.T) {
	tags := dataset.Tags{"job": "api"}
	member := func(count, mean, variance string) [3]any {
		return [3]any{
			pooledVarianceTestDataSet("count by (job) (up)", tags.Clone(), int64(100), count),
			pooledVarianceTestDataSet("avg by (job) (up)", tags.Clone(), int64(100), mean),
			pooledVarianceTestDataSet("stdvar by (job) (up)", tags.Clone(), int64(100), variance),
		}
	}
	members := [][3]any{
		member("2", "2", "1"),
		member("3", "7", "2.6666666666666665"),
		member("2", "2", "1"), // exact HA replica of member 0
	}
	members[0][0].(*dataset.DataSet).ExtentList = timeseries.ExtentList{{
		Start: time.Unix(0, 0), End: time.Unix(10, 0),
	}}
	members[1][0].(*dataset.DataSet).ExtentList = timeseries.ExtentList{{
		Start: time.Unix(5, 0), End: time.Unix(20, 0),
	}}

	accumulator, warnings, err := reducePooledVariancePlan(
		context.Background(), pooledVarianceTestPlan(), pooledVarianceTestExecutions(members),
	)
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	ds, ok := accumulator.GetTSData().(*dataset.DataSet)
	if !ok || len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 1 {
		t.Fatalf("output: %#v", accumulator.GetTSData())
	}
	series := ds.Results[0].SeriesList[0]
	state, ok := series.Points[0].Values[0].(dataset.PooledVarianceState)
	if !ok {
		t.Fatalf("point value type: %T", series.Points[0].Values[0])
	}
	if got, want := state.PopulationVariance(), 8.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("variance got %.17g want %.17g", got, want)
	}
	if state.Count != 5 {
		t.Fatalf("deduplicated count got %v want 5", state.Count)
	}
	if series.Header.QueryStatement != pooledVarianceTestPlan().OriginalQuery {
		t.Fatalf("query statement: %q", series.Header.QueryStatement)
	}
	if ds.TimeRangeQuery == nil || ds.TimeRangeQuery.Statement != pooledVarianceTestPlan().OriginalQuery {
		t.Fatalf("time range query: %#v", ds.TimeRangeQuery)
	}
	if len(ds.Warnings) != len(members) {
		t.Fatalf("dataset warnings: %v", ds.Warnings)
	}
	if len(ds.ExtentList) != 1 || !ds.ExtentList[0].Start.Equal(time.Unix(0, 0)) ||
		!ds.ExtentList[0].End.Equal(time.Unix(20, 0)) {
		t.Fatalf("dataset extents: %v", ds.ExtentList)
	}
	for _, warning := range ds.Warnings {
		if warning != "origin warning" {
			t.Fatalf("dataset warnings: %v", ds.Warnings)
		}
	}
}

func TestReducePooledVariancePlanDropsUnpairedPoints(t *testing.T) {
	tags := dataset.Tags{"job": "api"}
	members := [][3]any{{
		pooledVarianceTestDataSet("count", tags.Clone(),
			int64(100), "2", int64(200), "2"),
		pooledVarianceTestDataSet("avg", tags.Clone(), int64(100), "3"),
		pooledVarianceTestDataSet("stdvar", tags.Clone(),
			int64(100), "1", int64(200), "1", int64(300), "0"),
	}}

	accumulator, warnings, err := reducePooledVariancePlan(
		context.Background(), pooledVarianceTestPlan(), pooledVarianceTestExecutions(members),
	)
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "2 incomplete") {
		t.Fatalf("warnings: %v", warnings)
	}
	ds := accumulator.GetTSData().(*dataset.DataSet)
	points := ds.Results[0].SeriesList[0].Points
	if len(points) != 1 || points[0].Epoch != 100 {
		t.Fatalf("points: %#v", points)
	}
}

func TestReducePooledVariancePlanDropsUnpairedSeries(t *testing.T) {
	api := dataset.Tags{"job": "api"}
	db := dataset.Tags{"job": "db"}
	countDS := pooledVarianceTestDataSet("count", api.Clone(), int64(100), "2")
	countDS.Results[0].SeriesList = append(countDS.Results[0].SeriesList,
		pooledVarianceTestDataSet("count", db.Clone(), int64(100), "2").Results[0].SeriesList[0])
	meanDS := pooledVarianceTestDataSet("avg", api.Clone(), int64(100), "3")
	varianceDS := pooledVarianceTestDataSet("stdvar", api.Clone(), int64(100), "1")
	varianceDS.Results[0].SeriesList = append(varianceDS.Results[0].SeriesList,
		pooledVarianceTestDataSet("stdvar", db.Clone(), int64(100), "4").Results[0].SeriesList[0])
	members := [][3]any{{countDS, meanDS, varianceDS}}

	accumulator, warnings, err := reducePooledVariancePlan(
		context.Background(), pooledVarianceTestPlan(), pooledVarianceTestExecutions(members),
	)
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "1 incomplete") {
		t.Fatalf("warnings: %v", warnings)
	}
	ds := accumulator.GetTSData().(*dataset.DataSet)
	if got := ds.Results[0].SeriesList; len(got) != 1 || got[0].Header.Tags["job"] != "api" {
		t.Fatalf("series: %#v", got)
	}
}

func TestReducePooledVariancePlanRejectsUnexpectedData(t *testing.T) {
	members := [][3]any{{[]byte("wire"), &dataset.DataSet{}, &dataset.DataSet{}}}
	if _, _, err := reducePooledVariancePlan(
		context.Background(), pooledVarianceTestPlan(), pooledVarianceTestExecutions(members),
	); err == nil {
		t.Fatal("unexpected data type was accepted")
	}
}

func TestReducePooledVariancePlanDropsInvalidCount(t *testing.T) {
	tags := dataset.Tags{"job": "api"}
	members := [][3]any{{
		pooledVarianceTestDataSet("count", tags.Clone(), int64(100), "1.5"),
		pooledVarianceTestDataSet("avg", tags.Clone(), int64(100), "2"),
		pooledVarianceTestDataSet("stdvar", tags.Clone(), int64(100), "0"),
	}}
	accumulator, warnings, err := reducePooledVariancePlan(
		context.Background(), pooledVarianceTestPlan(), pooledVarianceTestExecutions(members),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "1 incomplete") {
		t.Fatalf("warnings: %v", warnings)
	}
	ds := accumulator.GetTSData().(*dataset.DataSet)
	if len(ds.Results[0].SeriesList) != 0 {
		t.Fatalf("invalid count produced series: %#v", ds.Results[0].SeriesList)
	}
}

func TestPairPooledVarianceMemberIgnoresHistogramSeries(t *testing.T) {
	tags := dataset.Tags{"job": "api"}
	countDS := pooledVarianceTestDataSet("count", tags.Clone(), int64(100), "2")
	countDS.Results[0].SeriesList[0].Header.ValueFieldsList[0].Name = "histogram"
	meanDS := pooledVarianceTestDataSet("avg", tags.Clone(), int64(100), "3")
	varianceDS := pooledVarianceTestDataSet("stdvar", tags.Clone(), int64(100), "1")

	points, dropped, err := pairPooledVarianceMember(
		context.Background(), countDS, meanDS, varianceDS, pooledVarianceTestPlan().OriginalQuery,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 || dropped != 1 {
		t.Fatalf("points=%#v dropped=%d", points, dropped)
	}
}

func TestIndexPooledVariancePointsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ds := pooledVarianceTestDataSet("count", dataset.Tags{"job": "api"}, int64(100), "2")
	if _, err := indexPooledVariancePoints(ctx, ds, pooledVarianceTestPlan().OriginalQuery); err == nil {
		t.Fatal("canceled indexing unexpectedly succeeded")
	}
}

func BenchmarkReducePooledVariancePlan(b *testing.B) {
	const (
		shardCount = 16
		groupCount = 64
		stepCount  = 30
	)
	members := make([][3]any, shardCount)
	queries := [3]string{"count", "avg", "stdvar"}
	for shard := range shardCount {
		for variant := range 3 {
			ds := &dataset.DataSet{Results: dataset.Results{{}}}
			for group := range groupCount {
				points := make(dataset.Points, stepCount)
				for step := range stepCount {
					value := "100"
					switch variant {
					case 1:
						value = strconv.Itoa(shard + group)
					case 2:
						value = "4"
					}
					points[step] = dataset.Point{
						Epoch:  epoch.Epoch(step + 1),
						Values: []any{value},
					}
				}
				ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, &dataset.Series{
					Header: dataset.SeriesHeader{
						Tags:           dataset.Tags{"group": strconv.Itoa(group)},
						QueryStatement: queries[variant],
					},
					Points: points,
				})
			}
			members[shard][variant] = ds
		}
	}
	plan := pooledVarianceTestPlan()
	executions := pooledVarianceTestExecutions(members)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		accumulator, _, err := reducePooledVariancePlan(context.Background(), plan, executions)
		if err != nil {
			b.Fatalf("reduce: %v", err)
		}
		if accumulator.GetTSData() == nil {
			b.Fatal("reduce returned no data")
		}
	}
}
