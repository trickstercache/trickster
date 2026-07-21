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
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestPrometheusValueQuantile(t *testing.T) {
	tests := []struct {
		name   string
		phi    float64
		values []float64
		want   float64
	}{
		{name: "empty", phi: 0.5, want: math.NaN()},
		{name: "nan parameter", phi: math.NaN(), values: []float64{1}, want: math.NaN()},
		{name: "below range", phi: -0.1, values: []float64{1}, want: math.Inf(-1)},
		{name: "above range", phi: 1.1, values: []float64{1}, want: math.Inf(1)},
		{name: "singleton", phi: 0.5, values: []float64{7}, want: 7},
		{name: "odd", phi: 0.5, values: []float64{5, 1, 3}, want: 3},
		{name: "even interpolation", phi: 0.5, values: []float64{4, 1, 3, 2}, want: 2.5},
		{name: "upper interpolation", phi: 0.9, values: []float64{0, 10, 20, 30},
			want: 27.000000000000004},
		{name: "duplicates", phi: 0.75, values: []float64{1, 1, 1, 5}, want: 2},
		{name: "nan sorts first", phi: 0.5, values: []float64{2, math.NaN(), 1}, want: 1},
		{name: "negative infinity interpolation", phi: 0.25,
			values: []float64{0, math.Inf(1), math.Inf(-1)}, want: math.Inf(-1)},
		{name: "positive infinity interpolation", phi: 0.75,
			values: []float64{0, math.Inf(1), math.Inf(-1)}, want: math.Inf(1)},
		{name: "infinity zero-weight follows IEEE arithmetic", phi: 0.5,
			values: []float64{0, math.Inf(1), math.Inf(-1)}, want: math.NaN()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := append([]float64(nil), tt.values...)
			got := prometheusValueQuantile(tt.phi, values)
			if math.IsNaN(tt.want) {
				if !math.IsNaN(got) {
					t.Fatalf("got %v want NaN", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestFinalizeTSMMergeQuantileGlobalGroupsAndSparseRange(t *testing.T) {
	const inner = "rate(requests[5m])"
	series := []*dataset.Series{
		varianceFinalizeSeries(dataset.Tags{"__name__": "requests", "instance": "a", "region": "east"},
			inner, "1", int64(100), "10", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "requests", "instance": "b", "region": "east"},
			inner, "3", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "requests", "instance": "c", "region": "east"},
			inner, "100", int64(100), "30", int64(200)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "requests", "instance": "a", "region": "west"},
			inner, "1000", int64(100)),
		varianceFinalizeSeries(dataset.Tags{"__name__": "requests", "instance": "b", "region": "west"},
			inner, "2000", int64(100)),
	}
	histogram := varianceFinalizeSeries(dataset.Tags{
		"__name__": "requests", "instance": "hist", "region": "east",
	}, inner, `{"count":"100","sum":"1000"}`, int64(100))
	histogram.Header.ValueFieldsList[0].Name = histogramFieldName
	series = append(series, histogram)
	ds := varianceFinalizeDataSet(inner, series...)
	ds.TimeRangeQuery.Step = time.Minute

	(&Client{}).FinalizeTSMMerge("quantile by (region) (0.5, "+inner+")", ds)
	got := ds.Results[0].SeriesList
	if len(got) != 2 {
		t.Fatalf("series count got %d want 2: %#v", len(got), got)
	}
	east := quantileSeriesByLabel(t, got, "region", "east")
	west := quantileSeriesByLabel(t, got, "region", "west")
	if east.Header.Tags.JSON() != `{"region":"east"}` || east.Header.Name != "" ||
		east.Header.QueryStatement != "quantile by (region) (0.5, "+inner+")" {
		t.Fatalf("east header: %#v", east.Header)
	}
	if len(east.Points) != 2 || east.Points[0].Epoch != 100 || east.Points[1].Epoch != 200 ||
		east.Points[0].Values[0] != "3" || east.Points[1].Values[0] != "20" {
		t.Fatalf("east points: %#v", east.Points)
	}
	if len(west.Points) != 1 || west.Points[0].Values[0] != "1500" {
		t.Fatalf("west points: %#v", west.Points)
	}
}

func TestFinalizeTSMMergeQuantileGroupingMetadataAndMetricName(t *testing.T) {
	const inner = "count without (shard) (requests)"
	makeSeries := func(name, instance, region, value string) *dataset.Series {
		return varianceFinalizeSeries(dataset.Tags{
			"__name__": name, "instance": instance, "region": region,
			"__type__": "gauge", "__unit__": "requests",
		}, inner, value, int64(100))
	}

	t.Run("without removes metric name and excluded labels", func(t *testing.T) {
		ds := varianceFinalizeDataSet(inner,
			makeSeries("requests", "a", "east", "1"),
			makeSeries("requests", "b", "east", "5"),
		)
		(&Client{}).FinalizeTSMMerge("quantile without (instance) (0.5, "+inner+")", ds)
		got := ds.Results[0].SeriesList
		if len(got) != 1 || got[0].Header.Tags.JSON() !=
			`{"__type__":"gauge","__unit__":"requests","region":"east"}` ||
			got[0].Points[0].Values[0] != "3" {
			t.Fatalf("result: %#v", got)
		}
	})

	t.Run("by metric name retains it", func(t *testing.T) {
		ds := varianceFinalizeDataSet(inner,
			makeSeries("requests", "a", "east", "1"),
			makeSeries("requests", "b", "west", "3"),
		)
		(&Client{}).FinalizeTSMMerge("quantile by (__name__) (0.5, "+inner+")", ds)
		got := ds.Results[0].SeriesList
		if len(got) != 1 || got[0].Header.Tags.JSON() != `{"__name__":"requests"}` ||
			got[0].Header.Name != "requests" || got[0].Points[0].Values[0] != "2" {
			t.Fatalf("result: %#v", got)
		}
	})
}

func TestFinalizeTSMMergeQuantileSpecialParametersAndIdempotence(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"quantile(NaN, up)", "NaN"},
		{"quantile(-0.1, up)", "-Inf"},
		{"quantile(1.1, up)", "+Inf"},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			ds := varianceFinalizeDataSet("up",
				varianceFinalizeSeries(dataset.Tags{"instance": "a"}, "up", "7", int64(100)),
			)
			client := &Client{}
			client.FinalizeTSMMerge(tt.query, ds)
			client.FinalizeTSMMerge(tt.query, ds)
			got := ds.Results[0].SeriesList
			if len(got) != 1 || len(got[0].Points) != 1 || got[0].Points[0].Values[0] != tt.want {
				t.Fatalf("result: %#v", got)
			}
			if len(ds.Warnings) != 1 || ds.Warnings[0] != invalidQuantileParameterWarning {
				t.Fatalf("warnings: %v", ds.Warnings)
			}
		})
	}
}

func TestFinalizeTSMMergeQuantileSortAndFallback(t *testing.T) {
	t.Run("sorts finalized instant vector", func(t *testing.T) {
		const inner = "up"
		ds := varianceFinalizeDataSet(inner,
			varianceFinalizeSeries(dataset.Tags{"job": "small", "instance": "a"}, inner, "1", int64(100)),
			varianceFinalizeSeries(dataset.Tags{"job": "small", "instance": "b"}, inner, "3", int64(100)),
			varianceFinalizeSeries(dataset.Tags{"job": "large", "instance": "a"}, inner, "10", int64(100)),
			varianceFinalizeSeries(dataset.Tags{"job": "large", "instance": "b"}, inner, "30", int64(100)),
		)
		(&Client{}).FinalizeTSMMerge("sort_desc(quantile by (job) (0.5, up))", ds)
		got := ds.Results[0].SeriesList
		if len(got) != 2 || got[0].Header.Tags["job"] != "large" ||
			got[1].Header.Tags["job"] != "small" {
			t.Fatalf("sort order: %#v", got)
		}
	})

	t.Run("range sort warning", func(t *testing.T) {
		ds := varianceFinalizeDataSet("up",
			varianceFinalizeSeries(dataset.Tags{"instance": "a"}, "up", "1", int64(100)),
		)
		ds.TimeRangeQuery.Step = time.Minute
		(&Client{}).FinalizeTSMMerge("sort(quantile(0.5, up))", ds)
		if len(ds.Warnings) != 1 || ds.Warnings[0] != sortInRangeQueryWarning {
			t.Fatalf("warnings: %v", ds.Warnings)
		}
	})

	t.Run("unsupported inner expression is not aggregated twice", func(t *testing.T) {
		const fallback = "quantile(0.5, up + down)"
		ds := varianceFinalizeDataSet(fallback,
			varianceFinalizeSeries(dataset.Tags{"instance": "a"}, fallback, "1", int64(100)),
			varianceFinalizeSeries(dataset.Tags{"instance": "b"}, fallback, "3", int64(100)),
		)
		(&Client{}).FinalizeTSMMerge("sort_desc("+fallback+")", ds)
		got := ds.Results[0].SeriesList
		if len(got) != 2 || got[0].Header.Tags["instance"] != "b" {
			t.Fatalf("fallback result: %#v", got)
		}
	})
}

func TestFinalizeTSMMergeQuantileHistogramOnlyGroupIsIgnored(t *testing.T) {
	const inner = "up"
	histogram := varianceFinalizeSeries(dataset.Tags{"job": "api"}, inner,
		`{"count":"1","sum":"2"}`, int64(100))
	histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
	ds := varianceFinalizeDataSet(inner, histogram)
	(&Client{}).FinalizeTSMMerge("quantile by (job) (0.5, up)", ds)
	if len(ds.Results[0].SeriesList) != 0 {
		t.Fatalf("histogram series was retained: %#v", ds.Results[0].SeriesList)
	}
}

func quantileSeriesByLabel(t *testing.T, series dataset.SeriesList, label, value string) *dataset.Series {
	t.Helper()
	for _, candidate := range series {
		if candidate != nil && candidate.Header.Tags[label] == value {
			return candidate
		}
	}
	t.Fatalf("no series with %s=%q in %#v", label, value, series)
	return nil
}

func BenchmarkFinalizeTSMMergeQuantile(b *testing.B) {
	const (
		inner       = "rate(requests[5m])"
		seriesCount = 500
		pointCount  = 120
	)
	series := make([]*dataset.Series, 0, seriesCount)
	for i := 0; i < seriesCount; i++ {
		valuesAndEpochs := make([]any, 0, pointCount*2)
		for j := 0; j < pointCount; j++ {
			valuesAndEpochs = append(valuesAndEpochs,
				strconv.FormatFloat(float64(i*j+1), 'f', -1, 64), int64(j+1))
		}
		series = append(series, varianceFinalizeSeries(dataset.Tags{
			"instance": fmt.Sprintf("instance-%04d", i), "job": fmt.Sprintf("job-%02d", i%10),
		}, inner, valuesAndEpochs...))
	}
	base := varianceFinalizeDataSet(inner, series...)
	base.TimeRangeQuery.Step = time.Second
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ds := base.Clone().(*dataset.DataSet)
		(&Client{}).FinalizeTSMMerge("quantile by (job) (0.9, "+inner+")", ds)
	}
}
