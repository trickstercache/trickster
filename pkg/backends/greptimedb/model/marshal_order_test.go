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
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

const orderSchema = `[{"name":"time","data_type":"TimestampMillisecond"},{"name":"host","data_type":"String"},` +
	`{"name":"region","data_type":"String"},{"name":"v","data_type":"Float64"},{"name":"n","data_type":"Int64"},` +
	`{"name":"s","data_type":"String"},{"name":"b","data_type":"Boolean"},{"name":"u","data_type":"UInt64"}]`

var orderings = [][]timeseries.OrderTerm{
	nil,
	{{Column: "time"}},
	{{Column: "time", Descending: true}},
	{{Column: "time"}, {Column: "host"}},
	{{Column: "time", Descending: true}, {Column: "n", Descending: true, NullsFirst: true}},
	{{Column: "time"}, {Column: "v", NullsFirst: true}, {Column: "s", Descending: true}},
	{{Column: "time"}, {Column: "region", NullsFirst: true}},
	{{Column: "time"}, {Column: "missing"}},
	{{Column: "missing"}, {Column: "time", Descending: true}},
	{{Column: "host"}, {Column: "time", Descending: true}},
	{{Column: "region", Descending: true}, {Column: "u"}},
	{{Column: "v"}},
	{{Column: "b", Descending: true}, {Column: "n"}, {Column: "s", NullsFirst: true}},
}

func orderQuery(series, rows int) *timeseries.TimeRangeQuery {
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: "fixture", OutputColumn: "time", GroupColumns: []string{"host", "region"},
		ValueColumns: []string{"v", "n", "s", "b", "u"}, Step: time.Second, OutputUnit: timeseries.DateTimeRFC3339Nano,
	}
	trq := sqlanalyzer.NewTimeRangeQuery("fixture")
	plan.ApplyToQuery(trq)
	trq.Extent = timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(int64(series*rows), 0)}
	return trq
}

func orderBody(rows []string) []byte {
	return []byte(`{"output":[{"records":{"schema":{"column_schemas":` + orderSchema + `},"rows":[` +
		strings.Join(rows, ",") + `],"total_rows":` + strconv.Itoa(len(rows)) + `}}],"execution_time_ms":1}`)
}

// orderDataSet decodes random rows of a few series, many sharing a time, with nulls in every column but time
func orderDataSet(t testing.TB, rng *weaktest.Rand) *dataSet {
	pick := func(null string, values ...string) string {
		if rng.IntN(6) == 0 {
			return null
		}
		return values[rng.IntN(len(values))]
	}
	var rows []string
	for at := range 4 + rng.IntN(12) {
		for host := range 3 {
			for region := range 2 {
				if rng.IntN(3) == 0 {
					continue
				}
				r := `"r` + strconv.Itoa(region) + `"`
				if region == 1 {
					r = "null"
				}
				// a tag that needs escaping is held as escaped JSON
				rows = append(rows, fmt.Sprintf(`[%d,%s,%s,%s,%s,%s,%s,%s]`, at*1000, []string{`"h0"`, `"h<1>"`, `"h\"2"`}[host], r,
					pick("null", "1", "2.5", "-0.25", "1e21"), pick("null", "-9223372036854775808", "0", "7"),
					pick("null", `"a"`, `"b<&>"`, `""`), pick("null", "true", "false"),
					pick("null", "18446744073709551615", "3")))
			}
		}
	}
	// the decoder holds each series in time order, so shuffled rows test only its grouping
	rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
	ts, err := UnmarshalTimeseries(orderBody(rows), orderQuery(1, 100))
	require.NoError(t, err)
	return ts.(*dataSet)
}

func requireLegacyOutput(t *testing.T, d *dataSet) {
	t.Helper()
	if d.TimeRangeQuery == nil {
		d.TimeRangeQuery = orderQuery(1, 1)
	}
	for _, ordering := range orderings {
		d.TimeRangeQuery.Ordering = ordering
		want, got := httptest.NewRecorder(), httptest.NewRecorder()
		werr := legacyMarshal(d, want)
		err := MarshalTimeseriesWriter(d, nil, 200, got)
		require.Equal(t, werr, err, "ordering %v", ordering)
		require.Equal(t, want.Body.String(), got.Body.String(), "ordering %v", ordering)
		require.Equal(t, want.Header(), got.Header(), "ordering %v", ordering)
	}
}

func TestMarshalMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(11, 11)
	for range 60 {
		d := orderDataSet(t, rng)
		requireLegacyOutput(t, d)
		// a series held in parts, as a cached one can be, reads as one
		view := &dataSet{DataSet: parts.Of(d.DataSet, 3000*1e6), fields: d.fields}
		view.TimeRangeQuery = d.TimeRangeQuery.Clone()
		requireLegacyOutput(t, view)
	}
}

func TestMarshalMatchesLegacyUnmerged(t *testing.T) {
	rng := weaktest.NewRand(12, 12)
	a, b := orderDataSet(t, rng), orderDataSet(t, rng)
	// two results take the sort, as does a series whose rows aren't in time order
	requireLegacyOutput(t, &dataSet{DataSet: &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery,
		Results: dataset.Results{a.Results[0], nil, b.Results[0]}}, fields: a.fields})
	s := a.Results[0].SeriesList[0]
	pts := dspoints.Of(s)
	pts[0], pts[len(pts)-1] = pts[len(pts)-1], pts[0]
	a.Results[0].SeriesList = append(dataset.SeriesList{nil, dataset.NewSeries(s.Header, pts)}, a.Results[0].SeriesList[1:]...)
	requireLegacyOutput(t, a)
	// Segments that share an epoch at their boundary are still in order, and their rows keep it
	row := func(e int64, n int64) dataset.Points {
		return dataset.Points{{Epoch: epoch.Epoch(e * 1e9), Values: []any{1.5, n, "x", true, uint64(1)}}}
	}
	tied := append(dataset.NewSeries(s.Header, append(row(1, 1), row(2, 2)...)).Segments(),
		dataset.NewSeries(s.Header, append(row(2, 3), row(3, 4)...)).Segments()...)
	requireLegacyOutput(t, &dataSet{DataSet: &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery,
		Results: dataset.Results{{SeriesList: dataset.SeriesList{dataset.NewSeriesOf(s.Header, tied), dataset.NewSeries(s.Header, nil)}}}},
		fields: a.fields})
	// invalid UTF-8 in a tag is read as U+FFFD, which sorts before U+FFFE
	invalid, high := s.Header.Clone(), s.Header.Clone()
	invalid.Tags["host"], high.Tags["host"] = "\"\xff\"", "\"\uFFFE\""
	requireLegacyOutput(t, &dataSet{DataSet: &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery,
		Results: dataset.Results{{SeriesList: dataset.SeriesList{dataset.NewSeries(invalid, dspoints.Of(s)),
			dataset.NewSeries(high, dspoints.Of(s))}}}}, fields: a.fields})
	// an empty Segment holds no rows to check
	requireLegacyOutput(t, &dataSet{DataSet: &dataset.DataSet{TimeRangeQuery: a.TimeRangeQuery,
		Results: dataset.Results{{SeriesList: dataset.SeriesList{dataset.NewSeriesOf(s.Header, dataset.Segments{{}})}}}},
		fields: a.fields})
	// nothing, or an empty result, still writes the schema
	requireLegacyOutput(t, &dataSet{DataSet: &dataset.DataSet{}, fields: a.fields})
	requireLegacyOutput(t, &dataSet{DataSet: &dataset.DataSet{Results: dataset.Results{nil, {}}}, fields: a.fields})
}

func TestMarshalFailsAsLegacy(t *testing.T) {
	d := orderDataSet(t, weaktest.NewRand(13, 13))
	s := d.Results[0].SeriesList[0]
	with := func(series ...*dataset.Series) *dataSet {
		return &dataSet{DataSet: &dataset.DataSet{TimeRangeQuery: d.TimeRangeQuery,
			Results: dataset.Results{{SeriesList: series}}}, fields: d.fields}
	}
	point := func(e int64, values ...any) *dataset.Series {
		return dataset.NewSeries(s.Header, dataset.Points{{Epoch: epoch.Epoch(e), Values: values}})
	}
	values := []any{1.5, int64(1), "x", true, uint64(1)}
	noTag := s.Header.Clone()
	delete(noTag.Tags, "host")
	badTag := s.Header.Clone()
	badTag.Tags["host"] = "7"
	rawTag := s.Header.Clone()
	rawTag.Tags["host"] = "\"a\x01\""
	short := dataset.NewSeries(s.Header, dataset.Points{{Epoch: 1e9, Values: values[:2]}, {Epoch: 1e9 + 1, Values: values[:2]}})
	for name, bad := range map[string]*dataSet{
		"a NaN":            with(s, point(1e9, append([]any{math.NaN()}, values[1:]...)...)),
		"a missing value":  with(s, point(1e9, values[:4]...)),
		"an extra value":   with(s, point(1e9, append(values, 1)...)),
		"a sub-unit time":  with(s, point(1e9+1, values...)),
		"a sub-unit short": with(s, point(1e9+1, values[:2]...)),
		"a missing tag":    with(s, dataset.NewSeries(noTag, nil)),
		"a mistyped tag":   with(s, dataset.NewSeries(badTag, nil)),
		"a raw control":    with(s, dataset.NewSeries(rawTag, nil)),
		"a later bad time": with(s, short),
	} {
		t.Run(name, func(t *testing.T) {
			requireLegacyOutput(t, bad)
			_, err := MarshalTimeseries(bad, nil, 200)
			require.Error(t, err)
		})
	}
	// a time after a missing value isn't checked
	fields := append(timeseries.FieldDefinitions{d.fields[3]}, d.fields[:3]...)
	fields = append(fields, d.fields[4:]...)
	requireLegacyOutput(t, &dataSet{DataSet: with(point(1e9 + 1)).DataSet, fields: fields})
	// a signed time of the finest unit is any epoch, an unsigned one only those from 1970, and a float one any
	fields = d.fields.Clone()
	fields[0].SDataType, fields[0].DataType = "Int64", timeseries.Int64
	fields[0].ProviderData1 = byte(timeseries.DateTimeUnixNano)
	requireLegacyOutput(t, &dataSet{DataSet: with(s, point(-1, values...)).DataSet, fields: fields})
	fields[0].SDataType, fields[0].DataType = "UInt64", timeseries.Uint64
	requireLegacyOutput(t, &dataSet{DataSet: with(s, point(-1, values...)).DataSet, fields: fields})
	fields[0].SDataType, fields[0].DataType = "Float64", timeseries.Float64
	fields[0].ProviderData1 = byte(timeseries.DateTimeUnixMilli)
	requireLegacyOutput(t, &dataSet{DataSet: with(s, point(1e9+1, values...)).DataSet, fields: fields})
	// a column of another role is written as null
	fields = append(d.fields.Clone(), timeseries.FieldDefinition{Name: "x", SDataType: "String", Role: timeseries.RoleUntracked})
	requireLegacyOutput(t, &dataSet{DataSet: d.DataSet, fields: fields})
	// a time of an unknown unit fails before anything is written
	fields = d.fields.Clone()
	fields[0].SDataType = "Int64"
	requireLegacyOutput(t, &dataSet{DataSet: d.DataSet, fields: fields})
	fields[0].SDataType, fields[0].DataType = "Float64", timeseries.Float64
	requireLegacyOutput(t, &dataSet{DataSet: d.DataSet, fields: fields})
}

func BenchmarkMarshalOrdered(b *testing.B) {
	const series, points = 100, 1000
	rows := make([]string, 0, series*points)
	for i := range series {
		for j := range points {
			rows = append(rows, fmt.Sprintf(`[%d,"h%d","r%d",%d.5,%d,"s%d",%t,%d]`, j*1000, i, i%3, i*j%9973, j, j%7, j%2 == 0, i))
		}
	}
	ts, err := UnmarshalTimeseries(orderBody(rows), orderQuery(series, points))
	require.NoError(b, err)
	d := ts.(*dataSet)
	for _, ordering := range [][]timeseries.OrderTerm{nil, {{Column: "time"}}, {{Column: "time", Descending: true}},
		{{Column: "time"}, {Column: "host"}}, {{Column: "host"}, {Column: "time"}}, {{Column: "n"}, {Column: "s"}}} {
		name := "none"
		if len(ordering) > 0 {
			var terms []string
			for _, term := range ordering {
				terms = append(terms, term.Column+map[bool]string{true: "-desc"}[term.Descending])
			}
			name = strings.Join(terms, ",")
		}
		b.Run(name+"/legacy", func(b *testing.B) {
			d.TimeRangeQuery.Ordering = ordering
			b.ReportAllocs()
			for b.Loop() {
				if err := legacyMarshal(d, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/stream", func(b *testing.B) {
			d.TimeRangeQuery.Ordering = ordering
			b.ReportAllocs()
			for b.Loop() {
				if err := MarshalTimeseriesWriter(d, nil, 200, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestCompareValue(t *testing.T) {
	for _, c := range []struct {
		a, b any
		want int
	}{
		{int64(-1), int64(2), -1},
		{uint64(3), uint64(2), 1},
		{1.5, 1.5, 0},
		{"b", "a", 1},
		{false, true, -1},
		{true, true, 0},
		{json.Number("9007199254740993"), json.Number("9.007199254740992e15"), 1},
		{json.Number("x"), json.Number("1"), 0},
		{int64(1), uint64(2), 0},
		{[]byte("a"), []byte("b"), 0},
	} {
		require.Equal(t, c.want, compareValue(c.a, c.b), "%v %v", c.a, c.b)
		require.Equal(t, -c.want, compareValue(c.b, c.a), "%v %v", c.b, c.a)
	}
}

func TestRowsStopWhenAsked(t *testing.T) {
	d := orderDataSet(t, weaktest.NewRand(14, 14))
	for _, ordering := range orderings[:3] {
		d.TimeRangeQuery.Ordering = ordering
		p, err := newGreptimePlan(d)
		require.NoError(t, err)
		n := 0
		for range p.rows() {
			n++
			break
		}
		require.Equal(t, 1, n)
	}
}
