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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/stretchr/testify/require"
)

const (
	testBucket = "bucket"
	testHost   = "host"
	testValue  = "value"
	testCount  = "count"
	testPage   = "page"
)

func TestAppendJacksonString(t *testing.T) {
	for in, want := range map[string]string{
		"":                      `""`,
		"plain é<>&/\u2028\x7f": "\"plain é<>&/\u2028\x7f\"",
		"\"\\":                  `"\"\\"`,
		"\b\t\n\f\r\x00\x1f":    `"\b\t\n\f\r\u0000\u001F"`,
		"a𝄞b😀":                  `"a\uD834\uDD1Eb\uD83D\uDE00"`,
	} {
		require.Equal(t, want, string(appendJacksonString(nil, in)), "%q", in)
	}
}

func TestJavaMapOrder(t *testing.T) {
	order := func(capacity int, keys ...string) []string {
		cells := make([]druidCell, len(keys))
		for i, k := range keys {
			cells[i] = druidCell{name: k}
		}
		javaMapOrder(cells, capacity)
		out := make([]string, len(cells))
		for i := range cells {
			out[i] = cells[i].name
		}
		return out
	}
	// as Druid 37 wrote these groupBy events, timeseries results and topN rows
	require.Equal(t, []string{"fare", "payment_type", "pax", "cab_type", "cnt"},
		order(javaMapCapacity, "cab_type", "payment_type", "cnt", "fare", "pax"))
	require.Equal(t, []string{"fare", "pc", "pax", "cnt", "pickup_neighborhood_name"},
		order(javaMapCapacity, "pc", "pickup_neighborhood_name", "cnt", "fare", "pax"))
	require.Equal(t, []string{"cnt", "fare", "pickup_neighborhood_name", "pax"},
		order(javaMapCapacityFor(4), "pickup_neighborhood_name", "cnt", "fare", "pax"))
	// a map that outgrows its table iterates as its grown table would, keys sharing a bucket as given
	require.Equal(t, []string{"zz5924", "Q5924", "f5924", "c5924", "p5924", "k5924"},
		order(javaMapCapacityFor(5), "c5924", "f5924", "p5924", "zz5924", "Q5924", "k5924"))
	for n, want := range map[int]int{0: 16, 1: 2, 2: 4, 3: 8, 5: 8, 6: 16, 11: 16, 12: 32, 24: 64} {
		require.Equal(t, want, javaMapCapacityFor(n), "%d", n)
	}
	// a supplementary character hashes as its two UTF-16 code units
	require.Equal(t, int((uint32(0xd834*31+0xdd1e)^uint32(0xd834*31+0xdd1e)>>16)&0xff), javaMapBucket("𝄞", 256))
}

func TestSQLTagOutputValue(t *testing.T) {
	for _, c := range []struct {
		tag  string
		want any
		json string
	}{
		{"null", nil, "null"},
		{"true", true, "true"},
		{"false", false, "false"},
		{`"a b"`, "a b", `"a b"`},
		{`"a\"b"`, `a"b`, `"a\"b"`},
		{`"\u0041"`, "A", `"\u0041"`},
		{"-5", int64(-5), "-5"},
		{"0", int64(0), "0"},
		{"1.50", 1.5, "1.50"},
		{`{"a":1}`, map[string]any{"a": int64(1)}, `{"a":1}`},
		// a tag that isn't JSON is written as a string
		{"007", "007", `"007"`},
		{"a\x01b", "a\x01b", `"a\u0001b"`},
		{`"open`, `"open`, `"\"open"`},
	} {
		value, text := sqlTagOutputValue(c.tag)
		require.Equal(t, c.want, value, c.tag)
		require.Equal(t, c.json, text, c.tag)
	}
}

// nativeSeries returns a series of one row at minute m, its fields named as given, each with a value
func nativeSeries(m int, tags dataset.Tags, fields timeseries.FieldDefinitions, values ...any) *dataset.Series {
	return dataset.NewSeries(dataset.SeriesHeader{Tags: tags, ValueFieldsList: fields},
		dataset.Points{{Epoch: epoch.Epoch(time.Duration(m) * time.Minute), Values: values}})
}

func TestNativeKeys(t *testing.T) {
	write := func(plan *QueryPlan, series ...*dataset.Series) string {
		ds := &dataset.DataSet{Results: dataset.Results{{SeriesList: series}}}
		out, err := MarshalTimeseries(ds, &timeseries.RequestOptions{ProviderRequest: plan}, 200)
		require.NoError(t, err)
		return string(out)
	}
	topN := NewQueryPlan(queryTopN, []string{testPage}, []string{testCount}, false, nil, nil)
	// a value of a dimension's name takes the dimension's place, as a Java map's put keeps the first place
	require.Equal(t, `[{"timestamp":"1970-01-01T00:00:00.000Z","result":[{"page":2,"count":1}]}]`,
		write(topN, nativeSeries(0, dataset.Tags{testPage: "a"},
			timeseries.FieldDefinitions{{Name: testPage, ProviderData1: fieldNativeDimension}, {Name: testCount}, {Name: testPage}},
			"a", int64(1), int64(2))))
	// series of different fields keep their own keys
	require.Equal(t, `[{"timestamp":"1970-01-01T00:00:00.000Z","result":[{"count":1}]},`+
		`{"timestamp":"1970-01-01T00:01:00.000Z","result":[{"sum":2}]},{"timestamp":"1970-01-01T00:02:00.000Z","result":[{"total":3}]}]`,
		write(topN, nativeSeries(0, nil, timeseries.FieldDefinitions{{Name: testCount}}, int64(1)),
			nativeSeries(1, nil, timeseries.FieldDefinitions{{Name: "sum"}}, int64(2)),
			nativeSeries(2, nil, timeseries.FieldDefinitions{{Name: "total", ProviderData1: fieldNativeDimension}}, int64(3))))
	// a topN's keys are in the query's order, which Druid writes when its cache serves the rows
	body := `[{"timestamp":"2026-09-28T00:00:00.000Z","result":[{"cab_type":"orange","c16160":336,"f16160":4276.5,"p16160":562},` +
		`{"cab_type":"blue","c16160":115,"f16160":1610.5,"p16160":179}]}]`
	plan := NewQueryPlan(queryTopN, []string{"cab_type"}, []string{"c16160", "f16160", "p16160"}, false, nil, nil)
	ts, err := UnmarshalTimeseries([]byte(body), testTRQ(plan))
	require.NoError(t, err)
	out, err := MarshalTimeseries(ts, nil, 200)
	require.NoError(t, err)
	require.Equal(t, body, string(out))
}

// sqlTestSeries returns a series of a host tag and a value at minute m, its fields at the positions given
func sqlTestSeries(m int, host string, hostPos, valuePos int, value any) *dataset.Series {
	return dataset.NewSeries(dataset.SeriesHeader{
		Tags:            dataset.Tags{testHost: host},
		TimestampField:  timeseries.FieldDefinition{Name: testBucket, DataType: timeseries.DateTimeRFC3339Nano},
		TagFieldsList:   timeseries.FieldDefinitions{{Name: testHost, OutputPosition: hostPos}},
		ValueFieldsList: timeseries.FieldDefinitions{{Name: testValue, OutputPosition: valuePos}},
	}, dataset.Points{{Epoch: epoch.Epoch(time.Duration(m) * time.Minute), Values: []any{value}}})
}

func TestSQLLayouts(t *testing.T) {
	write := func(ordering []timeseries.OrderTerm, series ...*dataset.Series) string {
		trq := testSQLTRQ(testSQLPlan())
		trq.Ordering = ordering
		ds := &dataset.DataSet{TimeRangeQuery: trq, Results: dataset.Results{{SeriesList: series}}}
		out, err := MarshalTimeseries(ds, nil, 200)
		require.NoError(t, err)
		return string(out)
	}
	byTime := []timeseries.OrderTerm{{Column: testBucket}, {Column: testHost, NullsFirst: true}}
	// series of different positions or time types keep their own layouts
	millis := sqlTestSeries(2, `"c"`, 1, 2, int64(3))
	millis.Header.TimestampField.DataType = timeseries.DateTimeUnixMilli
	require.Equal(t, `[{"bucket":"1970-01-01T00:00:00.000Z","host":"a","value":1},`+
		`{"bucket":"1970-01-01T00:01:00.000Z","value":2,"host":"b"},{"bucket":120000,"host":"c","value":3}]`+"\n",
		write(byTime, sqlTestSeries(0, `"a"`, 1, 2, int64(1)), millis, sqlTestSeries(1, `"b"`, 2, 1, int64(2))))
	// a series without rows sorts last, leaving the others in their tags' order
	empty := sqlTestSeries(0, `"z"`, 1, 2, int64(0))
	empty.SetPoints(nil)
	require.Equal(t, `[{"bucket":"1970-01-01T00:00:00.000Z","host":"a","value":1},`+
		`{"bucket":"1970-01-01T00:00:00.000Z","host":"b","value":2}]`+"\n",
		write(byTime, sqlTestSeries(0, `"b"`, 1, 2, int64(2)), empty, sqlTestSeries(0, `"a"`, 1, 2, int64(1))))
	// a tag that isn't JSON is written as a string
	require.Equal(t, `[{"bucket":"1970-01-01T00:00:00.000Z","host":"a b","value":1}]`+"\n",
		write(byTime, sqlTestSeries(0, "a b", 1, 2, int64(1))))
}
