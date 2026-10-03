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

package model

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// nativeBody returns a random response of the query type: a few times, each with a few pages in a random
// order, which is their rank, and a value that's sometimes null or a double
func nativeBody(rng *weaktest.Rand, queryType string) string {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	value := func() string {
		switch rng.IntN(5) {
		case 0:
			return "null"
		case 1:
			return strconv.Itoa(rng.IntN(9)) + ".5"
		}
		return strconv.Itoa(rng.IntN(9))
	}
	var b strings.Builder
	b.WriteByte('[')
	for at := range 2 + rng.IntN(6) {
		if at > 0 {
			b.WriteByte(',')
		}
		ts := `"` + start.Add(time.Duration(at)*time.Minute).Format(druidMillisLayout) + `"`
		pages := rng.Perm(4)[:1+rng.IntN(4)]
		switch queryType {
		case queryTimeseries:
			fmt.Fprintf(&b, `{"timestamp":%s,"result":{"count":%s,"sum":%s}}`, ts, value(), value())
		case queryGroupBy:
			for i, p := range pages {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"version":"v1","timestamp":%s,"event":{"page":"p%d","user":"u%d","count":%s}}`,
					ts, p, p%2, value())
			}
		case queryTopN:
			fmt.Fprintf(&b, `{"timestamp":%s,"result":[`, ts)
			for i, p := range pages {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, `{"page":"p%d","count":%s}`, p, value())
			}
			b.WriteString("]}")
		}
	}
	b.WriteByte(']')
	return b.String()
}

func requireLegacyNative(t *testing.T, ds *dataset.DataSet, plan *QueryPlan) {
	t.Helper()
	for _, descending := range []bool{false, true} {
		p := *plan
		p.descending = descending
		var want, got bytes.Buffer
		werr := legacyNativeMarshal(ds, &p, &want)
		err := MarshalTimeseriesWriter(ds, &timeseries.RequestOptions{ProviderRequest: &p}, 200, &got)
		require.Equal(t, werr, err, "%s descending=%t", p.queryType, descending)
		require.Equal(t, want.String(), got.String(), "%s descending=%t", p.queryType, descending)
	}
}

func TestNativeMarshalMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(31, 31)
	for _, plan := range []*QueryPlan{tsPlan, groupByPlan, topNPlan} {
		for range 40 {
			ts, err := UnmarshalTimeseries([]byte(nativeBody(rng, plan.queryType)), testTRQ(plan))
			require.NoError(t, err)
			ds := ts.(*dataset.DataSet)
			requireLegacyNative(t, ds, plan)
			// a series held in parts, as a cached one can be, reads as one
			requireLegacyNative(t, parts.Of(ds, epochMinute), plan)
		}
	}
}

const epochMinute = 60e9

func TestNativeMarshalMatchesLegacyUnmerged(t *testing.T) {
	rng := weaktest.NewRand(32, 32)
	decode := func(plan *QueryPlan) *dataset.DataSet {
		ts, err := UnmarshalTimeseries([]byte(nativeBody(rng, plan.queryType)), testTRQ(plan))
		require.NoError(t, err)
		return ts.(*dataset.DataSet)
	}
	for _, plan := range []*QueryPlan{tsPlan, groupByPlan, topNPlan} {
		a, b := decode(plan), decode(plan)
		// two results take the sort, as does a series whose rows aren't in time order
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{a.Results[0], nil, b.Results[0]}}, plan)
		s := a.Results[0].SeriesList[0]
		pts := append(dspoints.Of(s), dspoints.Of(s)...)
		for i := range pts[len(pts)/2:] {
			pts[len(pts)/2+i].Epoch += epochMinute * 100
		}
		pts[0], pts[len(pts)-1] = pts[len(pts)-1], pts[0]
		unsorted := append(dataset.SeriesList{nil, dataset.NewSeries(s.Header, nil), dataset.NewSeries(s.Header, pts)},
			a.Results[0].SeriesList[1:]...)
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: unsorted}}}, plan)
		// Segments that share an epoch at their boundary keep their rows' order, as do series of one tags
		p := dspoints.Of(s)
		tied := append(dataset.NewSeries(s.Header, p[:1]).Segments(), dataset.NewSeries(s.Header, p[:1]).Segments()...)
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{
			dataset.NewSeriesOf(s.Header, tied), s, dataset.NewSeries(s.Header, p),
		}}}}, plan)
		// a value that can't be written stops the write before it starts
		nan := dspoints.Of(s)
		nan[len(nan)-1].Values[len(nan[len(nan)-1].Values)-1] = math.NaN()
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: append(dataset.SeriesList{
			dataset.NewSeries(s.Header, nan),
		}, a.Results[0].SeriesList...)}}}, plan)
		requireLegacyNative(t, &dataset.DataSet{}, plan)
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{nil, {}}}, plan)
	}
}

func TestNativeMarshalMatchesLegacyTies(t *testing.T) {
	rng := weaktest.NewRand(33, 33)
	plans := []*QueryPlan{{queryType: queryTimeseries}, {queryType: queryGroupBy}, {queryType: queryTopN}}
	// series without ranks tie at every time, so their tags order them, whatever order they're held in
	ds := benchDruidDataSet(6, 4)
	rng.Shuffle(len(ds.Results[0].SeriesList), func(i, j int) {
		ds.Results[0].SeriesList[i], ds.Results[0].SeriesList[j] = ds.Results[0].SeriesList[j], ds.Results[0].SeriesList[i]
	})
	for _, plan := range plans {
		requireLegacyNative(t, ds, plan)
	}
	// as they do when a series isn't in time order, and they're sorted
	unsorted := slices.Clone(ds.Results[0].SeriesList)
	backward := dspoints.Of(unsorted[0])
	slices.Reverse(backward)
	unsorted[0] = dataset.NewSeries(unsorted[0].Header, backward)
	for _, plan := range plans {
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: unsorted}}}, plan)
	}
	// a series holding one time in two Segments, with ranks that tie, keeps its order
	s := ds.Results[0].SeriesList[0]
	p := dspoints.Of(s)
	other := slices.Clone(p[0].Values)
	other[1] = int64(-1)
	tied := append(dataset.NewSeries(s.Header, p[:1]).Segments(),
		dataset.NewSeries(s.Header, dataset.Points{{Epoch: p[0].Epoch, Values: other}}).Segments()...)
	tiedDS := &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{dataset.NewSeriesOf(s.Header, tied)}}}}
	for _, plan := range plans {
		requireLegacyNative(t, tiedDS, plan)
	}
	// series of one tags keep their order, however many there are
	var same dataset.SeriesList
	for i := range 20 {
		pts := dspoints.Of(s)
		pts[0].Values[1] = int64(i)
		same = append(same, dataset.NewSeries(ds.Results[0].SeriesList[i%2].Header, pts))
	}
	for _, plan := range plans {
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: same}}}, plan)
	}
	// a timeseries doesn't write its dimensions, so it doesn't check them
	nan := dspoints.Of(s)
	nan[0].Values[0] = math.NaN()
	requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{
		dataset.NewSeries(s.Header, nan),
	}}}}, &QueryPlan{queryType: queryTimeseries})
	// a groupBy writes its version, and a row whose version and rank are null writes none
	ts, err := UnmarshalTimeseries([]byte(nativeBody(rng, queryGroupBy)), testTRQ(groupByPlan))
	require.NoError(t, err)
	g := ts.(*dataset.DataSet).Results[0].SeriesList[0]
	version := dspoints.Of(g)
	version[0].Values[2] = math.NaN()
	narrow := dspoints.Of(g)
	for i := range narrow {
		narrow[i].Values = narrow[i].Values[:2]
	}
	for _, series := range []*dataset.Series{dataset.NewSeries(g.Header, version), dataset.NewSeries(g.Header, narrow)} {
		requireLegacyNative(t, &dataset.DataSet{Results: dataset.Results{{SeriesList: dataset.SeriesList{series}}}}, groupByPlan)
	}
}

// the SQL sort's typed comparisons order rows exactly as the boxed ones did, over every kind a value
// column holds and orders that don't start with time
func TestSQLOrderMatchesBoxed(t *testing.T) {
	rng := weaktest.NewRand(34, 34)
	sorted := 0
	for range 400 {
		c := randomSQL(rng)
		ts, err := UnmarshalTimeseries([]byte(c.body), c.trq)
		require.NoError(t, err)
		ds := ts.(*dataset.DataSet)
		if rng.IntN(3) == 0 {
			// values of other kinds, which a merge of other parts can bring
			for _, s := range ds.Results[0].SeriesList {
				pts := dspoints.Of(s)
				for i := range pts {
					pts[i].Values[len(pts[i].Values)-1] = []any{
						1.5, math.NaN(), uint64(7), uint64(3), true, false, "x",
						int64(-2), nil, json.Number("2.5e0"), json.Number("x"),
					}[rng.IntN(11)]
				}
				s.SetPoints(pts)
			}
		}
		ordering := randomOrdering(rng)
		if len(ordering) == 0 || ordering[0].Column == "bucket" {
			ordering = append([]timeseries.OrderTerm{{
				Column: "value", Descending: rng.IntN(2) == 0,
				NullsFirst: rng.IntN(2) == 0,
			}}, ordering...)
		}
		o := newSQLOrder(ds, ordering)
		rows := o.stored()
		want := slices.Clone(rows)
		slices.SortStableFunc(want, func(a, b sqlRow) int {
			if c := legacyCompareSQLTerms(a, b, ordering, 0); c != 0 {
				return c
			}
			return cmp.Compare(a.epoch(), b.epoch())
		})
		sortSQLRows(rows, ordering)
		require.Equal(t, want, rows, "%v", ordering)
		sorted += len(rows)
	}
	require.Greater(t, sorted, 2000)
}

func BenchmarkSQLMarshalValueOrdered(b *testing.B) {
	_, sql := benchBodies(100, 1000)
	ts, err := UnmarshalTimeseries(sql, testSQLTRQ(testSQLPlan()))
	require.NoError(b, err)
	ds := ts.(*dataset.DataSet)
	marker := testSQLPlan()
	for _, ordering := range [][]timeseries.OrderTerm{{{Column: "value"}}, {{Column: "host"}, {Column: "value", Descending: true}}} {
		ds.TimeRangeQuery.Ordering = ordering
		b.Run(ordering[len(ordering)-1].Column+strconv.Itoa(len(ordering)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := marshalSQLTimeseriesWriter(ds, marker, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkNativeMarshalOrdered(b *testing.B) {
	ds := benchDruidDataSet(100, 1000)
	for _, plan := range []*QueryPlan{
		{queryType: queryTimeseries},
		{queryType: queryGroupBy},
		{queryType: queryGroupBy, descending: true},
		{queryType: queryTopN},
	} {
		name := plan.queryType + map[bool]string{true: "-desc"}[plan.descending]
		b.Run(name+"/legacy", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := legacyNativeMarshal(ds, plan, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
		rlo := &timeseries.RequestOptions{ProviderRequest: plan}
		b.Run(name+"/stream", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := MarshalTimeseriesWriter(ds, rlo, 200, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
