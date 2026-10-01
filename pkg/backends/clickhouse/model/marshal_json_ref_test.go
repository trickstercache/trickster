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

package model

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

func jsonTestDataSet(tf timeseries.FieldDataType, fds []timeseries.FieldDefinition, series ...*dataset.Series,
) *dataset.DataSet {
	for _, s := range series {
		for _, fd := range fds {
			switch fd.Role {
			case timeseries.RoleTimestamp:
				s.Header.TimestampField = fd
			case timeseries.RoleTag:
				s.Header.TagFieldsList = append(s.Header.TagFieldsList, fd)
			default:
				s.Header.ValueFieldsList = append(s.Header.ValueFieldsList, fd)
			}
		}
	}
	return &dataset.DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{TimestampDefinition: timeseries.FieldDefinition{DataType: tf}},
		Results:        []*dataset.Result{{SeriesList: series}},
	}
}

// jsonValues returns each row's value of field name in a JSON document, as its raw JSON
func jsonValues(t *testing.T, doc []byte, name string) []string {
	t.Helper()
	var d struct {
		Data []map[string]json.RawMessage `json:"data"`
		Rows int                          `json:"rows"`
	}
	require.NoError(t, json.Unmarshal(doc, &d), "%s", doc)
	require.Len(t, d.Data, d.Rows)
	out := make([]string, len(d.Data))
	for i, row := range d.Data {
		out[i] = string(row[name])
	}
	return out
}

func TestMarshalJSONAsClickHouseWritesIt(t *testing.T) {
	ny, _ := LoadZone("America/New_York")
	// each as ClickHouse 26.7 writes it, by its column's type
	for _, c := range []struct {
		typ   string
		value any
		opts  FormatOptions
		want  string
	}{
		{"Int8", int64(-1), FormatOptions{}, "-1"},
		{"UInt64", uint64(18446744073709551615), FormatOptions{}, "18446744073709551615"},
		{"UInt64", uint64(7), FormatOptions{QuoteInt64: true}, `"7"`},
		{"Int32", int64(7), FormatOptions{QuoteInt64: true}, "7"},
		{"Int256", "-10", FormatOptions{}, "-10"},
		{"Int256", "-10", FormatOptions{QuoteInt64: true}, `"-10"`},
		{"Float64", 14.318181818181818, FormatOptions{}, "14.318181818181818"},
		{"Float64", 1e100, FormatOptions{}, "1e100"},
		{"Float64", 1e-7, FormatOptions{}, "1e-7"},
		{"Float64", math.NaN(), FormatOptions{}, "null"},
		{"Float64", math.Inf(-1), FormatOptions{}, "null"},
		{"Float64", math.Inf(-1), FormatOptions{QuoteDenormals: true}, `"-inf"`},
		{"Float64", "1", FormatOptions{}, "1"},
		{"Float64", "nan", FormatOptions{QuoteDenormals: true}, `"nan"`},
		{"Float32", 163.43, FormatOptions{}, "163.43"},
		{"Decimal(18, 3)", 139.0, FormatOptions{}, "139"},
		{"Decimal(18, 9)", 1e-7, FormatOptions{}, "0.0000001"},
		{"Decimal(18, 3)", 139.5, FormatOptions{QuoteDecimals: true}, `"139.5"`},
		{"Bool", true, FormatOptions{}, "true"},
		{"String", `a"b,c`, FormatOptions{}, `"a\"b,c"`},
		{"String", "", FormatOptions{}, `""`},
		{"Nullable(String)", nil, FormatOptions{}, "null"},
		{"Nullable(Float64)", nil, FormatOptions{}, "null"},
		{"FixedString(4)", "ab", FormatOptions{}, `"ab\u0000\u0000"`},
		{"UUID", "61f0c404-5cb3-11e7-907b-a6006ad3dba0", FormatOptions{}, `"61f0c404-5cb3-11e7-907b-a6006ad3dba0"`},
		{"Date", "2026-09-01", FormatOptions{}, `"2026-09-01"`},
		{"DateTime", "2026-09-01 01:02:03", FormatOptions{}, `"2026-09-01 01:02:03"`},
		{"DateTime", "2026-09-01 05:02:03", FormatOptions{Zone: ny}, `"2026-09-01 01:02:03"`},
		{"DateTime64(3)", "2026-09-01 01:02:03.500", FormatOptions{DateTimeFormat: DateTimeISO}, `"2026-09-01T01:02:03.500Z"`},
		{"DateTime", "1970-01-01 00:00:01", FormatOptions{DateTimeFormat: DateTimeUnix}, `"1"`},
		{"Enum8('orange' = 1)", "orange", FormatOptions{}, `"orange"`},
		{"Array(UInt8)", "[1,2]", FormatOptions{}, "[1,2]"},
		{"Array(String)", "['x','y\\'z']", FormatOptions{}, `["x","y'z"]`},
		{"Map(String, UInt8)", "{'k':1,'j':2}", FormatOptions{}, `{"k":1,"j":2}`},
		{"Map(UInt8, Float64)", "{1:nan,2:NULL}", FormatOptions{}, `{"1":null,"2":null}`},
		{"Tuple(UInt8, String)", "(1,'x')", FormatOptions{}, `[1,"x"]`},
		{"Tuple(a UInt8, b String)", "(1,'x')", FormatOptions{}, `{"a":1,"b":"x"}`},
		{"Array(Tuple(UInt8, Bool))", "[(1,true)]", FormatOptions{}, `[[1,true]]`},
		{"Array(UInt8)", "[1,", FormatOptions{}, `"[1,"`},
		{"IPv4", "1.2.3.4", FormatOptions{}, `"1.2.3.4"`},
	} {
		fds := []timeseries.FieldDefinition{
			{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime", DataType: timeseries.DateTimeSQL},
			{Name: "v", Role: timeseries.RoleValue, SDataType: c.typ, OutputPosition: 1},
		}
		ds := jsonTestDataSet(timeseries.DateTimeSQL, fds, dataset.NewSeries(dataset.SeriesHeader{},
			dataset.Points{{Epoch: 1e9, Values: []any{c.value}}}))
		var got bytes.Buffer
		require.NoError(t, marshalTimeseriesJSON(&got, ds, &timeseries.RequestOptions{ProviderRequest: c.opts}, 200))
		require.Equal(t, []string{c.want}, jsonValues(t, got.Bytes(), "v"), "%s %v", c.typ, c.value)
	}
}

func TestMarshalJSONTimesAndTags(t *testing.T) {
	ny, _ := LoadZone("America/New_York")
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime64(3)", DataType: timeseries.DateTimeSQL},
		{Name: "host", Role: timeseries.RoleTag, SDataType: "Nullable(String)", OutputPosition: 1},
		{Name: "code", Role: timeseries.RoleTag, SDataType: "FixedString(3)", OutputPosition: 2},
		{Name: "n", Role: timeseries.RoleTag, SDataType: "UInt64", OutputPosition: 3},
		{Name: "v", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 4},
		{Name: "u", Role: timeseries.RoleUntracked, DefaultValue: "x", OutputPosition: 5},
	}
	at := epoch.Epoch(time.Date(2026, 11, 1, 5, 30, 0, 5e8, time.UTC).UnixNano())
	ds := jsonTestDataSet(timeseries.DateTimeSQL, fds,
		dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"code": "a", "n": "7"}}, dataset.Points{{Epoch: at + 1e9, Values: []any{1.0}}}),
		dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"host": "", "code": "abc", "n": "8"}}, dataset.Points{{Epoch: at, Values: []any{2.0}}}))
	var got bytes.Buffer
	rlo := &timeseries.RequestOptions{ProviderRequest: FormatOptions{Zone: ny}}
	require.NoError(t, marshalTimeseriesJSON(&got, ds, rlo, 200))
	// rows by time across series; a NULL tag, an empty one, a padded one and a number one
	require.Equal(t, []string{`"2026-11-01 01:30:00.500"`, `"2026-11-01 01:30:01.500"`}, jsonValues(t, got.Bytes(), "t"))
	require.Equal(t, []string{`""`, "null"}, jsonValues(t, got.Bytes(), "host"))
	require.Equal(t, []string{`"abc"`, `"a\u0000\u0000"`}, jsonValues(t, got.Bytes(), "code"))
	require.Equal(t, []string{"8", "7"}, jsonValues(t, got.Bytes(), "n"))
	require.Equal(t, []string{`"x"`, `"x"`}, jsonValues(t, got.Bytes(), "u"))
	rw := httptest.NewRecorder()
	require.NoError(t, marshalTimeseriesJSON(rw, ds, rlo, 200))
	require.Equal(t, "America/New_York", rw.Header().Get(TimezoneHeader))
	// a time given in units is a number, quoted when a 64-bit one is
	fds[0] = timeseries.FieldDefinition{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "UInt64", DataType: timeseries.DateTimeUnixSecs}
	ds = jsonTestDataSet(timeseries.DateTimeUnixSecs, fds[:1], dataset.NewSeries(dataset.SeriesHeader{}, dataset.Points{{Epoch: 2e9}}))
	got.Reset()
	require.NoError(t, marshalTimeseriesJSON(&got, ds, &timeseries.RequestOptions{ProviderRequest: FormatOptions{QuoteInt64: true}}, 200))
	require.Equal(t, []string{`"2"`}, jsonValues(t, got.Bytes(), "t"))
}

func TestMarshalJSONIsValid(t *testing.T) {
	rng := weaktest.NewRand(9, 4)
	for trial := range 300 {
		ds := randomNativeDataSet(rng)
		for _, opts := range []FormatOptions{{}, {QuoteInt64: true, QuoteDecimals: true, QuoteDenormals: true, DateTimeFormat: DateTimeISO}} {
			var got bytes.Buffer
			require.NoError(t, marshalTimeseriesJSON(&got, ds, &timeseries.RequestOptions{ProviderRequest: opts}, 200))
			require.True(t, json.Valid(got.Bytes()), "trial %d: %s", trial, got.Bytes())
			var rows int
			for _, s := range ds.Results[0].SeriesList {
				rows += s.PointCount()
			}
			require.Len(t, jsonValues(t, got.Bytes(), "t"), rows)
		}
	}
	var empty bytes.Buffer
	require.NoError(t, marshalTimeseriesJSON(&empty, &dataset.DataSet{}, nil, 200))
	require.Equal(t, `{"meta":[],"data":[],"rows":0}`+"\n", empty.String())
}

func TestTimeOrderedRowsMatchesAStableSort(t *testing.T) {
	rng := weaktest.NewRand(14, 2)
	for trial := range 300 {
		r := &dataset.Result{}
		sorted := trial%3 != 0
		for range rng.IntN(5) {
			s := dataset.NewSeries(dataset.SeriesHeader{}, nil)
			at := rng.IntN(4)
			for range rng.IntN(8) {
				if sorted {
					at += rng.IntN(2)
				} else {
					at = rng.IntN(6)
				}
				s.SetPoints(append(dspoints.Of(s), dataset.Point{Epoch: epoch.Epoch(at), Values: []any{s.PointCount()}}))
			}
			r.SeriesList = append(r.SeriesList, s)
		}
		var want []outputRow
		for j, s := range r.SeriesList {
			segs := s.Segments()
			for k := range segs {
				for i := range segs[k].Len() {
					want = append(want, outputRow{series: s, seg: &segs[k], i: i, list: j})
				}
			}
		}
		slices.SortStableFunc(want, func(a, b outputRow) int { return cmp.Compare(a.epoch(), b.epoch()) })
		rows, n := timeOrderedRows(r)
		got := slices.Collect(rows)
		if n != len(want) || len(got) != len(want) {
			t.Fatalf("trial %d: %d rows, count %d, want %d", trial, len(got), n, len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("trial %d: row %d differs", trial, i)
			}
		}
	}
}

func TestMarshalReadsSeriesParts(t *testing.T) {
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime64(3)", DataType: timeseries.DateTimeUnixMilli, OutputPosition: 0},
		{Name: "hostname", Role: timeseries.RoleTag, SDataType: "String", OutputPosition: 1},
		{Name: "v", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 2},
	}
	var series []*dataset.Series
	for i := range 3 {
		s := dataset.NewSeries(dataset.SeriesHeader{Tags: dataset.Tags{"hostname": fmt.Sprint("h", i)}}, nil)
		for j := range 5 {
			s.SetPoints(append(dspoints.Of(s), dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
				Values: []any{float64(i*j) / 3}}))
		}
		series = append(series, s)
	}
	view := parts.Of(jsonTestDataSet(timeseries.DateTimeUnixMilli, fds, series...), 60e9)
	if !view.HasParts() {
		t.Fatal("the view has no parts")
	}
	for of := byte(0); of <= OutputFormatNative; of++ {
		rlo := &timeseries.RequestOptions{OutputFormat: of}
		var got, want bytes.Buffer
		if err := MarshalTimeseriesWriter(view, rlo, 200, &got); err != nil {
			t.Fatal(of, err)
		}
		if err := MarshalTimeseriesWriter(view.Flat(), rlo, 200, &want); err != nil {
			t.Fatal(of, err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) || got.Len() == 0 {
			t.Fatalf("format %d:\n got %q\nwant %q", of, got.Bytes(), want.Bytes())
		}
	}
}
