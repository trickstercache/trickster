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
}

func TestRankCandidateHeapMatchesFullSort(t *testing.T) {
	values := []float64{4, 1, math.NaN(), 9, 9, -2, math.Inf(1), math.Inf(-1), 4, 7}
	candidates := make([]rankCandidate, len(values))
	for i, value := range values {
		candidates[i] = rankCandidate{
			series: &dataset.Series{Header: dataset.SeriesHeader{Tags: dataset.Tags{
				"rank": []string{"c", "a", "nan", "b", "a", "z", "inf", "ninf", "c", "d"}[i],
			}}},
			value: value,
			order: i,
		}
	}

	for _, operator := range []string{rankOperatorTopK, rankOperatorBottomK} {
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
			Size:   32,
			Values: []any{valuesAndEpochs[i].(string)},
		})
	}
	return &dataset.Series{
		Header: dataset.SeriesHeader{Name: name, Tags: tags},
		Points: points,
	}
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
		for _, point := range series.Points {
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
	for range b.N {
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
