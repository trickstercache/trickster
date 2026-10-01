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

package arrow

import (
	"fmt"
	"math"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/stretchr/testify/require"
)

// the columns of orderSchema, and sort keys over them
var orderKeys = [][]SortKey{
	nil,
	{{Column: "time"}},
	{{Column: "time", Descending: true}},
	{{Column: "time"}, {Column: "host", Descending: true}},
	{{Column: "time", Descending: true}, {Column: "f", NullsFirst: true}},
	{{Column: "time"}, {Column: "i", Descending: true}, {Column: "s"}},
	{{Column: "host"}, {Column: "time", Descending: true}},
	{{Column: "f"}},
	{{Column: "f", Descending: true, NullsFirst: true}, {Column: "u"}},
	{{Column: "b"}, {Column: "i8", Descending: true}},
	{{Column: "s", NullsFirst: true}, {Column: "d"}},
	{{Column: "u", Descending: true}},
}

func orderSchema() *arrow.Schema {
	return arrow.NewSchema([]arrow.Field{
		{Name: "time", Type: arrow.FixedWidthTypes.Timestamp_ns},
		{Name: "host", Type: arrow.BinaryTypes.String},
		{Name: "f", Type: arrow.PrimitiveTypes.Float64, Nullable: true},
		{Name: "i", Type: arrow.PrimitiveTypes.Int64, Nullable: true},
		{Name: "u", Type: arrow.PrimitiveTypes.Uint64, Nullable: true},
		{Name: "s", Type: arrow.BinaryTypes.String, Nullable: true},
		{Name: "b", Type: arrow.FixedWidthTypes.Boolean, Nullable: true},
		{Name: "i8", Type: arrow.PrimitiveTypes.Int8, Nullable: true},
		{Name: "d", Type: dictUtf8(), Nullable: true},
	}, nil)
}

// orderDataSet decodes random rows of a few hosts, many sharing a time, values that tie and nulls
func orderDataSet(t *testing.T, rng *weaktest.Rand, rows int) *dataset.DataSet {
	pick := func(values ...any) any {
		if rng.IntN(5) == 0 {
			return nil
		}
		return values[rng.IntN(len(values))]
	}
	cells := make([][]any, 0, rows)
	for r := range rows {
		cells = append(cells, []any{int64(r/3) * 1e9, fmt.Sprintf("h%d", r%3),
			pick(1.5, -2.0, 1.5, math.NaN(), math.Inf(1), 1e300), pick(int64(1), int64(-1), int64(math.MaxInt64), int64(math.MaxInt64-1)),
			pick(uint64(math.MaxUint64), uint64(3)), pick("a", "b", ""), pick(true, false),
			pick(int64(-8), int64(7)), pick("x", "y", "")})
	}
	schema := orderSchema()
	return orderingDataSet(t, schema, cells)
}

func requireLegacyRecords(t *testing.T, ds *dataset.DataSet) {
	t.Helper()
	schema := orderSchema()
	for _, keys := range orderKeys {
		want, werr := legacyToRecords(schema, ds, keys...)
		got, err := ToRecords(schema, ds, keys...)
		require.Equal(t, werr, err, "keys %v", keys)
		require.Len(t, got, len(want), "keys %v", keys)
		for i := range want {
			// printed, as a NaN isn't equal to itself
			require.Equal(t, fmt.Sprint(want[i]), fmt.Sprint(got[i]), "keys %v, batch %d", keys, i)
			want[i].Release()
			got[i].Release()
		}
	}
}

func TestToRecordsMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(41, 41)
	for range 30 {
		ds := orderDataSet(t, rng, rng.IntN(60))
		requireLegacyRecords(t, ds)
		// a series held in parts, as a cached one can be, reads as one
		view := parts.Of(ds, 2e9)
		view.TimeRangeQuery = ds.TimeRangeQuery
		requireLegacyRecords(t, view)
	}
	// rows past a batch are cut where they were
	requireLegacyRecords(t, orderDataSet(t, rng, 2*maxRowsPerBatch+7))
}

func TestToRecordsMatchesLegacyUnmerged(t *testing.T) {
	rng := weaktest.NewRand(42, 42)
	ds := orderDataSet(t, rng, 40)
	list := ds.Results[0].SeriesList
	// a series whose rows aren't in time order takes the sort
	pts := dspoints.Of(list[0])
	pts[0], pts[len(pts)-1] = pts[len(pts)-1], pts[0]
	unsorted := &dataset.DataSet{TimeRangeQuery: ds.TimeRangeQuery, Results: dataset.Results{{SeriesList: dataset.SeriesList{
		nil, dataset.NewSeries(list[0].Header, pts), list[1], dataset.NewSeries(list[2].Header, nil)}}}}
	requireLegacyRecords(t, unsorted)
	// Segments that share an epoch at their boundary keep their rows' order
	p := dspoints.Of(list[1])
	other := p[0]
	other.Values = append([]any{}, p[0].Values...)
	other.Values[0] = 9.0
	tied := append(dataset.NewSeries(list[1].Header, p[:1]).Segments(),
		dataset.NewSeries(list[1].Header, dataset.Points{other}).Segments()...)
	requireLegacyRecords(t, &dataset.DataSet{TimeRangeQuery: ds.TimeRangeQuery, Results: dataset.Results{{
		SeriesList: dataset.SeriesList{list[0], dataset.NewSeriesOf(list[1].Header, tied)}}}})
	// a dictionary column of only empty text holds no bytes
	empty := dspoints.Of(list[2])
	for i := range empty {
		empty[i].Values[len(empty[i].Values)-1] = ""
	}
	requireLegacyRecords(t, &dataset.DataSet{TimeRangeQuery: ds.TimeRangeQuery, Results: dataset.Results{{
		SeriesList: dataset.SeriesList{dataset.NewSeries(list[2].Header, empty)}}}})
	// a column holding values of another kind compares them as before
	mixed := dspoints.Of(list[2])
	for i := range mixed {
		mixed[i].Values[0] = int64(i % 3)
		mixed[i].Values[1] = []byte("x")
	}
	requireLegacyRecords(t, &dataset.DataSet{TimeRangeQuery: ds.TimeRangeQuery, Results: dataset.Results{{
		SeriesList: dataset.SeriesList{list[0], dataset.NewSeries(list[2].Header, mixed)}}}})
}

func BenchmarkToRecords(b *testing.B) {
	const series, points = 100, 1000
	rows := make([][]any, 0, series*points)
	for p := range points {
		for s := range series {
			rows = append(rows, []any{int64(p) * 1e9, fmt.Sprintf("h%d", s), float64(s*p%9973) / 7, int64(p),
				uint64(s), "x", p%2 == 0, int64(p % 100), "y"})
		}
	}
	schema := orderSchema()
	rec := makeRecord(&testing.T{}, schema, rows)
	ds, err := FromRecords(schema, []arrow.RecordBatch{rec}, testTRQ("host"))
	rec.Release()
	require.NoError(b, err)
	for _, keys := range [][]SortKey{nil, {{Column: "time", Descending: true}}, {{Column: "time"}, {Column: "f"}},
		{{Column: "f"}}} {
		name := fmt.Sprint(keys)
		for impl, fn := range map[string]func(*arrow.Schema, *dataset.DataSet, ...SortKey) ([]arrow.RecordBatch, error){
			"legacy": legacyToRecords, "stream": ToRecords,
		} {
			b.Run(name+"/"+impl, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					recs, err := fn(schema, ds, keys...)
					if err != nil {
						b.Fatal(err)
					}
					for _, r := range recs {
						r.Release()
					}
				}
			})
		}
	}
}
