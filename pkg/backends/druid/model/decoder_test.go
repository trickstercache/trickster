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
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset/stream/streamtest"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"

	"github.com/stretchr/testify/require"
)

var (
	tsPlan      = NewQueryPlan(queryTimeseries, nil, []string{"count", "sum"}, false, nil, nil)
	groupByPlan = NewQueryPlan(queryGroupBy, []string{"page", "user"}, []string{"count"}, false, nil, nil)
	topNPlan    = NewQueryPlan(queryTopN, []string{"page"}, []string{"count"}, false, nil, nil)
)

func arraySQLPlan() *SQLQueryPlan {
	p := testSQLPlan()
	return NewSQLQueryPlanWithResponseShape(p.Plan, nil, SQLResponseArray, true, "bucket", "host", "value")
}

type druidCase struct {
	body string
	trq  *timeseries.TimeRangeQuery
}

// the bodies the stream decoders must decode exactly as the decoders they replaced did; each series'
// rows are in time order, which the replaced native decoder kept rather than sorted
var druidBodies = map[string]druidCase{
	"timeseries": {`[{"timestamp":"2024-01-01T00:00:00.000Z","result":{"count":1,"sum":1.5}},` +
		`{"timestamp":"2024-01-01T00:01:00.000Z","result":{"count":2,"sum":null}}]`, testTRQ(tsPlan)},
	"timeseries types": {`[{"timestamp":"2024-01-01T00:00:00Z","result":{"count":1,"sum":-2,"big":18446744073709551615,` +
		`"f":2.5e-7,"b":true,"s":"xé\"y","o":{"k":[1,2.5,null],"j":"v"},"n":null}},` +
		`{"timestamp":"2024-01-01T00:00:01.123456789Z","result":{"count":1.5,"sum":"text","big":7,"b":false}},` +
		`{"timestamp":"2024-01-01T01:00:02+01:00","result":{"count":3,"sum":4}}]`, testTRQ(tsPlan)},
	"timeseries late values": {`[{"timestamp":"2024-01-01T00:00:00Z","result":{"count":1}},` +
		`{"timestamp":"2024-01-01T00:01:00Z","result":{"zeta":1,"count":2,"alpha":"a"}},` +
		`{"timestamp":"2024-01-01T00:02:00Z","result":{"mid":3,"count":3,"count":4}}]`, testTRQ(tsPlan)},
	"timeseries wide integers": {`[{"timestamp":"2024-01-01T00:00:00Z","result":{"count":4294967296,"sum":-4294967296,` +
		`"over":99999999999999999999,"neg":-9223372036854775808}}]`, testTRQ(tsPlan)},
	"timeseries with a dimension": {`[{"timestamp":"2024-01-01T00:00:00Z","result":{"page":"a","count":1}}]`,
		testTRQ(NewQueryPlan(queryTimeseries, []string{"page"}, []string{"count"}, false, nil, nil))},
	"empty": {`[]`, testTRQ(tsPlan)},
	"null":  {`null`, testTRQ(groupByPlan)},
	"whitespace": {" [ {\"timestamp\" : \"2024-01-01T00:00:00Z\" , \"result\" : { \"count\" : 1 } } ] \n",
		testTRQ(tsPlan)},
	"groupBy": {`[{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"page":"a","user":"u","count":1}},` +
		`{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"page":"b","user":"u","count":2}},` +
		`{"version":"v1","timestamp":"2024-01-01T00:01:00.000Z","event":{"page":"a","user":"u","count":3,"extra":true}}]`,
		testTRQ(groupByPlan)},
	"groupBy dimension types": {`[{"timestamp":"2024-01-01T00:00:00Z","event":{"page":5,"user":null,"count":1}},` +
		`{"timestamp":"2024-01-01T00:00:00Z","version":2,"event":{"page":1.5,"count":2}},` +
		`{"timestamp":"2024-01-01T00:00:00Z","version":null,"event":{"page":{"b":1,"a":[true]},"user":"<a&b>","count":3}},` +
		`{"timestamp":"2024-01-01T00:00:00Z","event":{"page":"null","user":false,"count":4}},` +
		`{"timestamp":"2024-01-01T00:00:00Z","event":{"page":-1e-9,"user":-0,"count":5}},` +
		`{"timestamp":"2024-01-01T00:01:00Z","event":{"page":5,"user":null,"count":6,"page":6}}]`, testTRQ(groupByPlan)},
	"topN": {`[{"timestamp":"2024-01-01T00:00:00.000Z","result":[{"page":"a","count":9},{"page":"b","count":5}]},` +
		`{"timestamp":"2024-01-01T00:01:00.000Z","result":[]},` +
		`{"timestamp":"2024-01-01T00:02:00.000Z","result":[{"page":"b","count":7,"x":1},{"page":"a","count":2}]}]`,
		testTRQ(topNPlan)},
	"sql": {`[{"bucket":"2024-01-01T00:00:00.000Z","host":"a","value":1},{"bucket":"2024-01-01T00:00:00.000Z","host":"b","value":2.5},` +
		`{"host":"a","value":null,"bucket":"2024-01-01T01:00:00.000Z"}]`, testSQLTRQ(testSQLPlan())},
	"sql times": {`[{"bucket":1704067200000,"host":"a","value":1},{"bucket":"2024-01-01T01:00:00","host":"a","value":2},` +
		`{"bucket":"2024-01-01 02:00:00","host":"a","value":3},{"bucket":"2024-01-01 03:00:00.5","host":"a","value":4},` +
		`{"bucket":"2024-01-02","host":"a","value":5},{"bucket":"1704243600000","host":"a","value":6},` +
		`{"bucket":"2024-01-03T02:00:00+01:00","host":"a","value":7},{"bucket":"2024-01-03T03:00:00.123456789Z","host":"a","value":8}]`,
		testSQLTRQ(testSQLPlan())},
	"sql out of order": {`[{"bucket":"2024-01-01T02:00:00Z","host":"a","value":1},{"bucket":"2024-01-01T00:00:00Z","host":"a","value":2},` +
		`{"bucket":"2024-01-01T02:00:00Z","host":"a","value":3},{"bucket":"2024-01-01T01:00:00Z","host":"b","value":4}]`,
		testSQLTRQ(testSQLPlan())},
	"sql tag types": {`[{"bucket":"2024-01-01T00:00:00Z","host":5,"value":"x"},{"bucket":"2024-01-01T00:00:00Z","host":null,"value":true},` +
		`{"bucket":"2024-01-01T00:00:00Z","host":"<a&b>","value":{"k":[1]}},{"bucket":"2024-01-01T00:00:00Z","host":1.50,"value":18446744073709551615},` +
		`{"bucket":"2024-01-01T00:00:00Z","host":{"z":1,"a":2},"value":-1e-9},{"bucket":"2024-01-01T00:00:00Z","host":"null","value":1e400}]`,
		testSQLTRQ(testSQLPlan())},
	"sql empty": {`[]`, testSQLTRQ(testSQLPlan())},
	"sql array": {`[["bucket","host","value"],["2024-01-01T00:00:00Z","a",1],["2024-01-01T01:00:00Z","b",null]]`,
		testSQLTRQ(arraySQLPlan())},
	"sql array header only": {`[["bucket","host","value"]]`, testSQLTRQ(arraySQLPlan())},
	"sql null":              {`null`, testSQLTRQ(testSQLPlan())},
	"sql names ignore case": {`[{"BUCKET":"2024-01-01T00:00:00Z","Host":"a","value":1}]`, testSQLTRQ(NewSQLQueryPlan(testSQLPlan().Plan, nil))},
	"sql header without time": {`[["x","y"]]`,
		testSQLTRQ(NewSQLQueryPlanWithResponseShape(testSQLPlan().Plan, nil, SQLResponseArray, true))},
}

func druidConformance(t *testing.T, c druidCase) {
	t.Helper()
	streamtest.Conformance(t, newDecoder, streamtest.Case{
		TRQ: c.trq, Body: []byte(c.body), Legacy: legacyUnmarshalTimeseriesReader, Unwrap: legacyForm,
	})
}

// legacyForm returns a DataSet as the replaced decoders held it: numbers and compound values decoded,
// tags as encoding/json writes their values, and times typed as text
func legacyForm(ts timeseries.Timeseries) *dataset.DataSet {
	ds := ts.(*dataset.DataSet)
	for _, r := range ds.Results {
		for _, s := range r.SeriesList {
			if s.Header.TimestampField.DataType == timeseries.DateTimeUnixMilli {
				s.Header.TimestampField.DataType = timeseries.DateTimeRFC3339Nano
			}
			if s.Header.Name != sqlResultName {
				// a native dimension's tag is its text, which isn't JSON, or the JSON of another value
				for name, tag := range s.Header.Tags {
					if json.Valid([]byte(tag)) && tag[0] != '"' {
						s.Header.Tags[name] = legacyTagString(legacyJSON(tag))
					}
				}
			} else {
				for name, tag := range s.Header.Tags {
					s.Header.Tags[name], _ = legacySqlTagIdentity(legacyJSON(tag))
				}
			}
			pts := s.Points()
			for _, p := range pts {
				for i, v := range p.Values {
					switch v := v.(type) {
					case json.Number:
						p.Values[i] = normalizeJSONValue(v)
					case []byte:
						p.Values[i] = legacyJSON(string(v))
					}
				}
			}
			s.SetPoints(pts)
		}
		slices.SortFunc(r.SeriesList, func(a, b *dataset.Series) int {
			return strings.Compare(a.Header.Tags.JSON(), b.Header.Tags.JSON())
		})
	}
	return ds
}

// legacyJSON decodes JSON as the replaced decoders did
func legacyJSON(text string) any {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return text
	}
	return normalizeJSONValue(v)
}

func TestDecoderMatchesLegacy(t *testing.T) {
	for name, c := range druidBodies {
		t.Run(name, func(t *testing.T) { druidConformance(t, c) })
	}
}

// randomJSONValue returns a value a Druid row holds, in one of its JSON forms
func randomJSONValue(rng *weaktest.Rand) string {
	switch rng.IntN(10) {
	case 0:
		return "null"
	case 1:
		return strconv.FormatInt(rng.Int64N(1e6)-5e5, 10)
	case 2:
		return strconv.FormatFloat(rng.NormFloat64()*1e3, 'g', -1, 64)
	case 3:
		return `"s` + strconv.Itoa(rng.IntN(5)) + `"`
	case 4:
		return []string{"true", "false"}[rng.IntN(2)]
	case 5:
		return `{"k":` + strconv.Itoa(rng.IntN(3)) + `}`
	case 6:
		return "18446744073709551615"
	}
	return strconv.Itoa(rng.IntN(100))
}

// randomNative returns a groupBy or topN body of random dimensions and values, which may name values
// the plan doesn't declare
func randomNative(rng *weaktest.Rand) druidCase {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	var b strings.Builder
	b.WriteByte('[')
	topN := rng.IntN(2) == 0
	for p := range 1 + rng.IntN(20) {
		if p > 0 {
			b.WriteByte(',')
		}
		ts := start.Add(time.Duration(p) * time.Minute).Format(time.RFC3339Nano)
		row := func() string {
			var r strings.Builder
			r.WriteString(`{"page":` + []string{`"a"`, `"b"`, "1", "null", `"c"`}[rng.IntN(5)])
			if !topN {
				r.WriteString(`,"user":` + randomJSONValue(rng))
			}
			for _, name := range []string{"count", "extra", "alpha"} {
				if name == "count" || rng.IntN(3) == 0 {
					r.WriteString(`,"` + name + `":` + randomJSONValue(rng))
				}
			}
			return r.String() + "}"
		}
		if topN {
			b.WriteString(`{"timestamp":"` + ts + `","result":[`)
			for i := range rng.IntN(4) {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(row())
			}
			b.WriteString("]}")
			continue
		}
		for i := range 1 + rng.IntN(3) {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`{"timestamp":"` + ts + `","version":` + randomJSONValue(rng) + `,"event":` + row() + "}")
		}
	}
	b.WriteByte(']')
	if topN {
		return druidCase{b.String(), testTRQ(topNPlan)}
	}
	return druidCase{b.String(), testTRQ(groupByPlan)}
}

// randomSQL returns an object or array body of random hosts and values, its rows in random time order
func randomSQL(rng *weaktest.Rand) druidCase {
	array := rng.IntN(2) == 0
	var b strings.Builder
	b.WriteByte('[')
	if array {
		b.WriteString(`["bucket","host","value"]`)
	}
	for i := range rng.IntN(30) {
		if i > 0 || array {
			b.WriteByte(',')
		}
		at := strconv.Itoa(1704067200000 + rng.IntN(10)*3600000)
		host := []string{`"a"`, `"b"`, "5", "null"}[rng.IntN(4)]
		if array {
			b.WriteString("[" + at + "," + host + "," + randomJSONValue(rng) + "]")
			continue
		}
		b.WriteString(`{"value":` + randomJSONValue(rng) + `,"bucket":` + at + `,"host":` + host + "}")
	}
	b.WriteByte(']')
	if array {
		return druidCase{b.String(), testSQLTRQ(arraySQLPlan())}
	}
	return druidCase{b.String(), testSQLTRQ(testSQLPlan())}
}

func TestDecoderMatchesLegacyAtScale(t *testing.T) {
	rng := weaktest.NewRand(16, 16)
	for trial := range 60 {
		t.Run(strconv.Itoa(trial), func(t *testing.T) {
			if trial%2 == 0 {
				druidConformance(t, randomNative(rng))
				return
			}
			druidConformance(t, randomSQL(rng))
		})
	}
}

func TestDecoderErrors(t *testing.T) {
	ts := `{"timestamp":"2024-01-01T00:00:00Z","result":{"count":1}}`
	for name, c := range map[string]druidCase{
		"no time":             {`[{"result":{"count":1}}]`, testTRQ(tsPlan)},
		"time not text":       {`[{"timestamp":1,"result":{"count":1}}]`, testTRQ(tsPlan)},
		"bad time":            {`[{"timestamp":"yesterday","result":{"count":1}}]`, testTRQ(tsPlan)},
		"result not object":   {`[{"timestamp":"2024-01-01T00:00:00Z","result":[1]}]`, testTRQ(tsPlan)},
		"event not object":    {`[{"timestamp":"2024-01-01T00:00:00Z","event":1}]`, testTRQ(groupByPlan)},
		"no event":            {`[{"timestamp":"2024-01-01T00:00:00Z","result":{"page":"a"}}]`, testTRQ(groupByPlan)},
		"topN not array":      {`[{"timestamp":"2024-01-01T00:00:00Z","result":{"page":"a"}}]`, testTRQ(topNPlan)},
		"topN row not object": {`[{"timestamp":"2024-01-01T00:00:00Z","result":[1]}]`, testTRQ(topNPlan)},
		"row not object":      {`[` + ts + `,1]`, testTRQ(tsPlan)},
		"null row":            {`[` + ts + `,null]`, testTRQ(tsPlan)},
		"not array":           {`{"timestamp":"2024-01-01T00:00:00Z"}`, testTRQ(tsPlan)},
		"trailing data":       {`[` + ts + `] x`, testTRQ(tsPlan)},
		"cut":                 {`[` + ts, testTRQ(tsPlan)},
		"sql missing column":  {`[{"bucket":"2024-01-01T00:00:00Z","host":"a"}]`, testSQLTRQ(testSQLPlan())},
		"sql extra column":    {`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1,"x":2}]`, testSQLTRQ(testSQLPlan())},
		"sql changed columns": {`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1},{"bucket":"2024-01-01T00:00:00Z","host":"a","other":1}]`, testSQLTRQ(testSQLPlan())},
		"sql repeated column": {`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1,"value":2}]`, testSQLTRQ(testSQLPlan())},
		"sql bad time":        {`[{"bucket":true,"host":"a","value":1}]`, testSQLTRQ(testSQLPlan())},
		"sql time overflow":   {`[{"bucket":9223372036854775807,"host":"a","value":1}]`, testSQLTRQ(testSQLPlan())},
		"sql time too large":  {`[{"bucket":18446744073709551615,"host":"a","value":1}]`, testSQLTRQ(testSQLPlan())},
		"sql dropped column":  {`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1},{"bucket":"2024-01-01T00:00:00Z","host":"a"}]`, testSQLTRQ(testSQLPlan())},
		"sql repeat for drop": {`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1},{"bucket":"2024-01-01T00:00:00Z","host":"a","host":"b"}]`, testSQLTRQ(testSQLPlan())},
		"sql header repeats": {`[["bucket","host","host"]]`,
			testSQLTRQ(NewSQLQueryPlanWithResponseShape(testSQLPlan().Plan, nil, SQLResponseArray, true))},
		"sql time past 2262":   {`[{"bucket":"2262-04-12T00:00:00Z","host":"a","value":1}]`, testSQLTRQ(testSQLPlan())},
		"sql time before 1678": {`[{"bucket":"1677-09-21T00:00:00Z","host":"a","value":1}]`, testSQLTRQ(testSQLPlan())},
		"sql bad time text":    {`[{"bucket":"noon","host":"a","value":1}]`, testSQLTRQ(testSQLPlan())},
		"sql row not object":   {`[[1,2,3]]`, testSQLTRQ(testSQLPlan())},
		"sql array no header":  {`[["bucket","host","value"]]`, testSQLTRQ(NewSQLQueryPlanWithResponseShape(testSQLPlan().Plan, nil, SQLResponseArray, false))},
		"sql array empty":      {`[]`, testSQLTRQ(arraySQLPlan())},
		"sql array width":      {`[["bucket","host","value"],["2024-01-01T00:00:00Z","a"]]`, testSQLTRQ(arraySQLPlan())},
		"sql array wide":       {`[["bucket","host","value"],["2024-01-01T00:00:00Z","a",1,2]]`, testSQLTRQ(arraySQLPlan())},
		"sql header not text":  {`[["bucket",1,"value"]]`, testSQLTRQ(arraySQLPlan())},
		"sql header repeated":  {`[["bucket","host","host"]]`, testSQLTRQ(arraySQLPlan())},
		"sql header empty":     {`[[]]`, testSQLTRQ(arraySQLPlan())},
		"sql header blank":     {`[["bucket","","value"]]`, testSQLTRQ(arraySQLPlan())},
		"sql header row":       {`[{"bucket":1}]`, testSQLTRQ(arraySQLPlan())},
		"sql no time column": {`[{"time":"2024-01-01T00:00:00Z","host":"a","value":1}]`,
			testSQLTRQ(NewSQLQueryPlan(testSQLPlan().Plan, nil))},
		"sql output columns": {`[["bucket","host","other"]]`, testSQLTRQ(arraySQLPlan())},
	} {
		t.Run(name, func(t *testing.T) {
			streamtest.Conformance(t, newDecoder, streamtest.Case{
				TRQ: c.trq, Body: []byte(c.body), WantErr: streamtest.ErrAny, Legacy: legacyUnmarshalTimeseriesReader,
			})
		})
	}
	// a query the decoders can't model
	for _, trq := range []*timeseries.TimeRangeQuery{nil, {}, testTRQ(nil), testTRQ(NewQueryPlan("scan", nil, nil, false, nil, nil)),
		{ParsedQuery: (*SQLQueryPlan)(nil)}, {ParsedQuery: &SQLQueryPlan{}}} {
		_, err := UnmarshalTimeseries([]byte(`[]`), trq)
		require.Error(t, err)
	}
}

func TestDecoderDepartures(t *testing.T) {
	t.Run("a descending response is held in time order", func(t *testing.T) {
		// the replaced decoder held it newest first, which the cache's crops misread; it's still written
		// newest first
		plan := NewQueryPlan(queryTimeseries, nil, []string{"count"}, true, nil, nil)
		body := `[{"timestamp":"2024-01-01T00:02:00.000Z","result":{"count":3}},` +
			`{"timestamp":"2024-01-01T00:01:00.000Z","result":{"count":2}},` +
			`{"timestamp":"2024-01-01T00:00:00.000Z","result":{"count":1}}]`
		ts, err := UnmarshalTimeseries([]byte(body), testTRQ(plan))
		require.NoError(t, err)
		s := ts.(*dataset.DataSet).Results[0].SeriesList[0]
		require.True(t, s.IsSorted())
		require.Equal(t, []any{int64(1)}, s.Points()[0].Values)
		out, err := MarshalTimeseries(ts, nil, 200)
		require.NoError(t, err)
		require.Equal(t, body, string(out))
	})
	t.Run("values are written as Druid wrote them", func(t *testing.T) {
		// the replaced decoders decoded every number and compound value, rewriting 1.0 as 1 and sorting keys
		plan := NewQueryPlan(queryGroupBy, []string{"page"}, []string{"d", "o", "s", "w", "huge"}, false, nil, nil)
		body := `[{"version":"v1","timestamp":"2024-01-01T00:00:00.000Z","event":{"s":"\"\\\u0001\u001F<>&` + "\u2028" + `é\uD834\uDD1E",` +
			`"d":9.999999999999999E22,"w":18446744073709551615,"huge":1e400,"page":2.50,"o":{"b":1.0,"a":[true,1E2]}}}]`
		ts, err := UnmarshalTimeseries([]byte(body), testTRQ(plan))
		require.NoError(t, err)
		s := ts.(*dataset.DataSet).Results[0].SeriesList[0]
		require.Equal(t, "2.50", s.Header.Tags["page"])
		types := map[string]timeseries.FieldDataType{}
		for _, f := range s.Header.ValueFieldsList {
			types[f.Name] = f.DataType
		}
		require.Equal(t, timeseries.Float64, types["huge"])
		require.Equal(t, timeseries.Uint64, types["w"])
		out, err := MarshalTimeseries(ts, nil, 200)
		require.NoError(t, err)
		require.Equal(t, body, string(out))
	})
	t.Run("a time column of milliseconds is written as one", func(t *testing.T) {
		body := `[{"bucket":1704067200000,"host":"a","value":1.5},{"bucket":1704070800000,"host":"a","value":2.0}]`
		trq := testSQLTRQ(testSQLPlan())
		trq.Ordering = []timeseries.OrderTerm{{Column: "bucket"}}
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		require.NoError(t, err)
		require.Equal(t, timeseries.DateTimeUnixMilli, ts.(*dataset.DataSet).Results[0].SeriesList[0].Header.TimestampField.DataType)
		out, err := MarshalTimeseries(ts, nil, 200)
		require.NoError(t, err)
		require.Equal(t, body+"\n", string(out))
		// any time written as text keeps them all text
		ts, err = UnmarshalTimeseries([]byte(`[{"bucket":1704067200000,"host":"a","value":1},`+
			`{"bucket":"2024-01-01T01:00:00.000Z","host":"a","value":2}]`), trq)
		require.NoError(t, err)
		require.Equal(t, timeseries.DateTimeRFC3339Nano, ts.(*dataset.DataSet).Results[0].SeriesList[0].Header.TimestampField.DataType)
	})
	t.Run("a SQL tag is written as Druid wrote it", func(t *testing.T) {
		body := `[{"bucket":"2024-01-01T00:00:00.000Z","host":1.0,"value":1},{"bucket":"2024-01-01T01:00:00.000Z","host":"\u001F𝄞","value":2}]`
		trq := testSQLTRQ(testSQLPlan())
		trq.Ordering = []timeseries.OrderTerm{{Column: "bucket"}, {Column: "host", NullsFirst: true}}
		ts, err := UnmarshalTimeseries([]byte(body), trq)
		require.NoError(t, err)
		out, err := MarshalTimeseries(ts, nil, 200)
		require.NoError(t, err)
		require.Equal(t, body+"\n", string(out))
	})
}

func TestDecoderKeepsNothingOfItsInput(t *testing.T) {
	rng := weaktest.NewRand(17, 17)
	for trial := range 20 {
		c := randomNative(rng)
		if trial%2 == 1 {
			c = randomSQL(rng)
		}
		body := []byte(c.body)
		ts, err := UnmarshalTimeseries(body, c.trq)
		require.NoError(t, err)
		for i := range body {
			body[i] = 'x'
		}
		want, err := UnmarshalTimeseries([]byte(c.body), c.trq)
		require.NoError(t, err)
		require.NoError(t, streamtest.Compare(want.(*dataset.DataSet), ts.(*dataset.DataSet), streamtest.CompareOptions{}))
	}
}

// randomOrdering returns an ORDER BY of the SQL bodies' columns, starting with time most often
func randomOrdering(rng *weaktest.Rand) []timeseries.OrderTerm {
	columns := []string{"bucket", "host", "value", "missing"}
	var out []timeseries.OrderTerm
	for i := range rng.IntN(4) {
		c := columns[rng.IntN(len(columns))]
		if i == 0 && rng.IntN(4) > 0 {
			c = "bucket"
		}
		out = append(out, timeseries.OrderTerm{Column: c, Descending: rng.IntN(2) == 0, NullsFirst: rng.IntN(2) == 0})
	}
	return out
}

func TestSQLMarshalMatchesLegacy(t *testing.T) {
	rng := weaktest.NewRand(18, 18)
	for trial := range 400 {
		c := randomSQL(rng)
		ts, err := UnmarshalTimeseries([]byte(c.body), c.trq)
		require.NoError(t, err)
		ds := ts.(*dataset.DataSet)
		ds.TimeRangeQuery.Ordering = randomOrdering(rng)
		switch rng.IntN(6) {
		case 0:
			// a series out of time order, as a merge of parts may hold until sorted
			for _, s := range ds.Results[0].SeriesList {
				pts := s.Points()
				rng.Shuffle(len(pts), func(a, b int) { pts[a], pts[b] = pts[b], pts[a] })
				s.SetPoints(pts)
			}
		case 1:
			// two results, which don't merge
			ds.Results = append(ds.Results, &dataset.Result{Name: sqlResultName, SeriesList: ds.Results[0].SeriesList.Clone()})
		case 2, 3:
			// series held in parts, as merged cache entries are
			ds = parts.Of(ds, epoch.Epoch(2*time.Hour))
		case 4:
			// a value JSON can't hold, which writes nothing
			if sl := ds.Results[0].SeriesList; len(sl) > 0 && rng.IntN(2) == 0 {
				pts := sl[0].Points()
				pts[len(pts)-1].Values[len(pts[len(pts)-1].Values)-1] = math.NaN()
				sl[0].SetPoints(pts)
			}
		}
		marker := c.trq.ParsedQuery.(*SQLQueryPlan)
		var want, got strings.Builder
		werr := legacyMarshalSQLTimeseriesWriter(legacyForm(ds.Clone()), marker, &want)
		err = marshalSQLTimeseriesWriter(ds, marker, &got)
		require.Equal(t, werr != nil, err != nil, "trial %d: %v / %v", trial, werr, err)
		require.NoError(t, sameJSONTokens(want.String(), got.String()), "trial %d: %v\n%s\n%s", trial,
			ds.TimeRangeQuery.Ordering, want.String(), got.String())
	}
}

// sameJSONTokens reports whether two JSON texts hold the same tokens in order, numbers matching by value
// and times by their milliseconds
func sameJSONTokens(want, got string) error {
	wd, gd := json.NewDecoder(strings.NewReader(want)), json.NewDecoder(strings.NewReader(got))
	wd.UseNumber()
	gd.UseNumber()
	for {
		wt, werr := wd.Token()
		gt, gerr := gd.Token()
		if werr != nil || gerr != nil {
			if werr == io.EOF && gerr == io.EOF {
				return nil
			}
			return fmt.Errorf("token errors %v / %v", werr, gerr)
		}
		if wn, ok := wt.(json.Number); ok {
			if gn, ok := gt.(json.Number); ok && normalizeJSONValue(wn) == normalizeJSONValue(gn) {
				continue
			}
			wf, _ := strconv.ParseFloat(string(wn), 64)
			gf, _ := strconv.ParseFloat(fmt.Sprint(gt), 64)
			if wf == gf {
				continue
			}
		}
		// a time of milliseconds is now written as one
		if ws, ok := wt.(string); ok {
			if gn, ok := gt.(json.Number); ok {
				if at, err := time.Parse(time.RFC3339Nano, ws); err == nil && strconv.FormatInt(at.UnixMilli(), 10) == string(gn) {
					continue
				}
			}
		}
		if wt != gt {
			return fmt.Errorf("token %v, got %v", wt, gt)
		}
	}
}

func TestPlanAggregations(t *testing.T) {
	plan := NewQueryPlan(queryTimeseries, nil, []string{"a", "b", "c"}, false, nil, nil)
	require.Equal(t, 3, plan.Aggregations())
	require.Equal(t, 2, plan.WithAggregations(2).Aggregations())
	require.Equal(t, 3, plan.Aggregations())
	require.Equal(t, 3, plan.WithAggregations(9).Aggregations())
	require.Equal(t, 0, (*QueryPlan)(nil).Aggregations())
}

func TestCompareSQLNumbers(t *testing.T) {
	for _, c := range []struct {
		a, b any
		want int
	}{
		{json.Number("1.5"), int64(2), -1},
		{int64(2), json.Number("1.5"), 1},
		{json.Number("1e2"), json.Number("99.5"), 1},
		{json.Number("1.0"), float64(1), 0},
		{json.Number("1e400"), json.Number("2"), -1},
		{[]byte(`{"a":1}`), []byte(`{"b":1}`), -1},
	} {
		require.Equal(t, c.want, compareSQLValue(c.a, c.b, false), "%v %v", c.a, c.b)
	}
}

func TestCompareStoredRows(t *testing.T) {
	// a series holding the same time in two Segments, as overlapping parts may
	build := func(v float64) dataset.Segments {
		return dataset.NewSeries(dataset.SeriesHeader{}, dataset.Points{{Epoch: 1, Values: []any{v}}}).Segments()
	}
	segs := append(build(1), build(2)...)
	s := dataset.NewSeriesOf(dataset.SeriesHeader{}, segs)
	first := dataset.Row{Series: s, Seg: &s.Segments()[0], Index: 0}
	second := dataset.Row{Series: s, Seg: &s.Segments()[1], Index: 0}
	require.Equal(t, -1, compareStoredRows(first, second))
	require.Equal(t, 1, compareStoredRows(second, first))
	require.Equal(t, 0, compareStoredRows(first, first))
	require.Equal(t, -1, compareStoredRows(first, dataset.Row{SeriesIndex: 1}))
}

// benchBodies returns a groupBy response and a SQL object response of series x points
func benchBodies(series, points int) (native, sql []byte) {
	var n, q strings.Builder
	n.WriteByte('[')
	q.WriteByte('[')
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for p := range points {
		ts := start.Add(time.Duration(p) * time.Minute).Format("2006-01-02T15:04:05.000Z")
		for s := range series {
			if p+s > 0 {
				n.WriteByte(',')
				q.WriteByte(',')
			}
			count, added := strconv.Itoa(s*p%97), strconv.FormatFloat(float64(s*p%9973)/7, 'f', -1, 64)
			n.WriteString(`{"version":"v1","timestamp":"` + ts + `","event":{"page":"page-` + strconv.Itoa(s) +
				`","user":"u","count":` + count + `,"added":` + added + `}}`)
			q.WriteString(`{"bucket":"` + ts + `","host":"host-` + strconv.Itoa(s) + `","value":` + added + `}`)
		}
	}
	n.WriteByte(']')
	q.WriteByte(']')
	return []byte(n.String()), []byte(q.String())
}

func BenchmarkDecoder(b *testing.B) {
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10000, 10}} {
		native, sql := benchBodies(shape.series, shape.points)
		name := strconv.Itoa(shape.series) + "x" + strconv.Itoa(shape.points)
		for _, c := range []struct {
			name string
			body []byte
			trq  *timeseries.TimeRangeQuery
		}{{"groupby", native, testTRQ(groupByPlan)}, {"sql", sql, testSQLTRQ(testSQLPlan())}} {
			b.Run(c.name+"/"+name+"/legacy", func(b *testing.B) { streamtest.Bench(b, legacyUnmarshalTimeseriesReader, c.trq, c.body) })
			b.Run(c.name+"/"+name+"/stream", func(b *testing.B) { streamtest.Bench(b, UnmarshalTimeseriesReader, c.trq, c.body) })
		}
	}
}

func BenchmarkSQLMarshalOrdered(b *testing.B) {
	_, sql := benchBodies(100, 1000)
	ts, err := UnmarshalTimeseries(sql, testSQLTRQ(testSQLPlan()))
	require.NoError(b, err)
	ds := ts.(*dataset.DataSet)
	ds.TimeRangeQuery.Ordering = []timeseries.OrderTerm{{Column: "bucket"}}
	marker := testSQLPlan()
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := legacyMarshalSQLTimeseriesWriter(ds, marker, io.Discard); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("stream", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if err := marshalSQLTimeseriesWriter(ds, marker, io.Discard); err != nil {
				b.Fatal(err)
			}
		}
	})
}
