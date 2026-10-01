/*
 * Copyright 2026 The Trickster Authors
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
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// queries over x whose finalizers read every series' values
var finalizedQueries = []string{
	"topk(2, x)", "bottomk(1, x)", "topk by (job) (1, x)", "sort_desc(topk(3, x))", "sort(bottomk(2, x))",
	"limitk(2, x)", "limitk by (job) (1, x)", "sort_desc(limitk(3, x))",
	"limit_ratio(0.5, x)", "limit_ratio(-0.3, x)",
	"quantile(0.9, x)", "quantile by (job) (0.5, x)", "quantile(2, x)", "sort(quantile without (cpu) (0.25, x))",
	"stddev(x)", "stdvar by (job) (x)", "sort_desc(stddev without (cpu) (x))",
	"sum(x) * 2", "2 - sum(x)", "sum(x) > 1", "sum by (job) (x) <= bool 1", "time() - sum(x)",
	"sort(sum(x))", "sort_desc(sum by (job) (x))",
}

// finalizerDataSet returns random series of x across a few jobs and cpus, with gaps, values that don't
// parse, NaN and infinities, a histogram, and, for a range query, many times
func finalizerDataSet(rng *weaktest.Rand, rangeQuery bool) *dataset.DataSet {
	// values of other kinds are ignored, or read as numbers, as each finalizer reads them
	values := []any{"1", "2.5", "-3", "0", "NaN", "+Inf", "-Inf", "x", "1e300", "7", 2.5, float32(1.5),
		json.Number("3"), int64(4)}
	times := 1
	if rangeQuery {
		times = 2 + rng.IntN(8)
	}
	var list dataset.SeriesList
	for s := range 2 + rng.IntN(8) {
		tags := dataset.Tags{"__name__": "x", "job": "j" + strconv.Itoa(rng.IntN(3)), "cpu": strconv.Itoa(s % 4)}
		var pts dataset.Points
		for at := range times {
			if rangeQuery && rng.IntN(4) == 0 {
				continue
			}
			pts = append(pts, dataset.Point{Epoch: epoch.Epoch(int64(1000+at*60) * 1e9),
				Values: []any{values[rng.IntN(len(values))]}})
		}
		header := dataset.SeriesHeader{Name: "x", Tags: tags,
			ValueFieldsList: timeseries.FieldDefinitions{{Name: "value", DataType: timeseries.String}}}
		if rng.IntN(10) == 0 {
			header.ValueFieldsList[0].Name = histogramFieldName
			for i := range pts {
				pts[i].Values[0] = `{"count":"2","sum":"4"}`
			}
		}
		list = append(list, dataset.NewSeries(header, pts))
	}
	if rng.IntN(4) == 0 {
		// a series held twice, as replicas' contributions can be, and one without rows
		list = append(list, list[0].Clone(), dataset.NewSeries(list[1].Header, nil))
	}
	if rng.IntN(3) == 0 && list[0].PointCount() > 1 {
		// a series in parts, the first empty and the last narrower, as merged ones can be held
		pts := dspoints.Of(list[0])
		half := len(pts) / 2
		head := dataset.NewSeries(list[0].Header, pts[:half]).Segments()
		for i := range pts[half:] {
			pts[half+i].Values = append(pts[half+i].Values, "extra")
		}
		wide := dataset.NewSeries(list[0].Header, pts[half:]).Segments()
		list[0] = dataset.NewSeriesOf(list[0].Header, append(dataset.Segments{{}}, append(head, wide...)...))
	}
	trq := &timeseries.TimeRangeQuery{Statement: "x"}
	if rangeQuery {
		trq.Step = time.Minute
	}
	return &dataset.DataSet{TimeRangeQuery: trq, Results: dataset.Results{{SeriesList: list}}}
}

// requireLegacyFinalized finalizes ds both ways and requires the same result, reporting whether the
// finalizer changed anything
func requireLegacyFinalized(t *testing.T, query string, ds *dataset.DataSet) bool {
	t.Helper()
	want, got := ds.Clone().(*dataset.DataSet), ds.Clone().(*dataset.DataSet)
	(&Client{}).legacyFinalizeTSMMergeEntry(query, want)
	(&Client{}).FinalizeTSMMerge(query, got)
	changed := fmt.Sprint(want) != fmt.Sprint(ds) || len(want.Warnings) > 0
	require.Equal(t, want.Warnings, got.Warnings, query)
	require.Equal(t, len(want.Results), len(got.Results), query)
	for r := range want.Results {
		require.Equal(t, len(want.Results[r].SeriesList), len(got.Results[r].SeriesList), query)
		for s, ws := range want.Results[r].SeriesList {
			gs := got.Results[r].SeriesList[s]
			require.Equal(t, ws.Header.Name, gs.Header.Name, query)
			require.Equal(t, ws.Header.Tags, gs.Header.Tags, query)
			require.Equal(t, ws.Header.QueryStatement, gs.Header.QueryStatement, query)
			require.Equal(t, ws.Header.CalculateHash(), gs.Header.CalculateHash(), query)
			// printed, as a NaN isn't equal to itself
			require.Equal(t, fmt.Sprint(dspoints.Of(ws)), fmt.Sprint(dspoints.Of(gs)), "%s: series %d", query, s)
		}
	}
	require.NoError(t, streamtest.Compare(want, got, streamtest.CompareOptions{IgnoreSizes: true}), query)
	return changed
}

func TestFinalizersMatchLegacy(t *testing.T) {
	rng := weaktest.NewRand(61, 61)
	changed := make(map[string]int)
	for range 60 {
		for _, rangeQuery := range []bool{false, true} {
			ds := finalizerDataSet(rng, rangeQuery)
			for _, query := range finalizedQueries {
				if requireLegacyFinalized(t, query, ds) {
					changed[query]++
				}
			}
		}
	}
	// every query's finalizer changes most of its inputs, so each comparison tests one
	for _, query := range finalizedQueries {
		require.Greater(t, changed[query], 30, query)
	}
}

func TestPooledVarianceMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(62, 62)
	states := []any{
		dataset.PooledVarianceState{Count: 5, Mean: 5, M2: 40}, dataset.PooledVarianceState{Count: 1, Mean: -3},
		dataset.PooledVarianceState{Count: 0}, dataset.PooledVarianceState{Count: math.NaN()},
		dataset.PooledVarianceState{Count: math.Inf(1)}, "1",
	}
	for range 40 {
		var list dataset.SeriesList
		for s := range 1 + rng.IntN(5) {
			var pts dataset.Points
			for at := range 1 + rng.IntN(6) {
				pts = append(pts, dataset.Point{Epoch: epoch.Epoch(int64(at) * 1e9),
					Values: []any{states[rng.IntN(len(states))], "extra"}})
			}
			list = append(list, dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"job": strconv.Itoa(s)}}, pts))
		}
		// a series whose rows hold no values
		list = append(list, dataset.NewSeries(dataset.SeriesHeader{}, dataset.Points{{Epoch: 1}}))
		for _, query := range []string{"stddev by (job) (x)", "stdvar(x)"} {
			_ = requireLegacyFinalized(t, query, &dataset.DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: "x"},
				Results: dataset.Results{{SeriesList: list}}})
		}
	}
}

func BenchmarkFinalizers(b *testing.B) {
	rng := weaktest.NewRand(63, 63)
	var list dataset.SeriesList
	for s := range 200 {
		pts := make(dataset.Points, 500)
		for at := range pts {
			pts[at] = dataset.Point{Epoch: epoch.Epoch(int64(at*60) * 1e9),
				Values: []any{strconv.FormatFloat(rng.NormFloat64()*100, 'f', -1, 64)}}
		}
		list = append(list, dataset.NewSeries(dataset.SeriesHeader{Name: "x", Tags: dataset.Tags{"__name__": "x",
			"job": "j" + strconv.Itoa(s%5), "cpu": strconv.Itoa(s)}}, pts))
	}
	base := &dataset.DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: "x", Step: time.Minute},
		Results: dataset.Results{{SeriesList: list}}}
	for _, query := range []string{"topk(5, x)", "limitk(5, x)", "quantile by (job) (0.9, x)", "stddev by (job) (x)",
		"sum(x) * 2"} {
		for name, finalize := range map[string]func(*Client, string, timeseries.Timeseries){
			"legacy": (*Client).legacyFinalizeTSMMergeEntry, "columnar": (*Client).FinalizeTSMMerge,
		} {
			b.Run(query+"/"+name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					b.StopTimer()
					ds := base.Clone().(*dataset.DataSet)
					b.StartTimer()
					finalize(&Client{}, query, ds)
				}
			})
		}
	}
}
