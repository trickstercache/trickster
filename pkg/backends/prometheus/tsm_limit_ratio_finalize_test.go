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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestPrometheusLabelsHashCompatibility(t *testing.T) {
	// Reference values come from labels.FromMap(...).Hash() in Prometheus
	// commit 2cf323988931bd586a2ab25160e46bcace9398ae.
	tests := []struct {
		name string
		tags dataset.Tags
		want uint64
	}{
		{"empty", dataset.Tags{}, 17241709254077376921},
		{"up a", dataset.Tags{"__name__": "up", "instance": "a"}, 10139292233165076983},
		{"up b", dataset.Tags{"__name__": "up", "instance": "b"}, 1606270689090314871},
		{"service c", dataset.Tags{"service": "c"}, 9299309263614530225},
		{"service d", dataset.Tags{"service": "d"}, 375245342754498227},
		{"two labels sort by name", dataset.Tags{"foo": "bar", "baz": "qux"}, 9578719315788400658},
		{"254 byte value", dataset.Tags{"long": strings.Repeat("x", 254)}, 447315130497038107},
		{"255 byte value", dataset.Tags{"long": strings.Repeat("x", 255)}, 9328540357359131664},
		{"256 byte value", dataset.Tags{"long": strings.Repeat("x", 256)}, 837123608085968220},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prometheusLabelsHash(tt.tags); got != tt.want {
				t.Fatalf("hash got %d want %d", got, tt.want)
			}
		})
	}
}

func TestLimitRatioSelectHashBoundaries(t *testing.T) {
	for _, hash := range []uint64{0, 1 << 62, 1 << 63, 3 << 62, math.MaxUint64} {
		if limitRatioSelectHash(0, hash) {
			t.Errorf("ratio 0 selected hash %d", hash)
		}
		if !limitRatioSelectHash(-1, hash) {
			t.Errorf("ratio -1 omitted hash %d", hash)
		}
	}
	if !limitRatioSelectHash(1, 0) {
		t.Fatal("ratio 1 omitted the zero offset")
	}
	if limitRatioSelectHash(1, math.MaxUint64) {
		t.Fatal("ratio 1 selected the documented max-hash boundary")
	}

	for _, hash := range []uint64{0, 1 << 62, 1 << 63, 3 << 62, math.MaxUint64} {
		positive := limitRatioSelectHash(0.25, hash)
		negativeComplement := limitRatioSelectHash(-0.75, hash)
		if positive == negativeComplement {
			t.Errorf("hash %d is not in exactly one complementary set", hash)
		}
	}
}

func TestFinalizeTSMMergeLimitRatio(t *testing.T) {
	t.Run("positive and negative ratios are complements", func(t *testing.T) {
		positive := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("b", "2", 100),
		)
		negative := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("b", "2", 100),
		)

		client := &Client{}
		client.FinalizeTSMMerge("limit_ratio(0.5, up)", positive)
		client.FinalizeTSMMerge("limit_ratio(-0.5, up)", negative)

		if got, want := seriesNames(positive), []string{"b"}; !slices.Equal(got, want) {
			t.Fatalf("positive series got %v want %v", got, want)
		}
		if got, want := seriesNames(negative), []string{"a"}; !slices.Equal(got, want) {
			t.Fatalf("negative series got %v want %v", got, want)
		}
	})

	t.Run("grouping forms retain complete-label selection", func(t *testing.T) {
		for _, query := range []string{
			"limit_ratio by (job) (0.5, up)",
			"limit_ratio(0.5, up) without (pod)",
		} {
			ds := rankDataSet(
				rankSeries("a", "1", 100),
				rankSeries("b", "2", 100),
			)
			(&Client{}).FinalizeTSMMerge(query, ds)
			if got, want := seriesNames(ds), []string{"b"}; !slices.Equal(got, want) {
				t.Fatalf("%s series got %v want %v", query, got, want)
			}
		}
	})

	t.Run("range selection keeps every sparse point", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100, "3", 300),
			rankSeries("b", "2", 100, "4", 400),
		)
		ds.TimeRangeQuery = &timeseries.TimeRangeQuery{Step: time.Minute}

		(&Client{}).FinalizeTSMMerge("limit_ratio(0.5, up)", ds)

		if got, want := seriesNames(ds), []string{"b"}; !slices.Equal(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
		if got, want := seriesPointValues(ds)["b"], []string{"2", "4"}; !slices.Equal(got, want) {
			t.Fatalf("points got %v want %v", got, want)
		}
	})

	t.Run("unwrapped ratio preserves floats and native histograms", func(t *testing.T) {
		tags := dataset.Tags{"__name__": "up", "instance": "b"}
		floatSeries := rankSeriesWithTags("float", tags.Clone(), "2", 100)
		histogram := rankSeriesWithTags("histogram", tags.Clone(),
			`{"count":"2","sum":"2.5"}`, 100)
		histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		ds := rankDataSet(floatSeries, histogram)

		(&Client{}).FinalizeTSMMerge("limit_ratio(0.5, up)", ds)

		if got, want := seriesNames(ds), []string{"float", "histogram"}; !slices.Equal(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("edge ratios", func(t *testing.T) {
		for _, tt := range []struct {
			query string
			want  []string
		}{
			{"limit_ratio(0, up)", nil},
			{"limit_ratio(-1, up)", []string{"a", "b"}},
			{"limit_ratio(1, up)", []string{"a", "b"}},
		} {
			ds := rankDataSet(rankSeries("a", "1", 100), rankSeries("b", "2", 100))
			(&Client{}).FinalizeTSMMerge(tt.query, ds)
			if got := seriesNames(ds); !slices.Equal(got, tt.want) {
				t.Fatalf("%s series got %v want %v", tt.query, got, tt.want)
			}
		}
	})

	t.Run("sort wrapper orders floats and omits histograms", func(t *testing.T) {
		histogram := rankSeries("histogram", `{"count":"2","sum":"2.5"}`, 100)
		histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		ds := rankDataSet(
			rankSeries("a", "3", 100),
			histogram,
			rankSeries("b", "1", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort(limit_ratio(-1, up))", ds)

		if got, want := seriesNames(ds), []string{"b", "a"}; !slices.Equal(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("range sort wrapper preserves order and warns once", func(t *testing.T) {
		histogram := rankSeries("histogram", `{"count":"2","sum":"2.5"}`, 100)
		histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		ds := rankDataSet(
			rankSeries("a", "3", 100),
			histogram,
			rankSeries("b", "1", 100),
		)
		ds.TimeRangeQuery = &timeseries.TimeRangeQuery{Step: time.Minute}

		client := &Client{}
		client.FinalizeTSMMerge("sort_desc(limit_ratio(-1, up))", ds)
		client.FinalizeTSMMerge("sort_desc(limit_ratio(-1, up))", ds)

		if got, want := seriesNames(ds), []string{"a", "b"}; !slices.Equal(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
		if got, want := ds.Warnings, []string{sortInRangeQueryWarning}; !slices.Equal(got, want) {
			t.Fatalf("warnings got %v want %v", got, want)
		}
	})
}

func TestLimitRatioSelectionDistributesAcrossUnequalShards(t *testing.T) {
	combined := rankDataSet(
		rankSeries("a", "1", 100),
		rankSeries("b", "2", 100),
		rankSeries("c", "3", 100),
		rankSeries("d", "4", 100),
	)
	shardA := rankDataSet(rankSeries("a", "1", 100))
	shardB := rankDataSet(
		rankSeries("b", "2", 100),
		rankSeries("c", "3", 100),
		rankSeries("d", "4", 100),
	)

	client := &Client{}
	for _, ds := range []*dataset.DataSet{combined, shardA, shardB} {
		client.FinalizeTSMMerge("limit_ratio(0.5, up)", ds)
	}
	want := seriesNames(combined)
	got := append(seriesNames(shardB), seriesNames(shardA)...)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("shard union got %v want global selection %v", got, want)
	}
}
