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
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestFinalizeTSMMergeLimitKUsesStableFirstVisitedOrder(t *testing.T) {
	const inner = "up"
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "z"}, inner,
			"1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "a"}, inner,
			"100", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "m"}, inner,
			"50", int64(100)),
	)

	(&Client{}).FinalizeTSMMerge("limitk(2, up)", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 2 || got[0].Header.Tags["instance"] != "a" ||
		got[1].Header.Tags["instance"] != "m" ||
		got[0].Points[0].Values[0] != "100" || got[1].Points[0].Values[0] != "50" {
		t.Fatalf("selected series: %#v", got)
	}
}

func TestFinalizeTSMMergeLimitKDoesNotRankByValueOrHash(t *testing.T) {
	const inner = "up"
	aTags := dataset.Tags{"__name__": "up", "instance": "a"}
	bTags := dataset.Tags{"__name__": "up", "instance": "b"}
	if prometheusLabelsHash(aTags) <= prometheusLabelsHash(bTags) {
		t.Fatal("fixture must put b before a in label-hash order")
	}
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(aTags, inner, "0", int64(100)),
		varianceFinalizeSeries(bTags, inner, "100", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "c"}, inner,
			"-100", int64(100)),
	)

	(&Client{}).FinalizeTSMMerge("limitk(1, up)", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 1 || got[0].Header.Tags["instance"] != "a" {
		t.Fatalf("selected series: %#v", got)
	}
}

func TestFinalizeTSMMergeLimitKIsIndependentOfInputOrder(t *testing.T) {
	const inner = "up"
	series := map[string]*dataset.Series{
		"a": varianceFinalizeSeries(dataset.Tags{"instance": "a"}, inner, "1", int64(100)),
		"b": varianceFinalizeSeries(dataset.Tags{"instance": "b"}, inner, "2", int64(100)),
		"c": varianceFinalizeSeries(dataset.Tags{"instance": "c"}, inner, "3", int64(100)),
	}
	for _, order := range [][]string{{"a", "b", "c"}, {"c", "a", "b"}, {"b", "c", "a"}} {
		input := make(dataset.SeriesList, 0, len(order))
		for _, name := range order {
			input = append(input, series[name].Clone())
		}
		ds := varianceFinalizeDataSet(inner, input...)
		(&Client{}).FinalizeTSMMerge("limitk(2, up)", ds)
		got := ds.Results[0].SeriesList
		if len(got) != 2 || got[0].Header.Tags["instance"] != "a" ||
			got[1].Header.Tags["instance"] != "b" {
			t.Fatalf("input order %v selected %#v", order, got)
		}
	}
}

func TestFinalizeTSMMergeLimitKGrouping(t *testing.T) {
	const inner = "up"
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(dataset.Tags{"instance": "z-api", "job": "api"}, inner,
			"1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "a-db", "job": "db"}, inner,
			"2", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "a-api", "job": "api"}, inner,
			"3", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "m-api", "job": "api"}, inner,
			"5", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "m-db", "job": "db"}, inner,
			"6", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "z-db", "job": "db"}, inner,
			"4", int64(100)),
	)

	(&Client{}).FinalizeTSMMerge("limitk by (job) (2, up)", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 4 || got[0].Header.Tags["instance"] != "a-api" ||
		got[1].Header.Tags["instance"] != "a-db" ||
		got[2].Header.Tags["instance"] != "m-api" ||
		got[3].Header.Tags["instance"] != "m-db" {
		t.Fatalf("selected groups: %#v", got)
	}
	for _, series := range got {
		if len(series.Header.Tags) != 2 || series.Header.Tags["job"] == "" {
			t.Fatalf("selected labels were changed: %#v", series.Header.Tags)
		}
	}
}

func TestFinalizeTSMMergeLimitKSparseRangeChangesMembership(t *testing.T) {
	const inner = "up"
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(dataset.Tags{"instance": "a"}, inner, "1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b"}, inner, "2", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"instance": "c"}, inner,
			"3", int64(100), "4", int64(200)),
	)
	ds.TimeRangeQuery.Step = time.Minute

	(&Client{}).FinalizeTSMMerge("limitk(1, up)", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 2 || got[0].Header.Tags["instance"] != "a" ||
		got[1].Header.Tags["instance"] != "b" ||
		got[0].Points[0].Epoch != 100 || got[1].Points[0].Epoch != 200 {
		t.Fatalf("selected range: %#v", got)
	}
}

func TestFinalizeTSMMergeLimitKWithoutGroupingSparseRange(t *testing.T) {
	const inner = "up"
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(dataset.Tags{"instance": "a", "job": "api"}, inner,
			"1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "a", "job": "db"}, inner,
			"2", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b", "job": "api"}, inner,
			"3", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b", "job": "db"}, inner,
			"4", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "c", "job": "api"}, inner,
			"5", int64(100), "6", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"instance": "c", "job": "db"}, inner,
			"7", int64(100), "8", int64(200)),
	)
	ds.TimeRangeQuery.Step = time.Minute

	(&Client{}).FinalizeTSMMerge("limitk without (instance) (1, up)", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 4 || got[0].Header.Tags["instance"] != "a" ||
		got[0].Header.Tags["job"] != "api" || got[0].Points[0].Epoch != 100 ||
		got[1].Header.Tags["instance"] != "a" || got[1].Header.Tags["job"] != "db" ||
		got[1].Points[0].Epoch != 200 || got[2].Header.Tags["instance"] != "b" ||
		got[2].Header.Tags["job"] != "api" || got[2].Points[0].Epoch != 200 ||
		got[3].Header.Tags["instance"] != "b" || got[3].Header.Tags["job"] != "db" ||
		got[3].Points[0].Epoch != 100 {
		t.Fatalf("selected grouped range: %#v", got)
	}
}

func TestFinalizeTSMMergeLimitKCardinality(t *testing.T) {
	makeDataSet := func() *dataset.DataSet {
		return varianceFinalizeDataSet("up",
			varianceFinalizeSeries(dataset.Tags{"instance": "a"}, "up", "1", int64(100)),
			varianceFinalizeSeries(dataset.Tags{"instance": "b"}, "up", "2", int64(100)),
		)
	}
	tests := []struct {
		query string
		want  int
	}{
		{query: "limitk(0, up)", want: 0},
		{query: "limitk(0.9, up)", want: 0},
		{query: "limitk(1.9, up)", want: 1},
		{query: "limitk(10, up)", want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			ds := makeDataSet()
			client := &Client{}
			client.FinalizeTSMMerge(tt.query, ds)
			client.FinalizeTSMMerge(tt.query, ds)
			if got := len(ds.Results[0].SeriesList); got != tt.want {
				t.Fatalf("series count got %d want %d", got, tt.want)
			}
		})
	}
}

func TestFinalizeTSMMergeLimitKPreservesFloatAndHistogramSamples(t *testing.T) {
	const inner = "up"
	floatSeries := varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "a"}, inner,
		"1", int64(100))
	histogramSeries := varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "a"}, inner,
		`{"count":"2","sum":"3"}`, int64(200))
	histogramSeries.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
	laterSeries := varianceFinalizeSeries(dataset.Tags{"__name__": "up", "instance": "b"}, inner,
		"10", int64(100), "20", int64(200))
	ds := varianceFinalizeDataSet(inner, laterSeries, histogramSeries, floatSeries)
	ds.TimeRangeQuery.Step = time.Minute

	(&Client{}).FinalizeTSMMerge("limitk(1, up)", ds)
	got := ds.Results[0].SeriesList
	var gotFloat, gotHistogram *dataset.Series
	for _, series := range got {
		if isHistogramSeries(series) {
			gotHistogram = series
		} else {
			gotFloat = series
		}
	}
	if len(got) != 2 || gotFloat == nil || gotHistogram == nil ||
		gotFloat.Header.Tags["instance"] != "a" || gotHistogram.Header.Tags["instance"] != "a" ||
		gotFloat.Points[0].Values[0] != "1" ||
		gotHistogram.Points[0].Values[0] != `{"count":"2","sum":"3"}` {
		t.Fatalf("mixed samples: %#v", got)
	}
}

func TestFinalizeTSMMergeLimitKSortWrapper(t *testing.T) {
	const inner = "up"
	histogram := varianceFinalizeSeries(dataset.Tags{"instance": "a"}, inner,
		`{"count":"2","sum":"3"}`, int64(100))
	histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
	makeDataSet := func() *dataset.DataSet {
		return varianceFinalizeDataSet(inner,
			histogram.Clone(),
			varianceFinalizeSeries(dataset.Tags{"instance": "b"}, inner, "1", int64(100)),
			varianceFinalizeSeries(dataset.Tags{"instance": "c"}, inner, "3", int64(100)),
		)
	}

	t.Run("instant sort drops histograms and orders floats", func(t *testing.T) {
		ds := makeDataSet()
		(&Client{}).FinalizeTSMMerge("sort_desc(limitk(3, up))", ds)
		got := ds.Results[0].SeriesList
		if len(got) != 2 || got[0].Header.Tags["instance"] != "c" ||
			got[1].Header.Tags["instance"] != "b" {
			t.Fatalf("sort result: %#v", got)
		}
	})

	t.Run("range sort drops histograms and warns", func(t *testing.T) {
		ds := makeDataSet()
		ds.TimeRangeQuery.Step = time.Minute
		(&Client{}).FinalizeTSMMerge("sort(limitk(3, up))", ds)
		if len(ds.Results[0].SeriesList) != 2 || len(ds.Warnings) != 1 ||
			ds.Warnings[0] != sortInRangeQueryWarning {
			t.Fatalf("series=%#v warnings=%v", ds.Results[0].SeriesList, ds.Warnings)
		}
	})
}

func TestFinalizeTSMMergeLimitKFallbackOnlySorts(t *testing.T) {
	const fallback = "limitk(1, up + down)"
	ds := varianceFinalizeDataSet(fallback,
		varianceFinalizeSeries(dataset.Tags{"instance": "a"}, fallback, "1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"instance": "b"}, fallback, "3", int64(100)),
	)
	(&Client{}).FinalizeTSMMerge("sort_desc("+fallback+")", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 2 || got[0].Header.Tags["instance"] != "b" {
		t.Fatalf("fallback was selected twice or unsorted: %#v", got)
	}
}

func TestFinalizeTSMMergeLimitKGroupingKeyDoesNotCollide(t *testing.T) {
	const inner = "up"
	ds := varianceFinalizeDataSet(inner,
		varianceFinalizeSeries(dataset.Tags{"a": "x;b=y", "instance": "a"}, inner,
			"1", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"a": "x", "b": "y", "instance": "b"}, inner,
			"2", int64(100)),
	)
	(&Client{}).FinalizeTSMMerge("limitk without (instance) (1, up)", ds)
	if got := len(ds.Results[0].SeriesList); got != 2 {
		t.Fatalf("distinct groups collided: %#v", ds.Results[0].SeriesList)
	}
}

func BenchmarkFinalizeTSMMergeLimitK(b *testing.B) {
	const (
		inner       = "up"
		seriesCount = 500
		pointCount  = 120
	)
	series := make([]*dataset.Series, 0, seriesCount)
	for i := 0; i < seriesCount; i++ {
		points := make(dataset.Points, 0, pointCount)
		for j := 0; j < pointCount; j++ {
			value := strconv.Itoa(i*pointCount + j)
			points = append(points, dataset.Point{
				Epoch: epoch.Epoch(j + 1), Size: len(value) + 32, Values: []any{value},
			})
		}
		series = append(series, &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{
					"instance": fmt.Sprintf("instance-%04d", i), "job": fmt.Sprintf("job-%02d", i%10),
				},
				QueryStatement:  inner,
				ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.String}},
			},
			Points: points,
		})
	}
	base := varianceFinalizeDataSet(inner, series...)
	base.TimeRangeQuery.Step = time.Second
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ds := base.Clone().(*dataset.DataSet)
		(&Client{}).FinalizeTSMMerge("limitk by (job) (20, up)", ds)
	}
}
