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
	"cmp"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestFinalizeTSMMergeRankAggregation(t *testing.T) {
	t.Run("topk trims merged instant vector", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("c", "3", 100),
			rankSeries("b", "4", 100),
			rankSeries("d", "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("topk(2, up)", ds)

		got := seriesNames(ds)
		want := []string{"b", "c"}
		if !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("sort wrapper orders finalized instant vector", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("c", "3", 100),
			rankSeries("b", "4", 100),
			rankSeries("d", "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort(topk(2, up))", ds)

		got := seriesNames(ds)
		want := []string{"c", "b"}
		if !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("bottomk trims merged instant vector", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("b", "4", 100),
			rankSeries("c", "3", 100),
			rankSeries("d", "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("bottomk(2, up)", ds)

		got := seriesNames(ds)
		want := []string{"a", "d"}
		if !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("topk filters each range timestamp", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100, "10", 200),
			rankSeries("b", "2", 100, "9", 200),
			rankSeries("c", "3", 100, "8", 200),
		)

		(&Client{}).FinalizeTSMMerge("topk(2, up)", ds)

		got := seriesPointValues(ds)
		want := map[string][]string{
			"a": {"10"},
			"b": {"2", "9"},
			"c": {"3"},
		}
		if !equalStringSlicesByKey(got, want) {
			t.Fatalf("points got %v want %v", got, want)
		}
	})

	t.Run("topk groups by label", func(t *testing.T) {
		ds := rankDataSet(
			rankSeriesWithTags("a", dataset.Tags{"job": "api", "instance": "a"}, "1", 100),
			rankSeriesWithTags("b", dataset.Tags{"job": "api", "instance": "b"}, "4", 100),
			rankSeriesWithTags("c", dataset.Tags{"job": "worker", "instance": "c"}, "3", 100),
			rankSeriesWithTags("d", dataset.Tags{"job": "worker", "instance": "d"}, "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("topk by (job) (1, up)", ds)

		got := seriesNames(ds)
		want := []string{"b", "c"}
		if !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("sort wrapper warns once for range query", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100, "3", 200),
			rankSeries("b", "2", 100, "4", 200),
		)
		ds.TimeRangeQuery = &timeseries.TimeRangeQuery{Step: time.Minute}
		ds.Warnings = []string{"existing warning"}

		client := &Client{}
		client.FinalizeTSMMerge("sort(topk(1, up))", ds)
		client.FinalizeTSMMerge("sort(topk(1, up))", ds)

		want := []string{"existing warning", sortInRangeQueryWarning}
		if !slices.Equal(ds.Warnings, want) {
			t.Fatalf("warnings got %v want %v", ds.Warnings, want)
		}
	})

	t.Run("unwrapped range query does not add sort warning", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100, "3", 200),
			rankSeries("b", "2", 100, "4", 200),
		)
		ds.TimeRangeQuery = &timeseries.TimeRangeQuery{Step: time.Minute}

		(&Client{}).FinalizeTSMMerge("topk(1, up)", ds)

		if slices.Contains(ds.Warnings, sortInRangeQueryWarning) {
			t.Fatalf("unexpected sort warning: %v", ds.Warnings)
		}
	})
}

func TestFinalizeTSMMergeRankGroupingIgnoresMetricName(t *testing.T) {
	const selector = `{__name__=~"a|b"}`
	makeDataSet := func() *dataset.DataSet {
		return rankDataSet(
			rankSeriesWithTags("a-i1", dataset.Tags{"__name__": "a", "instance": "i1", "job": "x"}, "1", 100),
			rankSeriesWithTags("a-i2", dataset.Tags{"__name__": "a", "instance": "i2", "job": "x"}, "4", 100),
			rankSeriesWithTags("b-i1", dataset.Tags{"__name__": "b", "instance": "i1", "job": "x"}, "3", 100),
			rankSeriesWithTags("b-i2", dataset.Tags{"__name__": "b", "instance": "i2", "job": "x"}, "2", 100),
		)
	}
	// each instance is one group, so K=1 keeps one series of the two metrics per instance
	for _, grouping := range []string{"without (job)", "without ()", "without (__name__)", "by (instance)"} {
		for operator, want := range map[string][]string{
			aggregation.TopK:    {"a-i2", "b-i1"},
			aggregation.BottomK: {"a-i1", "b-i2"},
		} {
			query := operator + " " + grouping + " (1, " + selector + ")"
			t.Run(query, func(t *testing.T) {
				ds := makeDataSet()
				(&Client{}).FinalizeTSMMerge(query, ds)
				if got := seriesNames(ds); !equalStrings(got, want) {
					t.Fatalf("series got %v want %v", got, want)
				}
				for _, series := range ds.Results[0].SeriesList {
					if len(series.Header.Tags) != 3 || series.Header.Tags["__name__"] == "" ||
						series.Header.Tags["job"] != "x" {
						t.Fatalf("selected labels were changed: %v", series.Header.Tags)
					}
				}
			})
		}
	}
}

func TestRankGroups(t *testing.T) {
	tags := dataset.Tags{"__name__": "up", "instance": "a", "job": "api"}
	t.Run("ungrouped", func(t *testing.T) {
		var groups rankGroups
		if id := groups.id(tags); id != 0 || groups.ids != nil || groups.keyBuf != nil {
			t.Fatalf("id %d keys %v buffer %q", id, groups.ids, groups.keyBuf)
		}
	})
	for _, tt := range []struct {
		grouping promql.AggregationGrouping
		want     string
	}{
		{grouping: promql.AggregationGrouping{Without: true}, want: `{"instance":"a","job":"api"}`},
		{grouping: promql.AggregationGrouping{Labels: []string{"job"}, Without: true}, want: `{"instance":"a"}`},
		{
			grouping: promql.AggregationGrouping{Labels: []string{"__name__"}, Without: true},
			want:     `{"instance":"a","job":"api"}`,
		},
		{grouping: promql.AggregationGrouping{Labels: []string{"__name__", "job"}}, want: `{"__name__":"up","job":"api"}`},
	} {
		t.Run(tt.want, func(t *testing.T) {
			groups := rankGroups{grouping: tt.grouping}
			id := groups.id(tags)
			if _, ok := groups.ids[tt.want]; !ok || len(groups.ids) != 1 || id != 0 {
				t.Fatalf("id %d keys %v want key %s", id, groups.ids, tt.want)
			}
			// a group seen before is found without allocating, as its key is built in reused scratch space
			if n := testing.AllocsPerRun(100, func() {
				id = groups.id(tags)
			}); n != 0 || id != 0 || len(groups.ids) != 1 {
				t.Fatalf("%v allocations, id %d, keys %v", n, id, groups.ids)
			}
		})
	}
	t.Run("distinct groups", func(t *testing.T) {
		groups := rankGroups{grouping: promql.AggregationGrouping{Labels: []string{"instance"}, Without: true}}
		got := []int{
			groups.id(dataset.Tags{"__name__": "a", "instance": "i1", "job": "x"}),
			groups.id(dataset.Tags{"__name__": "b", "instance": "i1", "job": "y"}),
			groups.id(dataset.Tags{"__name__": "b", "instance": "i2", "job": "x"}),
			groups.id(dataset.Tags{"job": "y"}),
		}
		if want := []int{0, 1, 0, 1}; !slices.Equal(got, want) {
			t.Fatalf("ids got %v want %v", got, want)
		}
	})
}

func TestRankGroupingAllocations(t *testing.T) {
	const (
		runs        = 10
		fewerSeries = 200
		moreSeries  = 2 * fewerSeries
		margin      = 0.5
		groupCount  = 4
		inner       = "up"
		limit       = " (5, " + inner + ")"
	)
	makeDataSet := func(seriesCount int) *dataset.DataSet {
		list := make(dataset.SeriesList, seriesCount)
		for i := range list {
			id := strconv.Itoa(i)
			tags := dataset.Tags{
				"__name__": inner, "instance": "instance-" + id, "pod": "pod-" + id,
				"job": "job-" + strconv.Itoa(i%groupCount), "region": "us-east-1", "zone": "us-east-1a",
			}
			// distinct values leave no ties, whose label comparisons would add allocations unrelated to grouping
			list[i] = rankSeriesWithTags(id, tags, strconv.Itoa(2*i), 100, strconv.Itoa(2*i+1), 160)
		}
		return varianceFinalizeDataSet(inner, list...)
	}
	allocsPerSeries := func(query string) float64 {
		measure := func(seriesCount int) float64 {
			// AllocsPerRun calls the function runs+1 times, and each call needs a dataset not yet finalized
			sets := make([]*dataset.DataSet, runs+1)
			for i := range sets {
				sets[i] = makeDataSet(seriesCount)
			}
			var next int
			return testing.AllocsPerRun(runs, func() {
				(&Client{}).FinalizeTSMMerge(query, sets[next])
				next++
			})
		}
		// the difference between two series counts leaves out allocations that do not grow with the series
		return (measure(moreSeries) - measure(fewerSeries)) / (moreSeries - fewerSeries)
	}
	for _, operator := range []string{aggregation.TopK, aggregation.LimitK} {
		t.Run(operator, func(t *testing.T) {
			// the series share a few groups, so after the first series of a group, its key costs nothing
			ungrouped := allocsPerSeries(operator + limit)
			grouped := allocsPerSeries(operator + " without (instance, pod)" + limit)
			if grouped > ungrouped+margin {
				t.Fatalf("%.2f allocations per series grouped, %.2f ungrouped", grouped, ungrouped)
			}
		})
	}
}

func TestFinalizeTSMMergeSortWrapper(t *testing.T) {
	t.Run("sort orders merged instant vector ascending", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "3", 100),
			rankSeries("b", "1", 100),
			rankSeries("c", "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort(sum(up))", ds)

		if got, want := seriesNames(ds), []string{"b", "c", "a"}; !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("sort_desc orders merged instant vector descending", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("b", "3", 100),
			rankSeries("c", "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort_desc(count(up))", ds)

		if got, want := seriesNames(ds), []string{"b", "c", "a"}; !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("sort_desc orders merged zero fallback descending", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "1", 100),
			rankSeries("b", "3", 100),
			rankSeries("c", "2", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort_desc(count(up) or vector(0))", ds)

		if got, want := seriesNames(ds), []string{"b", "c", "a"}; !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("native histograms are omitted", func(t *testing.T) {
		histogram := rankSeries("histogram", `{"count":"2","sum":"2.5"}`, 100)
		histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		ds := rankDataSet(
			histogram,
			rankSeries("high", "4", 100),
			rankSeries("low", "1", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort(sum(up))", ds)

		if got, want := seriesNames(ds), []string{"low", "high"}; !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})

	t.Run("range vector omits histograms, preserves order, and warns once", func(t *testing.T) {
		histogram := rankSeries(
			"histogram",
			`{"count":"2","sum":"2.5"}`, 100,
			`{"count":"3","sum":"4.5"}`, 200,
		)
		histogram.Header.ValueFieldsList = timeseries.FieldDefinitions{{Name: histogramFieldName}}
		ds := rankDataSet(
			histogram,
			rankSeries("a", "3", 100, "4", 200),
			rankSeries("b", "1", 100, "2", 200),
		)
		ds.TimeRangeQuery = &timeseries.TimeRangeQuery{Step: time.Minute}
		ds.Warnings = []string{"existing warning"}

		client := &Client{}
		client.FinalizeTSMMerge("sort(sum(up))", ds)
		client.FinalizeTSMMerge("sort(sum(up))", ds)

		if got, want := seriesNames(ds), []string{"a", "b"}; !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
		wantWarnings := []string{"existing warning", sortInRangeQueryWarning}
		if !slices.Equal(ds.Warnings, wantWarnings) {
			t.Fatalf("warnings got %v want %v", ds.Warnings, wantWarnings)
		}
	})

	t.Run("non-aggregation sort is not finalized", func(t *testing.T) {
		ds := rankDataSet(
			rankSeries("a", "3", 100),
			rankSeries("b", "1", 100),
		)

		(&Client{}).FinalizeTSMMerge("sort(up)", ds)

		if got, want := seriesNames(ds), []string{"a", "b"}; !equalStrings(got, want) {
			t.Fatalf("series got %v want %v", got, want)
		}
	})
}

func TestRankCandidateHeapMatchesFullSort(t *testing.T) {
	values := []float64{4, 1, math.NaN(), 9, 9, -2, math.Inf(1), math.Inf(-1), 4, 7}
	candidates := make([]rankCandidate, len(values))
	for i, value := range values {
		candidates[i] = rankCandidate{
			series: dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{
				"rank": []string{"c", "a", "nan", "b", "a", "z", "inf", "ninf", "c", "d"}[i],
			}}, nil),
			value: value,
			order: i,
		}
	}

	for _, operator := range []string{aggregation.TopK, aggregation.BottomK} {
		for _, limit := range []int{0, 1, 3, len(candidates), len(candidates) + 2} {
			name := operator + "/" + strconv.Itoa(limit)
			t.Run(name, func(t *testing.T) {
				wantCandidates := append([]rankCandidate(nil), candidates...)
				sortRankCandidatesReference(wantCandidates, operator)
				wantCandidates = wantCandidates[:min(limit, len(wantCandidates))]

				h := &rankCandidateHeap{operator: operator}
				for _, candidate := range candidates {
					h.consider(candidate, limit)
				}
				gotCandidates := append([]rankCandidate(nil), h.items...)
				sortRankCandidatesReference(gotCandidates, operator)

				orders := func(input []rankCandidate) []int {
					out := make([]int, len(input))
					for i := range input {
						out[i] = input[i].order
					}
					return out
				}
				if got, want := orders(gotCandidates), orders(wantCandidates); !reflect.DeepEqual(got, want) {
					t.Fatalf("selected orders got %v want %v", got, want)
				}
			})
		}
	}
}

func sortRankCandidatesReference(candidates []rankCandidate, operator string) {
	slices.SortStableFunc(candidates, func(a, b rankCandidate) int {
		if c := compareRankCandidateValues(a, b, operator); c != 0 {
			return c
		}
		if c := strings.Compare(a.series.Header.Tags.JSON(), b.series.Header.Tags.JSON()); c != 0 {
			return c
		}
		return cmp.Compare(a.order, b.order)
	})
}

func rankDataSet(series ...*dataset.Series) *dataset.DataSet {
	return &dataset.DataSet{
		Results: dataset.Results{{
			SeriesList: series,
		}},
	}
}

func rankSeries(name string, valuesAndEpochs ...any) *dataset.Series {
	return rankSeriesWithTags(name, dataset.Tags{"__name__": "up", "instance": name}, valuesAndEpochs...)
}

func rankSeriesWithTags(name string, tags dataset.Tags, valuesAndEpochs ...any) *dataset.Series {
	points := make(dataset.Points, 0, len(valuesAndEpochs)/2)
	for i := 0; i < len(valuesAndEpochs); i += 2 {
		points = append(points, dataset.Point{
			Epoch:  epoch.Epoch(valuesAndEpochs[i+1].(int) * 1e9),
			Values: []any{valuesAndEpochs[i].(string)},
		})
	}
	return dataset.NewSeries(dataset.SeriesHeader{Name: name, Tags: tags}, points)
}

func seriesNames(ds *dataset.DataSet) []string {
	if ds == nil || len(ds.Results) == 0 {
		return nil
	}
	names := make([]string, 0, len(ds.Results[0].SeriesList))
	for _, series := range ds.Results[0].SeriesList {
		if series != nil {
			names = append(names, series.Header.Name)
		}
	}
	return names
}

func seriesPointValues(ds *dataset.DataSet) map[string][]string {
	out := make(map[string][]string)
	if ds == nil || len(ds.Results) == 0 {
		return out
	}
	for _, series := range ds.Results[0].SeriesList {
		if series == nil {
			continue
		}
		for _, point := range dspoints.Of(series) {
			if len(point.Values) > 0 {
				out[series.Header.Name] = append(out[series.Header.Name], point.Values[0].(string))
			}
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStringSlicesByKey(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || !equalStrings(av, bv) {
			return false
		}
	}
	return true
}

func BenchmarkFinalizeTSMMergeTopK(b *testing.B) {
	for b.Loop() {
		b.StopTimer()
		series := make([]*dataset.Series, 64_000)
		for i := range series {
			name := strconv.Itoa(i)
			series[i] = rankSeries(name, strconv.Itoa(i), 100)
		}
		ds := rankDataSet(series...)
		b.StartTimer()
		(&Client{}).FinalizeTSMMerge("sort_desc(topk(20, up))", ds)
	}
}
