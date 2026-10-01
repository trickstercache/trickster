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
	"encoding/binary"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/native/server"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// decoderTRQ returns a query whose time column is timeName, read as dt, grouped by tags
func decoderTRQ(timeName string, dt timeseries.FieldDataType, tags ...string) *timeseries.TimeRangeQuery {
	trq := testTRQ.Clone()
	trq.TimestampDefinition = timeseries.FieldDefinition{Name: timeName, DataType: dt}
	trq.TagFieldDefintions = make(timeseries.FieldDefinitions, len(tags))
	for i, tag := range tags {
		trq.TagFieldDefintions[i] = timeseries.FieldDefinition{Name: tag}
	}
	return trq
}

func sqlTRQ() *timeseries.TimeRangeQuery {
	return decoderTRQ("t", timeseries.DateTimeSQL, "host", "region")
}

type tsvCase struct {
	body string
	trq  *timeseries.TimeRangeQuery
}

const (
	sqlHeader = "t\thost\tregion\tv\tn\nDateTime\tString\tLowCardinality(String)\tFloat64\tUInt64\n"
	typesRow  = "Int8\tInt64\tUInt32\tFloat32\tNullable(Float64)\tDecimal(9, 2)\tBool\tString\tNullable(String)\t" +
		"Date\tDateTime\tDateTime64(3)\tEnum8(\\'a\\' = 1)\tArray(UInt8)\tNothing\tUUID"
	typesHeader = "t\thost\ti8\ti64\tu32\tf32\tnf\tdec\tb\ts\tns\td\tdt\tdt64\te\tarr\tnothing\tuuid\n" +
		"DateTime\tString\t" + typesRow + "\n"
)

// the TSV bodies every feed of the stream decoder must decode alike
var tsvBodies = map[string]tsvCase{
	"response": {testDataTSVWithNamesAndTypes, testTRQ.Clone()},
	"escaped":  {escapedTSVFixture, fixtureTRQ()},
	"grouped": {sqlHeader +
		"2024-01-01 00:00:00\ta\tr1\t1.5\t1\n2024-01-01 00:00:00\tb\tr1\t2\t2\n" +
		"2024-01-01 00:01:00\ta\tr1\t-3e-7\t3\n2024-01-01 00:01:00\tb\tr2\t4\t4\n" +
		"2024-01-01 00:02:00\ta\tr1\tnan\t5\n2024-01-01 00:02:00\tb\tr1\t-inf\t18446744073709551615\n", sqlTRQ()},
	"types": {typesHeader +
		"2024-01-01 00:00:00\th\t-8\t-9223372036854775808\t4294967295\t0.1\t1.25\t-14.83\ttrue\tx\\ty\t\\N\t" +
		"2024-01-01\t2024-01-01 00:00:01\t2024-01-01 00:00:00.123\ta\t[1,2]\t\t61f0c404-5cb3-11e7-907b-a6006ad3dba0\n" +
		"2024-01-01 00:01:00\th\tx\t99999999999999999999\t-1\t1e400\t\\N\tx\tmaybe\t\t\\\\N\t" +
		"bad\t\t\t\t[]\tz\t\n" +
		"2024-01-01 00:02:00\th\t\t\t\t\t\t\tF\tq\tn\t\t\t\t\t\t\t\n", decoderTRQ("t", timeseries.DateTimeSQL, "host")},
	"time forms": {"t\thost\tv\nDateTime64(9)\tString\tInt32\n" +
		"2024-01-01 00:00:00\ta\t1\n2024-01-01 00:00:00.123456789123\ta\t2\n 2024-01-01 00:00:00.5 \ta\t3\n" +
		"2024-01-01 00:00:01.\ta\t4\n2024-01-01 00:00:01.12x\ta\t5\n2024-02-30 00:00:00\ta\t6\n" +
		"2024-01-01T00:00:02\ta\t7\n\\N\ta\t8\n2024-01-01 00:00:03,5\ta\t9\n", sqlTRQ()},
	"dates": {"d\tv\nDate\tFloat64\n1970-01-01\t1\n2024-02-29\t2\n2023-02-29\t3\n2024-03-01 00:00:00\t4\n",
		decoderTRQ("d", timeseries.DateSQL)},
	"epoch forms": {"t\tv\nUInt64\tFloat64\n1700000000\t1\n1700000001000\t2\n1700000002000000\t3\n" +
		"1700000003000000000\t4\n170000000\t5\n-1700000005\t6\n\t7\n17000000060000000000\t8\n" +
		"2024-01-01 00:00:00\t9\n2024-01-01T00:00:00\t10\n", decoderTRQ("t", timeseries.DateTimeUnixSecs)},
	"rfc3339": {"t\tv\nObject\tFloat64\n2024-01-01T00:00:00Z\t1\n2024-01-01T00:00:01.5Z\t2\n" +
		"2024-01-01T01:00:02+01:00\t3\n2024-01-01 00:00:03\t4\n", decoderTRQ("t", timeseries.DateTimeRFC3339Nano)},
	"time of day": {"t\tv\nObject\tFloat64\n00:00:01\t1\n12:34:56\t2\n24:00:00\t3\n",
		decoderTRQ("t", timeseries.TimeSQL)},
	"string time": {"t\tv\nString\tFloat64\n1700000001\t1\n2024-01-01 00:00:00\t2\n2024-01-01 00:00:03.5\t3\n",
		decoderTRQ("t", timeseries.DateTimeSQL)},
	"empty tags": {sqlHeader + "2024-01-01 00:00:00\t\t\t1\t1\n2024-01-01 00:00:00\ta\t\t2\t2\n" +
		"2024-01-01 00:01:00\t\tr1\t3\t3\n2024-01-01 00:02:00\t\t\t4\t4\n", sqlTRQ()},
	"null tags": {sqlHeader + "2024-01-01 00:00:00\t\\N\tr1\t1\t1\n2024-01-01 00:01:00\t\\N\tr1\t2\t2\n", sqlTRQ()},
	"escaped tags": {"t\th\\tost\tv\nDateTime\tString\tFloat64\n" +
		"2024-01-01 00:00:00\ta\\tb\\\\c\\'d\\ne\t1\n2024-01-01 00:00:00\tx\\qy\\\t2\n",
		decoderTRQ("t", timeseries.DateTimeSQL, "h\tost")},
	"no rows":       {sqlHeader, sqlTRQ()},
	"blank lines":   {"\n\n" + sqlHeader + "\n2024-01-01 00:00:00\ta\tr1\t1\t1\n\n\n2024-01-01 00:01:00\ta\tr1\t2\t2\n\n", sqlTRQ()},
	"crlf":          {strings.ReplaceAll(sqlHeader+"2024-01-01 00:00:00\ta\tr1\t1\t1\n2024-01-01 00:01:00\ta\tr1\t2\t2\n", "\n", "\r\n"), sqlTRQ()},
	"no final line": {sqlHeader + "2024-01-01 00:00:00\ta\tr1\t1\t1\n2024-01-01 00:01:00\ta\tr1\t2\t2", sqlTRQ()},
	"duplicates": {sqlHeader + "2024-01-01 00:00:00\ta\tr1\t1\t1\n2024-01-01 00:00:00\ta\tr1\t2\t2\n" +
		"2024-01-01 00:00:00\ta\tr1\t3\t3\n", sqlTRQ()},
	"unnamed column": {"t\t\tv\nDateTime\tString\tFloat64\n2024-01-01 00:00:00\tx\t1\n", sqlTRQ()},
	"repeated names": {"t\tv\tt\tv\nDateTime\tFloat64\tDateTime\tInt64\n" +
		"2024-01-01 00:00:00\t1\t2024-01-01 00:05:00\t2\n2024-01-01 00:01:00\t3\tbad\t4\n" +
		"2024-01-01 00:02:00\t5\t2024-01-01 00:06:00\t6\n", sqlTRQ()},
	"bool forms": {"t\tb\nDateTime\tBool\n2024-01-01 00:00:00\ttrue\n2024-01-01 00:00:01\tFALSE\n" +
		"2024-01-01 00:00:02\t1\n2024-01-01 00:00:03\tt\n2024-01-01 00:00:04\tyes\n2024-01-01 00:00:05\t\\N\n", sqlTRQ()},
	"bad times": {sqlHeader + "2024-01-01 00:00:00\ta\tr1\t1\t1\nnope\ta\tr1\t2\t2\n2024-01-01 00:01:00\ta\tr1\t3\t3\n",
		sqlTRQ()},
	"long line": {sqlHeader + "2024-01-01 00:00:00\t" + strings.Repeat("h", 70000) + "\tr1\t1\t1\n", sqlTRQ()},
}

func tsvConformance(t *testing.T, c tsvCase) {
	t.Helper()
	streamtest.Conformance(t, newTSVDecoder, streamtest.Case{
		TRQ: c.trq, Body: []byte(c.body),
	})
}

func TestTSVDecoderConformance(t *testing.T) {
	for name, c := range tsvBodies {
		t.Run(name, func(t *testing.T) { tsvConformance(t, c) })
	}
}

func TestTSVDecoderConformanceAtScale(t *testing.T) {
	rng := weaktest.NewRand(5, 5)
	for trial := range 40 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) {
			body, trq := randomTSV(rng)
			tsvConformance(t, tsvCase{string(body), trq})
		})
	}
}

// a column of a random body: its name, type, and each row's text
type randomColumn struct {
	name, typ string
	value     func(rng *weaktest.Rand) string
}

var randomValueColumns = []randomColumn{
	{"f", "Float64", func(rng *weaktest.Rand) string {
		return []string{strconv.FormatFloat(rng.NormFloat64()*1e3, 'g', -1, 64), "nan", "-inf", "1e-7"}[rng.IntN(4)]
	}},
	{"f32", "Float32", func(rng *weaktest.Rand) string { return strconv.FormatFloat(float64(rng.Float32()), 'g', -1, 32) }},
	{"i", "Int32", func(rng *weaktest.Rand) string { return strconv.Itoa(rng.IntN(2000) - 1000) }},
	{"u", "UInt64", func(rng *weaktest.Rand) string { return strconv.FormatUint(rng.Uint64(), 10) }},
	{"b", "Bool", func(rng *weaktest.Rand) string { return []string{"true", "false", "1"}[rng.IntN(3)] }},
	{"s", "String", func(rng *weaktest.Rand) string {
		return []string{"plain", `tab\there`, `back\\slash`, `it\'s`, "a,b"}[rng.IntN(5)]
	}},
	{"ns", "Nullable(String)", func(rng *weaktest.Rand) string { return []string{`\N`, "v"}[rng.IntN(2)] }},
	{"d", "Decimal(18, 3)", func(rng *weaktest.Rand) string { return strconv.Itoa(rng.IntN(1e6)) + ".125" }},
	{"dt", "DateTime", func(rng *weaktest.Rand) string {
		return time.Unix(int64(rng.IntN(2e9)), 0).UTC().Format(dateTimeLayout)
	}},
	{"a", "Array(String)", func(rng *weaktest.Rand) string { return `['x','y']` }},
}

// randomTSV writes a GROUP BY time, tags response: each bucket holds a row for some of the tags'
// combinations, which keeps each series in time order
func randomTSV(rng *weaktest.Rand) ([]byte, *timeseries.TimeRangeQuery) {
	times := []struct {
		typ string
		dt  timeseries.FieldDataType
		fmt func(time.Time) string
	}{
		{"DateTime", timeseries.DateTimeSQL, func(t time.Time) string { return t.Format(dateTimeLayout) }},
		{"DateTime64(3)", timeseries.DateTimeSQL, func(t time.Time) string { return t.Format(dateTimeLayout + ".000") }},
		{"UInt64", timeseries.DateTimeUnixMilli, func(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }},
		{"Date", timeseries.DateSQL, func(t time.Time) string { return t.Format(dateLayout) }},
	}
	tt := times[rng.IntN(len(times))]
	tags := []string{"host", "region"}[:rng.IntN(3)]
	values := make([]randomColumn, 1+rng.IntN(3))
	for i := range values {
		values[i] = randomValueColumns[rng.IntN(len(randomValueColumns))]
		values[i].name += strconv.Itoa(i)
	}
	var b strings.Builder
	names, types := []string{"t"}, []string{tt.typ}
	for _, tag := range tags {
		names, types = append(names, tag), append(types, "String")
	}
	for _, v := range values {
		names, types = append(names, v.name), append(types, strings.ReplaceAll(v.typ, "'", `\'`))
	}
	b.WriteString(strings.Join(names, "\t") + "\n" + strings.Join(types, "\t") + "\n")
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	step := time.Minute
	if tt.dt == timeseries.DateSQL {
		step = 24 * time.Hour
	}
	for p := range rng.IntN(40) {
		at := tt.fmt(start.Add(time.Duration(p) * step))
		for s := range 1 + rng.IntN(4) {
			if rng.IntN(4) == 0 {
				continue
			}
			row := []string{at}
			for i := range tags {
				tag := tags[i] + strconv.Itoa(s)
				if rng.IntN(20) == 0 {
					tag = ""
				}
				row = append(row, tag)
			}
			for _, v := range values {
				text := v.value(rng)
				switch rng.IntN(15) {
				case 0:
					text = ""
				case 1:
					text = nullToken
				}
				row = append(row, text)
			}
			b.WriteString(strings.Join(row, "\t") + "\n")
		}
	}
	return []byte(b.String()), decoderTRQ("t", tt.dt, tags...)
}

func TestTSVDecoderErrors(t *testing.T) {
	bodies := map[string]string{
		"empty":       "",
		"blank":       "\n\n",
		"names only":  "t\tv\n",
		"field count": sqlHeader + "2024-01-01 00:00:00\ta\tr1\t1\t1\textra\n",
		"short row":   sqlHeader + "2024-01-01 00:00:00\ta\n",
		"no time":     "x\tv\nDateTime\tFloat64\n2024-01-01 00:00:00\t1\n",
		"ragged type": "t\tv\nDateTime\n2024-01-01 00:00:00\t1\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newTSVDecoder, streamtest.Case{
				TRQ: sqlTRQ(), Body: []byte(body), WantErr: streamtest.ErrAny,
			})
		})
	}
	_, err := UnmarshalTimeseries([]byte(sqlHeader), nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
	_, err = UnmarshalTimeseriesNative(nil, nil)
	require.ErrorIs(t, err, timeseries.ErrNoTimerangeQuery)
}

func decodeTSV(t *testing.T, body string, trq *timeseries.TimeRangeQuery) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseries([]byte(body), trq)
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func TestTSVDecoderDepartures(t *testing.T) {
	t.Run("a series' rows are sorted", func(t *testing.T) {
		s := decodeTSV(t, sqlHeader+"2024-01-01 00:01:00\ta\tr1\t2\t2\n2024-01-01 00:00:00\ta\tr1\t1\t1\n", sqlTRQ()).
			Results[0].SeriesList[0]
		require.True(t, s.IsSorted())
		require.Equal(t, []any{1.0, uint64(1)}, dspoints.Of(s)[0].Values)
	})
	t.Run("quotes are text", func(t *testing.T) {
		// ClickHouse's TSV escapes rather than quotes, so a quote is a value's own
		s := decodeTSV(t, "t\thost\tv\nDateTime\tString\tString\n2024-01-01 00:00:00\t\"a\"\tx\"y\n", sqlTRQ()).
			Results[0].SeriesList[0]
		require.Equal(t, dataset.Tags{"host": `"a"`}, s.Header.Tags)
		require.Equal(t, []any{`x"y`}, dspoints.Of(s)[0].Values)
	})
	t.Run("a series without a time is left out", func(t *testing.T) {
		ds := decodeTSV(t, sqlHeader+"nope\ta\tr1\t1\t1\n2024-01-01 00:00:00\tb\tr1\t2\t2\n", sqlTRQ())
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Equal(t, dataset.Tags{"host": "b", "region": "r1"}, ds.Results[0].SeriesList[0].Header.Tags)
		require.Empty(t, decodeTSV(t, sqlHeader+"nope\ta\tr1\t1\t1\n", sqlTRQ()).Results)
	})
	t.Run("NULL and empty differ", func(t *testing.T) {
		// \N is NULL, a tag left out or a null value; an empty cell is an empty string
		ds := decodeTSV(t, "t\thost\tregion\ts\tn\nDateTime\tNullable(String)\tString\tNullable(String)\tNullable(Int64)\n"+
			"2024-01-01 00:00:00\t\\N\t\t\\N\t\\N\n2024-01-01 00:00:00\t\t\\\\N\t\t\n", sqlTRQ())
		sl := ds.Results[0].SeriesList
		require.Len(t, sl, 2)
		require.Equal(t, dataset.Tags{"region": ""}, sl[0].Header.Tags)
		require.Equal(t, []any{nil, nil}, dspoints.Of(sl[0])[0].Values)
		require.Equal(t, dataset.Tags{"host": "", "region": `\N`}, sl[1].Header.Tags)
		require.Equal(t, []any{"", nil}, dspoints.Of(sl[1])[0].Values)
	})
	t.Run("compound and unknown types are text", func(t *testing.T) {
		s := decodeTSV(t, "t\ta\tm\tx\nDateTime\tArray(String)\tMap(String, UInt8)\tIntervalSecond\n"+
			"2024-01-01 00:00:00\t['a\\'b','c\\td']\t{'k':1}\t5\n", sqlTRQ()).Results[0].SeriesList[0]
		// ClickHouse's TSV writes a compound's literal as it is, its elements escaped within it
		require.Equal(t, []any{`['a\'b','c\td']`, "{'k':1}", "5"}, dspoints.Of(s)[0].Values)
	})
	t.Run("a FixedString is held without its padding", func(t *testing.T) {
		s := decodeTSV(t, "t\thost\tv\nDateTime\tFixedString(4)\tFixedString(4)\n2024-01-01 00:00:00\ta\\0\\0\\0\tbc\\0\\0\n",
			sqlTRQ()).Results[0].SeriesList[0]
		require.Equal(t, dataset.Tags{"host": "a"}, s.Header.Tags)
		require.Equal(t, []any{"bc"}, dspoints.Of(s)[0].Values)
	})
	t.Run("DateTimes are UTC", func(t *testing.T) {
		const body = "t\tv\tz\nDateTime\tDateTime64(3)\tDateTime('Asia/Tokyo')\n" +
			"2026-11-01T05:30:00Z\t2026-11-01T05:30:00.250Z\t1970-01-01T00:00:00Z\n" +
			"2026-11-01 01:30:01\t2026-11-01 01:30:00.250\t1970-01-01 09:00:00\n"
		at := epoch.Epoch(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC).UnixNano())
		ny, _ := LoadZone("America/New_York")
		// ISO text is UTC in any response; other text is in the column's zone, or else the response's
		for _, zone := range []*time.Location{nil, ny} {
			ts, err := stream.ReaderUnmarshaler(tsvDecoderIn(zone))(strings.NewReader(body), sqlTRQ())
			require.NoError(t, err)
			byTime := map[epoch.Epoch][]any{}
			for _, p := range dspoints.Of(ts.(*dataset.DataSet).Results[0].SeriesList[0]) {
				byTime[p.Epoch] = p.Values
			}
			second := at + 1e9
			values := []any{"2026-11-01 05:30:00.250", "1970-01-01 00:00:00"}
			if zone == nil {
				second, values = at-epoch.Epoch(4*time.Hour)+1e9, []any{"2026-11-01 01:30:00.250", "1970-01-01 00:00:00"}
			}
			require.Equal(t, map[epoch.Epoch][]any{
				at:     {"2026-11-01 05:30:00.250", "1970-01-01 00:00:00"},
				second: values,
			}, byTime)
		}
		// the response's zone comes with its format
		hr := timeseries.NewFormatHintReader(strings.NewReader(body), "TSVWithNamesAndTypes")
		hr.Timezone = "America/New_York"
		ts, err := UnmarshalTimeseriesAutoReader(hr, sqlTRQ())
		require.NoError(t, err)
		require.Equal(t, at+1e9, dspoints.Of(ts.(*dataset.DataSet).Results[0].SeriesList[0])[1].Epoch)
		// a native connection's response, which names no zone, is UTC
		hr = timeseries.NewFormatHintReader(strings.NewReader(body), "TSVWithNamesAndTypes")
		ts, err = UnmarshalTimeseriesAutoReader(hr, sqlTRQ())
		require.NoError(t, err)
		require.Equal(t, at-epoch.Epoch(4*time.Hour)+1e9, dspoints.Of(ts.(*dataset.DataSet).Results[0].SeriesList[0])[0].Epoch)
	})
}

func decodeNative(t *testing.T, body []byte, trq *timeseries.TimeRangeQuery) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseriesNative(body, trq)
	require.NoError(t, err)
	return ts.(*dataset.DataSet)
}

func TestNativeDecoderNulls(t *testing.T) {
	body := nativeBody(t, server.ServerRevision, []nativeColumnValues{
		{"t", "DateTime", minutes(0, 1)},
		{"host", "Nullable(String)", []any{nil, ""}},
		{"region", "LowCardinality(Nullable(String))", []any{nil, ""}},
		{"s", "Nullable(String)", []any{nil, ""}},
		{"l", "LowCardinality(Nullable(String))", []any{nil, ""}},
	})
	ds := decodeNative(t, body, sqlTRQ())
	sl := ds.Results[0].SeriesList
	require.Len(t, sl, 2)
	require.Equal(t, dataset.Tags{}, sl[0].Header.Tags)
	require.Equal(t, []any{nil, nil}, dspoints.Of(sl[0])[0].Values)
	require.Equal(t, dataset.Tags{"host": "", "region": ""}, sl[1].Header.Tags)
	require.Equal(t, []any{"", ""}, dspoints.Of(sl[1])[0].Values)
}

// nativeColumnValues is a column of a Native block: its name, type and values, as the encoder takes them
type nativeColumnValues struct {
	name, typ string
	values    []any
}

// nativeBody encodes each block as ClickHouse's Native format, with block info when revision is set
func nativeBody(t testing.TB, revision uint64, blocks ...[]nativeColumnValues) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, cols := range blocks {
		columns, values := make([]server.Column, len(cols)), make([][]any, len(cols))
		for i, c := range cols {
			columns[i], values[i] = server.Column{Name: c.name, Type: c.typ}, c.values
		}
		require.NoError(t, server.EncodeNativeFormat(&b, columns, values, uint64(len(cols[0].values)), revision))
	}
	return b.Bytes()
}

func minutes(n ...int) []any {
	out := make([]any, len(n))
	for i, m := range n {
		out[i] = time.Date(2024, 1, 1, 0, m, 0, 0, time.UTC)
	}
	return out
}

type nativeCase struct {
	blocks   [][]nativeColumnValues
	revision uint64
	trq      *timeseries.TimeRangeQuery
}

var groupedBlock = []nativeColumnValues{
	{"t", "DateTime", minutes(0, 0, 1, 1)},
	{"host", "String", []any{"a", "b", "a", "b"}},
	{"region", "LowCardinality(String)", []any{"r1", "r1", "r1", "r2"}},
	{"v", "Float64", []any{1.5, math.NaN(), math.Inf(-1), -0.0}},
	{"n", "UInt64", []any{uint64(1), uint64(2), uint64(math.MaxUint64), uint64(0)}},
}

// the Native bodies every feed of the stream decoder must decode alike
var nativeBodies = map[string]nativeCase{
	"grouped":       {[][]nativeColumnValues{groupedBlock}, server.ServerRevision, sqlTRQ()},
	"no block info": {[][]nativeColumnValues{groupedBlock}, 0, sqlTRQ()},
	"blocks": {[][]nativeColumnValues{groupedBlock, {
		{"t", "DateTime", minutes(2, 3)},
		{"host", "String", []any{"a", "c"}},
		{"region", "LowCardinality(String)", []any{"r1", "r1"}},
		{"v", "Float64", []any{2.5, 3.5}},
		{"n", "UInt64", []any{uint64(3), uint64(4)}},
	}}, server.ServerRevision, sqlTRQ()},
	"mis-sized block": {[][]nativeColumnValues{groupedBlock, {
		{"t", "DateTime", minutes(2)}, {"host", "String", []any{"a"}},
	}, {
		{"t", "DateTime", minutes(3)},
		{"host", "String", []any{"a"}},
		{"region", "LowCardinality(String)", []any{"r1"}},
		{"v", "Float64", []any{4.5}},
		{"n", "UInt64", []any{uint64(5)}},
	}}, server.ServerRevision, sqlTRQ()},
	"scalars": {[][]nativeColumnValues{{
		{"t", "DateTime('UTC')", minutes(0, 1, 2)},
		{"host", "Int32", []any{int32(-5), int32(-5), int32(-5)}},
		{"i8", "Int8", []any{int8(-128), int8(0), int8(127)}},
		{"i16", "Int16", []any{int16(-300), int16(0), int16(300)}},
		{"i64", "Int64", []any{int64(math.MinInt64), int64(0), int64(math.MaxInt64)}},
		{"u8", "UInt8", []any{uint8(0), uint8(1), uint8(255)}},
		{"u16", "UInt16", []any{uint16(65535), uint16(0), uint16(1)}},
		{"u32", "UInt32", []any{uint32(math.MaxUint32), uint32(0), uint32(1)}},
		{"f32", "Float32", []any{float32(0.1), float32(math.Inf(1)), float32(-3.25)}},
		{"b", "Bool", []any{true, false, true}},
		{"s", "String", []any{"x\ty", "", "\\N"}},
		{"fs", "FixedString(4)", []any{"ab", "abcd", ""}},
		{"d", "Date", minutes(0, 1, 2)},
		{"d32", "Date32", []any{time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(2299, 12, 31, 0, 0, 0, 0,
			time.UTC), time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"dt", "DateTime", minutes(0, 1, 2)},
		{"dt64", "DateTime64(6)", []any{time.Date(1969, 12, 31, 23, 59, 59, 999999000, time.UTC), time.Unix(0, 0),
			time.Date(2299, 1, 1, 0, 0, 0, 1000, time.UTC)}},
		{"dec", "Decimal(9, 2)", []any{"-14.83", "0.05", "0"}},
		{"dec128", "Decimal(38, 3)", []any{"123456789012345678901234.567", "-0.001", "1"}},
		{"e", "Enum8('a' = 1, 'b\\'c' = -2)", []any{"a", "b'c", "a"}},
		{"e16", "Enum16('x' = -300)", []any{"x", "x", "x"}},
		{"uuid", "UUID", []any{"61f0c404-5cb3-11e7-907b-a6006ad3dba0", "00000000-0000-0000-0000-000000000000",
			"ffffffff-ffff-ffff-ffff-ffffffffffff"}},
		{"ip4", "IPv4", []any{net.ParseIP("10.1.2.3"), net.ParseIP("0.0.0.0"), net.ParseIP("255.255.255.255")}},
		{"ip6", "IPv6", []any{net.ParseIP("2001:db8::1"), net.ParseIP("::ffff:1.2.3.4"), net.ParseIP("::")}},
		{"i128", "Int128", []any{bigInt("-170141183460469231731687303715884105728"), bigInt("0"), bigInt("1")}},
		{"u256", "UInt256", []any{bigInt("115792089237316195423570985008687907853269984665640564039457584007913129639935"),
			bigInt("0"), bigInt("7")}},
	}}, server.ServerRevision, decoderTRQ("t", timeseries.DateTimeSQL, "host")},
	"nullable": {[][]nativeColumnValues{{
		{"t", "Nullable(DateTime)", append(minutes(0, 1), nil)},
		{"host", "Nullable(String)", []any{"a", nil, "a"}},
		{"region", "LowCardinality(Nullable(String))", []any{nil, "r1", nil}},
		{"v", "Nullable(Float64)", []any{nil, 2.5, 3.5}},
		{"i", "Nullable(Int32)", []any{int32(1), nil, int32(3)}},
		{"s", "Nullable(String)", []any{nil, "x", "y"}},
		{"b", "Nullable(Bool)", []any{nil, true, false}},
		{"dt", "Nullable(DateTime)", []any{nil, time.Unix(1, 0), nil}},
	}}, server.ServerRevision, sqlTRQ()},
	"low cardinality": {[][]nativeColumnValues{{
		{"t", "LowCardinality(DateTime)", minutes(0, 0, 1)},
		{"host", "LowCardinality(String)", []any{"a", "b", "a"}},
		{"v", "LowCardinality(Float64)", []any{1.5, 1.5, 2.5}},
		{"n", "LowCardinality(UInt32)", []any{uint32(7), uint32(8), uint32(7)}},
	}}, server.ServerRevision, sqlTRQ()},
	"compound": {[][]nativeColumnValues{{
		{"t", "DateTime", minutes(0, 1)},
		{"host", "Array(String)", []any{"['a','b\\'c']", "[]"}},
		{"region", "Tuple(String, UInt8)", []any{"('x',1)", "('y',2)"}},
		{"m", "Map(String, UInt64)", []any{"{'k':1,'j':2}", "{}"}},
		{"arr", "Array(Nullable(UInt8))", []any{"[1,NULL]", "[]"}},
		{"ab", "Array(Bool)", []any{"[true,false]", "[]"}},
	}}, server.ServerRevision, sqlTRQ()},
	"time dateTime64": {[][]nativeColumnValues{{
		{"t", "DateTime64(3, 'UTC')", []any{time.UnixMilli(-1), time.UnixMilli(0), time.UnixMilli(1700000000123)}},
		{"v", "Float64", []any{1.0, 2.0, 3.0}},
	}}, server.ServerRevision, sqlTRQ()},
	"time dateTime64 nanos": {[][]nativeColumnValues{{
		{"t", "DateTime64(9)", []any{time.Unix(0, 1), time.Unix(0, 2), time.Unix(0, 3)}},
		{"v", "Float64", []any{1.0, 2.0, 3.0}},
	}}, server.ServerRevision, sqlTRQ()},
	"time dateTime64 seconds": {[][]nativeColumnValues{{
		// past 2262, a time wraps as time.Time.UnixNano wraps it
		{"t", "DateTime64(0)", []any{time.Date(2299, 12, 31, 0, 0, 0, 0, time.UTC), time.Unix(1, 0)}},
		{"v", "Float64", []any{1.0, 2.0}},
	}}, server.ServerRevision, sqlTRQ()},
	"time date": {[][]nativeColumnValues{{
		{"t", "Date", minutes(0, 60*24)},
		{"v", "Float64", []any{1.0, 2.0}},
	}}, server.ServerRevision, decoderTRQ("t", timeseries.DateSQL)},
	"time date32": {[][]nativeColumnValues{{
		{"t", "Date32", []any{time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"v", "Float64", []any{1.0, 2.0}},
	}}, server.ServerRevision, decoderTRQ("t", timeseries.DateSQL)},
	"time epoch": {[][]nativeColumnValues{{
		{"t", "UInt64", []any{uint64(1700000000), uint64(1700000001000), uint64(17), uint64(1700000002000000000)}},
		{"v", "Float64", []any{1.0, 2.0, 3.0, 4.0}},
	}}, server.ServerRevision, decoderTRQ("t", timeseries.DateTimeUnixSecs)},
	"time string": {[][]nativeColumnValues{{
		{"t", "String", []any{"2024-01-01 00:00:00", "2024-01-01 00:00:01.5", "bad"}},
		{"v", "Float64", []any{1.0, 2.0, 3.0}},
	}}, server.ServerRevision, decoderTRQ("t", timeseries.DateTimeSQL)},
	"time int": {[][]nativeColumnValues{{
		{"t", "Int64", []any{int64(1700000000000), int64(-5)}},
		{"v", "Float64", []any{1.0, 2.0}},
	}}, server.ServerRevision, decoderTRQ("t", timeseries.DateTimeUnixMilli)},
}

func nativeConformance(t *testing.T, body []byte, trq *timeseries.TimeRangeQuery) {
	t.Helper()
	streamtest.Conformance(t, newNativeDecoder, streamtest.Case{
		TRQ: trq, Body: body,
	})
}

func TestNativeDecoderConformance(t *testing.T) {
	for name, c := range nativeBodies {
		t.Run(name, func(t *testing.T) { nativeConformance(t, nativeBody(t, c.revision, c.blocks...), c.trq) })
	}
	// what follows an empty block, or a header that doesn't read, is ignored
	grouped := nativeBody(t, server.ServerRevision, groupedBlock)
	empty := nativeBody(t, server.ServerRevision, []nativeColumnValues{{"t", "DateTime", nil}})
	// a DateTime64 is a time from year 0 to 9999, and wraps as time.Time.UnixNano wraps it
	ticks := []int64{minTextSeconds, 1, maxTextSeconds, 2, minTextSeconds - 1}
	years := append(bytes.Clone(blockInfo), 2, byte(len(ticks)), 1, 't', 13)
	years = append(append(years, "DateTime64(0)"...), 0)
	for _, v := range ticks {
		years = binary.LittleEndian.AppendUint64(years, uint64(v))
	}
	years = append(append(years, 1, 'v', 7), "Float64"...)
	years = append(years, 0)
	for i := range ticks {
		years = binary.LittleEndian.AppendUint64(years, math.Float64bits(float64(i)))
	}
	t.Run("years", func(t *testing.T) { nativeConformance(t, years, sqlTRQ()) })
	// a tick of a finer DateTime64 before year 0 isn't a time, and a Bool byte past 1 isn't a bool
	fine := append(bytes.Clone(blockInfo), 2, 3, 1, 't', 13)
	fine = append(append(fine, "DateTime64(3)"...), 0)
	for _, v := range []int64{minTextSeconds*1000 - 1, 0, 1} {
		fine = binary.LittleEndian.AppendUint64(fine, uint64(v))
	}
	fine = append(append(fine, 1, 'b', 4), "Bool"...)
	fine = append(fine, 0, 1, 2, 0)
	t.Run("ticks and bools", func(t *testing.T) { nativeConformance(t, fine, sqlTRQ()) })
	for name, tail := range map[string][]byte{
		"empty block":        append(bytes.Clone(empty), grouped...),
		"zero columns":       {1, 0, 2, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 7},
		"unknown block info": {1, 0, 7, 0, 3},
		"overlong varint":    bytes.Repeat([]byte{0xff}, 12),
		"cut header":         {1, 0, 2},
		"cut count":          {5, 0x80},
	} {
		t.Run(name, func(t *testing.T) { nativeConformance(t, append(bytes.Clone(grouped), tail...), sqlTRQ()) })
	}
}

func TestNativeDecoderConformanceAtScale(t *testing.T) {
	rng := weaktest.NewRand(6, 6)
	for trial := range 40 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) {
			body, trq := randomNative(t, rng)
			nativeConformance(t, body, trq)
		})
	}
}

// the Native types of random bodies, each with a random value
var randomNativeTypes = []struct {
	typ   string
	value func(rng *weaktest.Rand) any
}{
	{"Float64", func(rng *weaktest.Rand) any { return rng.NormFloat64() * 1e6 }},
	{"Float32", func(rng *weaktest.Rand) any { return float32(rng.NormFloat64()) }},
	{"Int64", func(rng *weaktest.Rand) any { return rng.Int64() - math.MaxInt64/2 }},
	{"UInt32", func(rng *weaktest.Rand) any { return rng.Uint32() }},
	{"Int16", func(rng *weaktest.Rand) any { return int16(rng.IntN(65536) - 32768) }},
	{"Bool", func(rng *weaktest.Rand) any { return rng.IntN(2) == 0 }},
	{"String", func(rng *weaktest.Rand) any { return []string{"a", "", "b\tc", "\\N"}[rng.IntN(4)] }},
	{"Nullable(Float64)", func(rng *weaktest.Rand) any {
		if rng.IntN(3) == 0 {
			return nil
		}
		return rng.Float64()
	}},
	{"Nullable(String)", func(rng *weaktest.Rand) any {
		if rng.IntN(3) == 0 {
			return nil
		}
		return "s"
	}},
	{"LowCardinality(String)", func(rng *weaktest.Rand) any { return []string{"x", "y", "z"}[rng.IntN(3)] }},
	{"Decimal(18, 4)", func(rng *weaktest.Rand) any { return strconv.Itoa(rng.IntN(1e6)-5e5) + ".0625" }},
	{"DateTime64(3)", func(rng *weaktest.Rand) any { return time.UnixMilli(rng.Int64N(4e12)) }},
}

// randomNative writes the blocks of a GROUP BY time, host response, each series in time order
func randomNative(t testing.TB, rng *weaktest.Rand) ([]byte, *timeseries.TimeRangeQuery) {
	t.Helper()
	timeTypes := []struct {
		typ string
		dt  timeseries.FieldDataType
		at  func(m int) any
	}{
		{"DateTime", timeseries.DateTimeSQL, func(m int) any { return time.Unix(int64(1700000000+60*m), 0) }},
		{"DateTime64(3)", timeseries.DateTimeSQL, func(m int) any { return time.UnixMilli(int64(1700000000000 + 60001*m)) }},
		{"UInt64", timeseries.DateTimeUnixMilli, func(m int) any { return uint64(1700000000000 + 60000*m) }},
		{"Date", timeseries.DateSQL, func(m int) any { return time.Unix(int64(86400*(19000+m)), 0) }},
	}
	tt := timeTypes[rng.IntN(len(timeTypes))]
	tags := rng.IntN(2) == 0
	nvals := 1 + rng.IntN(3)
	kinds := make([]int, nvals)
	for i := range kinds {
		kinds[i] = rng.IntN(len(randomNativeTypes))
	}
	revision := []uint64{0, server.ServerRevision}[rng.IntN(2)]
	var body []byte
	minute := 0
	for range 1 + rng.IntN(3) {
		cols := []nativeColumnValues{{name: "t", typ: tt.typ}}
		if tags {
			cols = append(cols, nativeColumnValues{name: "host", typ: "LowCardinality(String)"})
		}
		for i, k := range kinds {
			cols = append(cols, nativeColumnValues{name: "v" + strconv.Itoa(i), typ: randomNativeTypes[k].typ})
		}
		for range 1 + rng.IntN(30) {
			for h := range 1 + rng.IntN(3) {
				if !tags && h > 0 {
					break
				}
				cols[0].values = append(cols[0].values, tt.at(minute))
				c := 1
				if tags {
					cols[1].values = append(cols[1].values, "h"+strconv.Itoa(h))
					c++
				}
				for i, k := range kinds {
					cols[c+i].values = append(cols[c+i].values, randomNativeTypes[k].value(rng))
				}
			}
			minute++
		}
		body = append(body, nativeBody(t, revision, cols)...)
	}
	trq := decoderTRQ("t", tt.dt)
	if tags {
		trq = decoderTRQ("t", tt.dt, "host")
	}
	return body, trq
}

func TestNativeDecoderErrors(t *testing.T) {
	grouped := nativeBody(t, server.ServerRevision, groupedBlock)
	block := func(revision uint64, cols ...nativeColumnValues) []byte {
		return nativeBody(t, revision, cols)
	}
	info := func(b ...byte) []byte { return append(bytes.Clone(blockInfo), b...) }
	uint64Col := []byte{1, 't', 6, 'U', 'I', 'n', 't', '6', '4'}
	bodies := map[string][]byte{
		"cut name":      info(2, 1),
		"cut type":      info(1, 1, 1, 't'),
		"cut flag":      info(append([]byte{1, 1}, uint64Col...)...),
		"cut values":    grouped[:len(grouped)-3],
		"custom flag":   info(append(append([]byte{1, 1}, uint64Col...), 1, 0, 0, 0, 0, 0, 0, 0, 0)...),
		"no time":       block(0, nativeColumnValues{"x", "DateTime", minutes(0)}, nativeColumnValues{"v", "Float64", []any{1.0}}),
		"variant":       {2, 1, 1, 't', 8, 'D', 'a', 't', 'e', 'T', 'i', 'm', 'e', 0, 0, 0, 0, 1, 'v', 7, 'D', 'y', 'n', 'a', 'm', 'i', 'c', 0},
		"precision":     info(append([]byte{1, 1, 1, 't', 14}, append([]byte("DateTime64(10)"), 0, 0, 0, 0, 0, 0, 0, 0, 0)...)...),
		"cut blocks":    append(bytes.Clone(grouped), grouped[:40]...),
		"key version":   lowCardinalityBlock(2, 1<<9, 1, 1, 0),
		"key width":     lowCardinalityBlock(1, 1<<9|4, 1, 1, 0),
		"global dict":   lowCardinalityBlock(1, 1<<9|1<<8, 1, 1, 0),
		"index":         lowCardinalityBlock(1, 1<<9, 1, 1, 1),
		"wide index":    lowCardinalityBlock(1, 1<<9|1, 1, 1, 1, 0),
		"row count":     lowCardinalityBlock(1, 1<<9, 1, 2, 0),
		"cut dict":      lowCardinalityBlock(1, 1<<9, 5, 1, 0),
		"map arity":     info(append([]byte{1, 1, 1, 't', 11}, append([]byte("Map(String)"), 0, 1, 0, 0, 0, 0, 0, 0, 0)...)...),
		"map offsets":   info(append([]byte{1, 2, 1, 't', 18}, append([]byte("Map(String, UInt8)"), 0, 2, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0)...)...),
		"cut map keys":  info(append([]byte{1, 1, 1, 't', 18}, append([]byte("Map(String, UInt8)"), 0, 1, 0, 0, 0, 0, 0, 0, 0)...)...),
		"cut map vals":  info(append([]byte{1, 1, 1, 't', 18}, append([]byte("Map(String, UInt8)"), 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 'k')...)...),
		"nested prefix": info(append([]byte{1, 1, 1, 't', 34}, append([]byte("Tuple(LowCardinality(String), Int8)"), 0, 1)...)...),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newNativeDecoder, streamtest.Case{
				TRQ: sqlTRQ(), Body: body, WantErr: streamtest.ErrAny,
			})
		})
	}
}

// the block info of a block that isn't a two-level aggregation's, which a client that sends its
// protocol version is given
var blockInfo = []byte{1, 0, 2, 0xff, 0xff, 0xff, 0xff, 0}

// lowCardinalityBlock is a block of rows rows whose time is a LowCardinality(String) column, with
// the provided key version, dictionary flags, dictionary size and indexes
func lowCardinalityBlock(version, flags uint64, size byte, rows byte, index ...byte) []byte {
	b := append(bytes.Clone(blockInfo), 1, rows, 1, 't', 22)
	b = append(b, "LowCardinality(String)"...)
	b = append(b, 0, byte(version), 0, 0, 0, 0, 0, 0, 0)
	b = append(b, byte(flags), byte(flags>>8), 0, 0, 0, 0, 0, 0)
	b = append(b, size, 0, 0, 0, 0, 0, 0, 0, 1, 'x') // a dictionary of size, of which one arrives
	b = append(b, rows, 0, 0, 0, 0, 0, 0, 0)
	return append(b, index...)
}

func TestNativeDecoderKeepsTheOneNaN(t *testing.T) {
	// a NaN is read as its text reads, whatever its payload
	body := []byte{2, 1, 1, 't', 8, 'D', 'a', 't', 'e', 'T', 'i', 'm', 'e', 0, 0, 0, 0, 1, 'v', 7, 'F', 'l', 'o', 'a', 't',
		'6', '4'}
	body = binary.LittleEndian.AppendUint64(body, 0x7ff8000000000abc)
	ts, err := UnmarshalTimeseriesNative(body, sqlTRQ())
	require.NoError(t, err)
	v := dspoints.Of(ts.(*dataset.DataSet).Results[0].SeriesList[0])[0].Values[0].(float64)
	require.Equal(t, math.Float64bits(math.NaN()), math.Float64bits(v))
}

func TestNativeDecoderReadsNoRowsAsNoResults(t *testing.T) {
	// ClickHouse's HTTP interface answers with an empty body, and a native connection with a block of no rows
	for name, body := range map[string][]byte{
		"empty":       {},
		"header only": nativeBody(t, server.ServerRevision, []nativeColumnValues{{"t", "DateTime", nil}}),
	} {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newNativeDecoder, streamtest.Case{
				TRQ: sqlTRQ(), Body: body,
				Want: &dataset.DataSet{ExtentList: timeseries.ExtentList{sqlTRQ().Extent}, Results: dataset.Results{}},
			})
		})
	}
	// a body whose first block doesn't read is no response at all
	_, err := UnmarshalTimeseriesNative([]byte{0x80}, sqlTRQ())
	require.ErrorIs(t, err, timeseries.ErrInvalidBody)
}

func TestNativeColumnEdges(t *testing.T) {
	// values that fail cleanly: offsets past the input, and a key width of zero
	for _, c := range []struct {
		typ  string
		data []byte
	}{
		{"Array(UInt8)", []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x0f}},
		{"LowCardinality(String)", []byte{0x40, 2, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 'x'}},
		{"LowCardinality(String)", []byte{0, 2, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0}},
		{"LowCardinality(String)", []byte{0, 2, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 'x', 1, 0, 0}},
		{"Nullable(UInt8)", nil},
	} {
		_, err := readTexts(c.data, c.typ, 1)
		require.Error(t, err, c.typ)
	}
	// a null within a null is null
	texts, err := readTexts([]byte{0, 1, 1, 0, 7, 8}, "Nullable(Nullable(UInt8))", 2)
	require.NoError(t, err)
	require.Equal(t, []string{nullToken, nullToken}, texts)
	texts, err = readTexts(append([]byte{2, 0, 0, 0, 0, 0, 0, 0}, 3, 'a', '\n', 0, 4, '\t', 'b', '\'', '\\'),
		"Array(String)", 1)
	require.NoError(t, err)
	require.Equal(t, []string{`['a\n\0','\tb\'\\']`}, texts)
	// indexes of each width
	dict := []byte{2, 0, 0, 0, 0, 0, 0, 0, 1, 'x', 1, 'y', 2, 0, 0, 0, 0, 0, 0, 0}
	for width, indexes := range [][]byte{{1, 0}, {1, 0, 0, 0}, {1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, make([]byte, 16)} {
		if width == 3 {
			indexes[0] = 1
		}
		texts, err = readTexts(append(append([]byte{byte(width), 2, 0, 0, 0, 0, 0, 0}, dict...), indexes...),
			"LowCardinality(String)", 2)
		require.NoError(t, err)
		require.Equal(t, []string{"y", "x"}, texts, width)
	}
	// a row count that isn't the block's, or doesn't arrive
	_, err = readTexts([]byte{0, 2, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 'x', 3, 0, 0, 0, 0, 0, 0, 0, 0},
		"LowCardinality(String)", 1)
	require.Error(t, err)
	_, err = readTexts([]byte{0, 2, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 'x', 1}, "LowCardinality(String)", 1)
	require.Error(t, err)
	// an empty array of a LowCardinality type holds no dictionary
	var col nativeColumn
	require.NoError(t, col.readColumn(testCursor([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}),
		"Array(LowCardinality(String))", 1))
	require.Equal(t, []string{"[]"}, col.texts(1))
	_, err = readTexts([]byte{1, 0, 0, 0, 0, 0, 0, 0, 1}, "Map(Variant(UInt8), UInt8)", 1)
	require.ErrorIs(t, err, errUnsupportedColumnType)
	require.Error(t, col.readColumn(testCursor([]byte{1, 0}), "LowCardinality(String)", 1))
	require.Error(t, col.readColumn(testCursor([]byte{1, 0}), "Map(String, LowCardinality(String))", 1))
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	// a decoded DataSet must not change when the buffer it was decoded from is reused
	check := func(body []byte, unmarshal timeseries.UnmarshalerFunc, trq *timeseries.TimeRangeQuery) {
		pristine := bytes.Clone(body)
		ts, err := unmarshal(body, trq)
		require.NoError(t, err)
		for i := range body {
			body[i] = 'x'
		}
		want, err := unmarshal(pristine, trq)
		require.NoError(t, err)
		require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), ts.(*dataset.DataSet), streamtest.CompareOptions{}))
	}
	check(tsvBody(20, 30), UnmarshalTimeseries, sqlTRQ())
	check([]byte(tsvBodies["types"].body), UnmarshalTimeseries, tsvBodies["types"].trq)
	for _, name := range []string{"scalars", "nullable", "low cardinality", "compound"} {
		c := nativeBodies[name]
		check(nativeBody(t, c.revision, c.blocks...), UnmarshalTimeseriesNative, c.trq)
	}
}

func TestUnmarshalTimeseriesAuto(t *testing.T) {
	tsv := tsvBody(2, 3)
	native := nativeBody(t, server.ServerRevision, nativeBlock(0, 2, 3))
	fromTSV, err := UnmarshalTimeseries(tsv, sqlTRQ())
	require.NoError(t, err)
	fromNative, err := UnmarshalTimeseriesNative(native, sqlTRQ())
	require.NoError(t, err)
	// the format a response names picks its decoder, and TSV is the default
	for _, c := range []struct {
		want   timeseries.Timeseries
		reader io.Reader
	}{
		{fromTSV, bytes.NewReader(tsv)},
		{fromTSV, timeseries.NewFormatHintReader(bytes.NewReader(tsv), "TSVWithNamesAndTypes")},
		{fromNative, timeseries.NewFormatHintReader(bytes.NewReader(native), "native")},
	} {
		got, err := UnmarshalTimeseriesAutoReader(c.reader, sqlTRQ())
		require.NoError(t, err)
		require.NoError(t, streamtest.Compare(c.want.(*dataset.DataSet), got.(*dataset.DataSet),
			streamtest.CompareOptions{}))
	}
	got, err := UnmarshalTimeseriesAuto(tsv, sqlTRQ())
	require.NoError(t, err)
	require.NoError(t, streamtest.Compare(fromTSV.(*dataset.DataSet), got.(*dataset.DataSet), streamtest.CompareOptions{}))
}

func TestNativeDecoderReadsBlocksAsTheyArrive(t *testing.T) {
	body := nativeBody(t, server.ServerRevision, nativeBlock(0, 10, 1000), nativeBlock(1000, 10, 1000))
	dec, err := newNativeDecoder(sqlTRQ())
	require.NoError(t, err)
	n := dec.(*nativeDecoder)
	// the first block is read once it has arrived, and the second waits for the rest of it
	half := len(body) / 2
	_, err = dec.Write(body[:half+10])
	require.NoError(t, err)
	require.Equal(t, 1, n.blocks)
	require.Less(t, len(n.buf), half)
	_, err = dec.ReadFrom(bytes.NewReader(body[half+10:]))
	require.NoError(t, err)
	require.Equal(t, 2, n.blocks)
	ts, err := dec.Finish()
	require.NoError(t, err)
	require.Len(t, ts.(*dataset.DataSet).Results[0].SeriesList, 10)
	_, err = dec.Finish()
	require.ErrorIs(t, err, stream.ErrFinished)
	_, err = dec.Write(nil)
	require.ErrorIs(t, err, stream.ErrFinished)
	_, err = dec.ReadFrom(bytes.NewReader(nil))
	require.ErrorIs(t, err, stream.ErrFinished)
	// a failed read fails the decode
	dec, _ = newNativeDecoder(sqlTRQ())
	_, err = dec.ReadFrom(io.MultiReader(bytes.NewReader(body[:half]), &failingReader{}))
	require.Error(t, err)
	_, err = dec.Write(body[half:])
	require.Error(t, err)
	_, err = dec.Finish()
	require.Error(t, err)
}

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

// tsvBody writes a GROUP BY time, host response of series hosts, each with points rows
func tsvBody(series, points int) []byte {
	var b strings.Builder
	b.WriteString(sqlHeader)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for p := range points {
		at := start.Add(time.Duration(p) * time.Minute).Format(dateTimeLayout)
		for s := range series {
			b.WriteString(at + "\thost-" + strconv.Itoa(s) + "\tr1\t" + strconv.FormatFloat(float64((s*p)%97)/7, 'f', -1, 64) +
				"\t" + strconv.Itoa(s*p) + "\n")
		}
	}
	return []byte(b.String())
}

// nativeBlock is a block of the same response as tsvBody's, from its first point
func nativeBlock(first, series, points int) []nativeColumnValues {
	cols := []nativeColumnValues{{name: "t", typ: "DateTime"}, {name: "host", typ: "LowCardinality(String)"},
		{name: "region", typ: "LowCardinality(String)"}, {name: "v", typ: "Float64"}, {name: "n", typ: "UInt64"}}
	for p := first; p < first+points; p++ {
		for s := range series {
			cols[0].values = append(cols[0].values, time.Unix(int64(1704067200+60*p), 0))
			cols[1].values = append(cols[1].values, "host-"+strconv.Itoa(s))
			cols[2].values = append(cols[2].values, "r1")
			cols[3].values = append(cols[3].values, float64((s*p)%97)/7)
			cols[4].values = append(cols[4].values, uint64(s*p))
		}
	}
	return cols
}

func BenchmarkDecoder(b *testing.B) {
	trq := sqlTRQ()
	for _, shape := range []struct {
		name           string
		series, points int
	}{
		{"100x1000", 100, 1000},
		{"10000x10", 10000, 10},
	} {
		tsv := tsvBody(shape.series, shape.points)
		b.Run("tsv/"+shape.name+"/stream", func(b *testing.B) {
			streamtest.Bench(b, UnmarshalTimeseriesReader, trq, tsv)
		})
		var native []byte
		// ClickHouse's blocks hold 65,409 rows at most
		for first := 0; first < shape.points; first += max(1, 65409/shape.series) {
			native = append(native, nativeBody(b, server.ServerRevision,
				nativeBlock(first, shape.series, min(max(1, 65409/shape.series), shape.points-first)))...)
		}
		b.Run("native/"+shape.name+"/stream", func(b *testing.B) {
			streamtest.Bench(b, UnmarshalTimeseriesNativeReader, trq, native)
		})
	}
}
