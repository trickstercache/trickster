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
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

// each value type's valid literals, then literals it rejects
var typeLiterals = map[string][2][]string{
	"Int8":    {{"-128", "127", "0"}, {"128", "1.5", `"1"`, "true"}},
	"Int16":   {{"-32768", "32767"}, {"32768"}},
	"Int32":   {{"-2147483648", "2147483647"}, {"2147483648"}},
	"Int64":   {{"-9223372036854775808", "9223372036854775807", "-0"}, {"9223372036854775808", "1e3"}},
	"UInt8":   {{"0", "255"}, {"256", "-1"}},
	"UInt16":  {{"65535"}, {"65536"}},
	"UInt32":  {{"4294967295"}, {"4294967296"}},
	"UInt64":  {{"18446744073709551615", "9007199254740993"}, {"18446744073709551616", `"7"`}},
	"Float32": {{"0.1", "3.4e38", "-1e-45"}, {"3.5e38", `"0.1"`}},
	"Float64": {{"1.25e-20", "-0.0", "1e308", "9007199254740993"}, {"1e1000", "true"}},
	"String":  {{`"a"`, `"a\u0000b"`, `"\ud800"`, `"é\"\\"`, `""`, "\"\xff\""}, {"1", "false", "{}"}},
	"Boolean": {{"true", "false"}, {"1", `"true"`}},
	"Null":    {{"null"}, {"1", `""`}},
}

var valueTypes = []string{
	"Int8", "Int16", "Int32", "Int64", "UInt8", "UInt16", "UInt32", "UInt64",
	"Float32", "Float64", "String", "Boolean", "Null",
}

// each time type and unit, and its literal for a second
var timeTypes = []struct {
	typ  string
	unit timeseries.FieldDataType
	at   func(sec int64) string
}{
	{"TimestampSecond", timeseries.DateTimeRFC3339Nano, func(s int64) string { return strconv.FormatInt(s, 10) }},
	{"TimestampMillisecond", timeseries.DateTimeRFC3339Nano, func(s int64) string { return strconv.FormatInt(s*1e3, 10) }},
	{"TimestampMicrosecond", timeseries.DateTimeRFC3339Nano, func(s int64) string { return strconv.FormatInt(s*1e6, 10) }},
	{"TimestampNanosecond", timeseries.DateTimeRFC3339Nano, func(s int64) string { return strconv.FormatInt(s*1e9, 10) }},
	{"Int32", timeseries.DateTimeUnixSecs, func(s int64) string { return strconv.FormatInt(s, 10) }},
	{"Int64", timeseries.DateTimeUnixMilli, func(s int64) string { return strconv.FormatInt(s*1e3, 10) }},
	{"UInt64", timeseries.DateTimeUnixMicro, func(s int64) string { return strconv.FormatInt(s*1e6, 10) }},
	{"Float64", timeseries.DateTimeUnixSecs, func(s int64) string { return strconv.FormatInt(s, 10) + ".000" }},
}

// tags, some of which encoding/json writes otherwise; the fourth and seventh are one series
var hosts = []string{`"h0"`, `"h<"`, `"h>"`, `"h\u2028"`, `"h\"q"`, `"&"`, "\"h\xe2\x80\xa8\"", "\"h\xe2\x80\xa9\""}

// a time that some unit rejects: off the step, of a finer unit, out of range, or not a number
var badTimes = []string{"1", "1.5", "-1", "9223372036854775807", "null", `"1"`, "1e3"}

// typedBody returns a response of every value type, its query, and whether it has a value or time it rejects
func typedBody(rng *weaktest.Rand) ([]byte, *timeseries.TimeRangeQuery) {
	tt := timeTypes[rng.IntN(len(timeTypes))]
	columns := []string{`{"name":"time","data_type":"` + tt.typ + `"}`, `{"name":"host","data_type":"String"}`}
	types := make([]string, 0, len(valueTypes))
	names := make([]string, 0, len(valueTypes))
	for i, typ := range valueTypes {
		if rng.IntN(3) == 0 {
			continue
		}
		types = append(types, typ)
		names = append(names, "v"+strconv.Itoa(i))
		columns = append(columns, `{"name":"`+names[len(names)-1]+`","data_type":"`+typ+`"}`)
	}
	if len(types) == 0 {
		types, names = append(types, "Int64"), append(names, "v")
		columns = append(columns, `{"name":"v","data_type":"Int64"}`)
	}
	bad := -1
	rows := 3 + rng.IntN(20)
	if rng.IntN(3) == 0 {
		bad = rng.IntN(rows)
	}
	var b strings.Builder
	for r := range rows {
		if r > 0 {
			b.WriteByte(',')
		}
		at := tt.at(int64(r / 2))
		if r == bad && rng.IntN(3) == 0 {
			at = badTimes[rng.IntN(len(badTimes))]
		}
		fmt.Fprintf(&b, `[%s,%s`, at, hosts[r%len(hosts)])
		badCol := -1
		if r == bad {
			badCol = rng.IntN(len(types))
		}
		for c, typ := range types {
			choices := typeLiterals[typ]
			v := choices[0][rng.IntN(len(choices[0]))]
			switch {
			case c == badCol:
				v = choices[1][rng.IntN(len(choices[1]))]
			case rng.IntN(5) == 0:
				v = "null"
			}
			b.WriteString("," + v)
		}
		b.WriteByte(']')
	}
	body := `{"output":[{"records":{"schema":{"column_schemas":[` + strings.Join(columns, ",") +
		`]},"rows":[` + b.String() + `],"total_rows":` + strconv.Itoa(rows) + `}}],"execution_time_ms":1}`
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: "fixture", OutputColumn: "time", GroupColumns: []string{"host"},
		ValueColumns: names, Step: time.Second, OutputUnit: tt.unit,
	}
	trq := sqlanalyzer.NewTimeRangeQuery("fixture")
	plan.ApplyToQuery(trq)
	trq.Extent = timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(int64(rows), 0)}
	return []byte(body), trq
}

func unwrapSQL(ts timeseries.Timeseries) *dataset.DataSet { return ts.(*dataSet).DataSet }

func TestDecoderMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(21, 21)
	legacy := stream.BytesUnmarshaler(newLegacyDecoder)
	var failed, decoded int
	for range 400 {
		body, trq := typedBody(rng)
		want, werr := legacy(body, trq.Clone())
		got, err := UnmarshalTimeseries(body, trq.Clone())
		if werr != nil {
			failed++
			require.Error(t, err, "%s", body)
			require.Equal(t, werr.Error(), err.Error(), "%s", body)
			continue
		}
		decoded++
		require.NoError(t, err, "%s", body)
		require.NoError(t, streamtest.Compare(unwrapSQL(want), unwrapSQL(got), streamtest.CompareOptions{IgnoreSizes: true}), "%s", body)
		require.Equal(t, want.(*dataSet).fields, got.(*dataSet).fields)
		if decoded%10 == 0 {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: trq, Body: body, Unwrap: unwrapSQL,
				Legacy: stream.ReaderUnmarshaler(newLegacyDecoder),
			})
		}
	}
	// both outcomes are common
	require.Greater(t, failed, 50)
	require.Greater(t, decoded, 150)
}

func TestDecoderFailsAsLegacy(t *testing.T) {
	legacy := stream.BytesUnmarshaler(newLegacyDecoder)
	failed := 0
	check := func(body []byte, trq *timeseries.TimeRangeQuery) {
		t.Helper()
		want, werr := legacy(body, trq.Clone())
		got, err := UnmarshalTimeseries(body, trq.Clone())
		if werr == nil {
			require.NoError(t, err, "%s", body)
			require.NoError(t, streamtest.Compare(unwrapSQL(want), unwrapSQL(got), streamtest.CompareOptions{IgnoreSizes: true}))
			return
		}
		failed++
		require.Error(t, err, "%s", body)
		require.Equal(t, werr.Error(), err.Error(), "%s", body)
	}
	// every time type takes or rejects each bad time as it did
	for _, tt := range timeTypes {
		for _, at := range badTimes {
			body := []byte(`{"output":[{"records":{"schema":{"column_schemas":[{"name":"time","data_type":"` + tt.typ +
				`"},{"name":"v","data_type":"Int64"}]},"rows":[[` + at + `,1]],"total_rows":1}}],"execution_time_ms":1}`)
			plan := &sqlanalyzer.QueryPlan{
				CanonicalSQL: "fixture", OutputColumn: "time", ValueColumns: []string{"v"},
				Step: time.Second, OutputUnit: tt.unit,
			}
			trq := sqlanalyzer.NewTimeRangeQuery("fixture")
			plan.ApplyToQuery(trq)
			check(body, trq)
		}
	}
	// a tag of another type holding a string
	for _, typ := range []string{"Int64", "Boolean", "Float64"} {
		body := []byte(`{"output":[{"records":{"schema":{"column_schemas":[{"name":"time","data_type":"TimestampSecond"},` +
			`{"name":"host","data_type":"` + typ + `"},{"name":"value","data_type":"Int64"}]},"rows":[[1,"5",1]],"total_rows":1}}],` +
			`"execution_time_ms":1}`)
		check(body, query())
	}
	require.Greater(t, failed, 50)
}

func BenchmarkDecoder(b *testing.B) {
	const series, points = 100, 1000
	var rows strings.Builder
	for i := range series * points {
		if i > 0 {
			rows.WriteByte(',')
		}
		fmt.Fprintf(&rows, `[%d,"h%d",%d.25,%d,"s%d",%t]`, (i/series)*1000, i%series, i%9973, i, i%7, i%2 == 0)
	}
	body := []byte(`{"output":[{"records":{"schema":{"column_schemas":[{"name":"time","data_type":"TimestampMillisecond"},` +
		`{"name":"host","data_type":"String"},{"name":"v","data_type":"Float64"},{"name":"n","data_type":"Int64"},` +
		`{"name":"s","data_type":"String"},{"name":"b","data_type":"Boolean"}]},"rows":[` + rows.String() +
		`],"total_rows":` + strconv.Itoa(series*points) + `}}],"execution_time_ms":1}`)
	plan := &sqlanalyzer.QueryPlan{
		CanonicalSQL: "fixture", OutputColumn: "time", GroupColumns: []string{"host"},
		ValueColumns: []string{"v", "n", "s", "b"}, Step: time.Second, OutputUnit: timeseries.DateTimeRFC3339Nano,
	}
	trq := sqlanalyzer.NewTimeRangeQuery("fixture")
	plan.ApplyToQuery(trq)
	for name, decode := range map[string]timeseries.UnmarshalerFunc{
		"legacy": stream.BytesUnmarshaler(newLegacyDecoder), "stream": UnmarshalTimeseries,
	} {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := decode(body, trq); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
