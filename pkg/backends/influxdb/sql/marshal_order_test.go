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

package sql

import (
	"bytes"
	"io"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

var orderings = [][]timeseries.OrderTerm{
	nil,
	{{Column: "time"}},
	{{Column: "time", Descending: true}},
	{{Column: "time"}, {Column: "host"}},
	{{Column: "time", Descending: true}, {Column: "v", Descending: true, NullsFirst: true}},
	{{Column: "time"}, {Column: "v", NullsFirst: true}, {Column: "s", Descending: true}},
	{{Column: "time"}, {Column: "missing"}},
	{{Column: "host"}, {Column: "time", Descending: true}},
	{{Column: "v"}},
	{{Column: "time"}, {Column: "b", Descending: true}},
	{{Column: "b"}, {Column: "v", Descending: true}, {Column: "n"}},
}

// orderDataSet decodes random rows of a few hosts, many sharing a time, some sharing one within a host
func orderDataSet(t *testing.T, rng *weaktest.Rand) *dataset.DataSet {
	var b bytes.Buffer
	b.WriteString("time,host,region,v,s,n,b\n")
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 20 + rng.IntN(60) {
		at := start.Add(time.Duration(i/3+rng.IntN(2)) * time.Second).Format(v3TimestampLayouts[0])
		v := strconv.Itoa(rng.IntN(5))
		switch rng.IntN(7) {
		case 0:
			v = ""
		case 1:
			v += ".5"
		case 2:
			// JSON can't write it, so only CSV sorts it
			v = "NaN"
		}
		b.WriteString(at + ",h" + strconv.Itoa(rng.IntN(3)) + ",r" + strconv.Itoa(rng.IntN(2)) + "," + v +
			",s" + strconv.Itoa(rng.IntN(3)) + "," + strconv.Itoa(rng.IntN(100)) + "," +
			[]string{"true", "false", ""}[rng.IntN(3)] + "\n")
	}
	ts, err := UnmarshalTimeseries(b.Bytes(), decoderTRQ())
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func requireLegacyOutput(t *testing.T, ds *dataset.DataSet) {
	t.Helper()
	for _, of := range []byte{iofmt.V3OutputJSON, iofmt.V3OutputJSONL, iofmt.V3OutputCSV} {
		for _, ordering := range orderings {
			ds.TimeRangeQuery.Ordering = ordering
			var got, want bytes.Buffer
			werr := legacyMarshal(&want, ds, of)
			err := MarshalTimeseriesWriter(ds, &timeseries.RequestOptions{OutputFormat: of}, 200, &got)
			require.Equal(t, werr, err, "format %d, ordering %v", of, ordering)
			require.Equal(t, want.String(), got.String(), "format %d, ordering %v", of, ordering)
		}
	}
}

func TestMarshalMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(8, 8)
	for range 40 {
		ds := orderDataSet(t, rng)
		requireLegacyOutput(t, ds)
		// a series held in parts, as a cached one can be, reads as one
		view := parts.Of(ds, epoch.Epoch(5*time.Second))
		view.TimeRangeQuery = ds.TimeRangeQuery.Clone()
		requireLegacyOutput(t, view)
	}
}

func TestMarshalMatchesLegacyUnmerged(t *testing.T) {
	rng := weaktest.NewRand(9, 9)
	a, b := orderDataSet(t, rng), orderDataSet(t, rng)
	// two results take the sort, as does a series whose rows aren't in time order
	two := &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery, Results: dataset.Results{a.Results[0], nil, b.Results[0]}}
	requireLegacyOutput(t, two)
	s := a.Results[0].SeriesList[0]
	pts := dspoints.Of(s)
	pts[0], pts[len(pts)-1] = pts[len(pts)-1], pts[0]
	a.Results[0].SeriesList[0] = dataset.NewSeries(s.Header, pts)
	requireLegacyOutput(t, a)
	// values of other kinds are written as fmt writes them, and a NaN stops a JSON write before it starts
	vs := dataset.NewSeries(s.Header, dataset.Points{
		{Epoch: 1, Values: []any{uint64(7), "a,\"b", true, int64(-1)}},
		{Epoch: 2, Values: []any{[]byte("x"), nil, false, math.NaN()}},
	})
	requireLegacyOutput(t, &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery,
		Results: dataset.Results{{SeriesList: dataset.SeriesList{vs, nil, dataset.NewSeries(s.Header, nil)}}}})
	// Segments that share an epoch at their boundary are still in order, and their rows keep it
	tied := append(dataset.NewSeries(s.Header, dataset.Points{{Epoch: 1, Values: []any{int64(1)}},
		{Epoch: 2, Values: []any{int64(2)}}}).Segments(), dataset.NewSeries(s.Header, dataset.Points{
		{Epoch: 2, Values: []any{int64(3)}}, {Epoch: 3, Values: []any{int64(4)}}}).Segments()...)
	requireLegacyOutput(t, &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery,
		Results: dataset.Results{{SeriesList: dataset.SeriesList{dataset.NewSeriesOf(s.Header, tied)}}}})
	requireLegacyOutput(t, &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery})
	requireLegacyOutput(t, &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery, Results: dataset.Results{nil}})
}

func BenchmarkMarshalOrdered(b *testing.B) {
	ts, err := UnmarshalTimeseries(rowsBody(0, 100, 1000), decoderTRQ())
	require.NoError(b, err)
	ds := ts.(*dataset.DataSet)
	for _, ordering := range [][]timeseries.OrderTerm{nil, {{Column: "time"}}, {{Column: "time", Descending: true}},
		{{Column: "time"}, {Column: "host"}}} {
		for _, of := range []byte{iofmt.V3OutputJSON, iofmt.V3OutputCSV} {
			name := strconv.Itoa(len(ordering)) + "terms/format" + strconv.Itoa(int(of))
			if len(ordering) > 0 && ordering[0].Descending {
				name += "/desc"
			}
			rlo := &timeseries.RequestOptions{OutputFormat: of}
			b.Run(name+"/legacy", func(b *testing.B) {
				ds.TimeRangeQuery.Ordering = ordering
				b.ReportAllocs()
				for b.Loop() {
					if err := legacyMarshal(io.Discard, ds, of); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run(name+"/stream", func(b *testing.B) {
				ds.TimeRangeQuery.Ordering = ordering
				b.ReportAllocs()
				for b.Loop() {
					if err := MarshalTimeseriesWriter(ds, rlo, 200, io.Discard); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
