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

package prometheus

import (
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func varianceFinalizeSeries(tags dataset.Tags, query string, valuesAndEpochs ...any) *dataset.Series {
	points := make(dataset.Points, 0, len(valuesAndEpochs)/2)
	for i := 0; i+1 < len(valuesAndEpochs); i += 2 {
		value := valuesAndEpochs[i]
		points = append(points, dataset.Point{
			Epoch:  epoch.Epoch(valuesAndEpochs[i+1].(int64)),
			Size:   32,
			Values: []any{value},
		})
	}
	return &dataset.Series{
		Header: dataset.SeriesHeader{
			Name:            tags["__name__"],
			Tags:            tags,
			QueryStatement:  query,
			ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.String}},
		},
		Points: points,
	}
}

func varianceFinalizeDataSet(query string, series ...*dataset.Series) *dataset.DataSet {
	return &dataset.DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: query},
		Results: dataset.Results{{
			SeriesList: series,
		}},
	}
}

func TestFinalizeTSMMergePooledVariance(t *testing.T) {
	makeDataSet := func(state dataset.PooledVarianceState) *dataset.DataSet {
		return varianceFinalizeDataSet("stdvar by (job) (up)",
			varianceFinalizeSeries(dataset.Tags{"job": "api"}, "stdvar by (job) (up)",
				state, int64(100)),
		)
	}

	t.Run("stdvar", func(t *testing.T) {
		ds := makeDataSet(dataset.PooledVarianceState{Count: 5, Mean: 5, M2: 40})
		(&Client{}).FinalizeTSMMerge("stdvar by (job) (up)", ds)
		if got := ds.Results[0].SeriesList[0].Points[0].Values[0]; got != "8" {
			t.Fatalf("value got %v", got)
		}
	})

	t.Run("stddev", func(t *testing.T) {
		ds := makeDataSet(dataset.PooledVarianceState{Count: 5, Mean: 5, M2: 40})
		(&Client{}).FinalizeTSMMerge("stddev by (job) (up)", ds)
		got, _ := strconv.ParseFloat(ds.Results[0].SeriesList[0].Points[0].Values[0].(string), 64)
		if math.Abs(got-math.Sqrt(8)) > 1e-15 {
			t.Fatalf("value got %.17g", got)
		}
	})

	t.Run("singleton and special values", func(t *testing.T) {
		series := varianceFinalizeSeries(dataset.Tags{"job": "api"}, "stdvar(up)",
			dataset.PooledVarianceState{Count: 1, Mean: -3, M2: 0}, int64(100),
			dataset.PooledVarianceState{Count: 2, Mean: math.NaN(), M2: math.NaN()}, int64(200),
			dataset.PooledVarianceState{Count: 2, Mean: 0, M2: math.Inf(1)}, int64(300),
		)
		ds := varianceFinalizeDataSet("stdvar(up)", series)
		(&Client{}).FinalizeTSMMerge("stdvar(up)", ds)
		got := ds.Results[0].SeriesList[0].Points
		if got[0].Values[0] != "0" || got[1].Values[0] != "NaN" || got[2].Values[0] != "+Inf" {
			t.Fatalf("values: %#v", got)
		}
	})

	t.Run("sort wrapper orders finalized states", func(t *testing.T) {
		ds := varianceFinalizeDataSet("stdvar by (job) (up)",
			varianceFinalizeSeries(dataset.Tags{"job": "small"}, "stdvar by (job) (up)",
				dataset.PooledVarianceState{Count: 2, Mean: 0, M2: 2}, int64(100)),
			varianceFinalizeSeries(dataset.Tags{"job": "large"}, "stdvar by (job) (up)",
				dataset.PooledVarianceState{Count: 2, Mean: 0, M2: 18}, int64(100)),
		)
		(&Client{}).FinalizeTSMMerge("sort_desc(stdvar by (job) (up))", ds)
		if got := ds.Results[0].SeriesList; len(got) != 2 ||
			got[0].Header.Tags["job"] != "large" || got[1].Header.Tags["job"] != "small" {
			t.Fatalf("sort order: %#v", got)
		}
	})
}

func TestFinalizeTSMMergeCentralVariance(t *testing.T) {
	const inner = "count by (instance, region) (requests)"
	floatA := varianceFinalizeSeries(dataset.Tags{
		"__name__": "requests", "instance": "a", "region": "east",
	}, inner, "1", int64(100), "2", int64(200))
	floatB := varianceFinalizeSeries(dataset.Tags{
		"__name__": "requests", "instance": "b", "region": "east",
	}, inner, "3", int64(100), "4", int64(200))
	floatC := varianceFinalizeSeries(dataset.Tags{
		"__name__": "requests", "instance": "c", "region": "east",
	}, inner, "5", int64(100))
	histogram := varianceFinalizeSeries(dataset.Tags{
		"__name__": "requests", "instance": "hist", "region": "east",
	}, inner, `{"count":"100","sum":"1000"}`, int64(100))
	histogram.Header.ValueFieldsList[0].Name = histogramFieldName
	ds := varianceFinalizeDataSet(inner, floatA, histogram, floatB, floatC)

	(&Client{}).FinalizeTSMMerge("stdvar by (region) ("+inner+")", ds)
	if len(ds.Results[0].SeriesList) != 1 {
		t.Fatalf("series: %#v", ds.Results[0].SeriesList)
	}
	series := ds.Results[0].SeriesList[0]
	if got := series.Header.Tags.JSON(); got != `{"region":"east"}` {
		t.Fatalf("tags: %s", got)
	}
	if len(series.Points) != 2 || series.Points[0].Epoch != 100 || series.Points[1].Epoch != 200 {
		t.Fatalf("points: %#v", series.Points)
	}
	first, _ := strconv.ParseFloat(series.Points[0].Values[0].(string), 64)
	second, _ := strconv.ParseFloat(series.Points[1].Values[0].(string), 64)
	if math.Abs(first-8.0/3.0) > 1e-15 || second != 1 {
		t.Fatalf("values got %.17g and %.17g", first, second)
	}
}

func TestFinalizeTSMMergeVarianceGroupingAndSort(t *testing.T) {
	const inner = "count without (shard) (requests)"
	series := []*dataset.Series{
		varianceFinalizeSeries(dataset.Tags{"instance": "a", "region": "east", "job": "api",
			"__type__": "gauge", "__unit__": "requests"}, inner,
			"1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b", "region": "east", "job": "api",
			"__type__": "gauge", "__unit__": "requests"}, inner,
			"3", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "a", "region": "west", "job": "api",
			"__type__": "gauge", "__unit__": "requests"}, inner,
			"1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b", "region": "west", "job": "api",
			"__type__": "gauge", "__unit__": "requests"}, inner,
			"7", int64(100)),
	}
	ds := varianceFinalizeDataSet(inner, series...)
	(&Client{}).FinalizeTSMMerge("sort_desc(stdvar without (instance) ("+inner+"))", ds)

	got := ds.Results[0].SeriesList
	if len(got) != 2 || got[0].Header.Tags["region"] != "west" ||
		got[1].Header.Tags["region"] != "east" {
		t.Fatalf("sort order: %#v", got)
	}
	if got[0].Header.Tags.JSON() !=
		`{"__type__":"gauge","__unit__":"requests","job":"api","region":"west"}` {
		t.Fatalf("without tags: %s", got[0].Header.Tags.JSON())
	}
	if got[0].Points[0].Values[0] != "9" || got[1].Points[0].Values[0] != "1" {
		t.Fatalf("values: %v %v", got[0].Points[0].Values, got[1].Points[0].Values)
	}
}

func TestFinalizeTSMMergeVarianceRangeSortWarning(t *testing.T) {
	const inner = "count by (instance) (requests)"
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(dataset.Tags{"instance": "a"}, inner, "1", int64(100), "3", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b"}, inner, "5", int64(100)),
	)
	ds.TimeRangeQuery.Step = time.Minute
	(&Client{}).FinalizeTSMMerge("sort(stddev("+inner+"))", ds)
	if len(ds.Warnings) != 1 || ds.Warnings[0] != sortInRangeQueryWarning {
		t.Fatalf("warnings: %v", ds.Warnings)
	}
}

func TestFinalizeTSMMergeVarianceFallbackOnlySorts(t *testing.T) {
	const fallback = "stddev(up + down)"
	ds := varianceFinalizeDataSet(fallback,
		varianceFinalizeSeries(dataset.Tags{"instance": "a"}, fallback, "1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b"}, fallback, "3", int64(100)),
	)
	(&Client{}).FinalizeTSMMerge("sort_desc("+fallback+")", ds)
	if len(ds.Results[0].SeriesList) != 2 ||
		ds.Results[0].SeriesList[0].Header.Tags["instance"] != "b" {
		t.Fatalf("fallback was regrouped or unsorted: %#v", ds.Results[0].SeriesList)
	}
}
