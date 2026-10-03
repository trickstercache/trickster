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

package flux

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"

	"github.com/stretchr/testify/require"
)

const jsonExpected = `{"results":[{"tables":[{"columns":[{"name":"result",` +
	`"datatype":"string"},{"name":"table","datatype":"long"},{"name":"_start"` +
	`,"datatype":"dateTime:RFC3339"},{"name":"_stop",` +
	`"datatype":"dateTime:RFC3339"},{"name":"_time",` +
	`"datatype":"dateTime:RFC3339"},{"name":"avg_query","datatype":"double"},` +
	`{"name":"avg_global_thread","datatype":"double"},{"name":"hostname",` +
	`"datatype":"string"},{"name":"_measurement","datatype":"string"}],` +
	`"records":[{"values":{"result":"_result","table":0,` +
	`"_start":"2020-01-01T00:00:00Z","_stop":"2020-01-01T00:02:00Z",` +
	`"_time":"2020-01-01T00:00:00Z","avg_query":1.781,` +
	`"avg_global_thread":54.12348,"hostname":"localhost","_measurement":"cpu"}},` +
	`{"values":{"result":"_result","table":0,"_start":"2020-01-01T00:00:00Z",` +
	`"_stop":"2020-01-01T00:02:00Z","_time":"2020-01-01T00:01:00Z",` +
	`"avg_query":2.429,"avg_global_thread":57.91308,"hostname":"localhost",` +
	`"_measurement":"cpu"}},{"values":{"result":"_result","table":0,` +
	`"_start":"2020-01-01T00:00:00Z","_stop":"2020-01-01T00:02:00Z",` +
	`"_time":"2020-01-01T00:02:00Z","avg_query":1.929,` +
	`"avg_global_thread":55.21703,"hostname":"localhost","_measurement":"cpu"}}]` +
	`}]}]}`

func TestMarshalTimeseriesJSONWriter(t *testing.T) {
	buf := new(bytes.Buffer)
	err := marshalTimeseriesJSONWriter(testDataSet(), nil, 200, buf)
	if err != nil {
		t.Error(err)
	}
	b := buf.Bytes()
	if string(b) != jsonExpected {
		t.Errorf("expected %s\n\ngot %s", jsonExpected, string(b))
	}
}

// a series of every value kind and cell branch: defaults for nulls and empty text, values a segment
// doesn't have, start and stop times, a missing tag, and a time that isn't RFC 3339
func jsonBranchSeries(timeType timeseries.FieldDataType) *dataset.Series {
	h := dataset.SeriesHeader{
		Tags: dataset.Tags{"host": "a<b>&c"},
		TimestampField: timeseries.FieldDefinition{
			Name: timeAltColumnName, DataType: timeType,
			SDataType: TypeRFC3339, Role: timeseries.RoleTimestamp, OutputPosition: 3,
		},
		TagFieldsList: timeseries.FieldDefinitions{
			{Name: "host", OutputPosition: 4, SDataType: TypeString, Role: timeseries.RoleTag},
			{Name: "missing", OutputPosition: 5, SDataType: TypeString, DefaultValue: "dflt", Role: timeseries.RoleTag},
		},
		ValueFieldsList: timeseries.FieldDefinitions{
			{Name: "v", OutputPosition: 6, SDataType: TypeDouble, DefaultValue: "0", Role: timeseries.RoleValue},
			{Name: "w", OutputPosition: 7, SDataType: TypeString, DefaultValue: "none", Role: timeseries.RoleValue},
			{Name: "past", OutputPosition: 8, SDataType: TypeLong, DefaultValue: "x", Role: timeseries.RoleValue},
		},
		UntrackedFieldsList: timeseries.FieldDefinitions{
			{Name: "", OutputPosition: 0},
			{Name: tableColumnName, OutputPosition: 1, SDataType: TypeLong, Role: timeseries.RoleUntracked},
			{Name: startColumnName, OutputPosition: 2, DataType: timeseries.Int64, SDataType: TypeLong, Role: timeseries.RoleUntracked},
			{Name: stopColumnName, OutputPosition: 9, DataType: timeseries.DateTimeRFC3339, SDataType: TypeRFC3339, Role: timeseries.RoleUntracked},
			{Name: "other", OutputPosition: 10, DefaultValue: "o\"d", Role: timeseries.RoleUntracked},
		},
	}
	values := [][]any{
		{1.5, "x"},
		{nil, ""},
		{math.NaN(), " <"},
		{int64(-7), true},
		{uint64(9), json.Number("12.50")},
		{[]byte("raw"), map[string]any{"k": 1}},
		{math.Inf(1), nil},
	}
	pts := make(dataset.Points, len(values))
	for i, v := range values {
		pts[i] = dataset.Point{Epoch: epoch.Epoch(1577836800123456789 + int64(i)*int64(time.Second)), Values: v}
	}
	return dataset.NewSeries(h, pts)
}

var legacyMissingValue = regexp.MustCompile(`:([,}])`)

func TestWriteJSONMatchesLegacy(t *testing.T) {
	for _, tt := range []timeseries.FieldDataType{
		timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano,
		timeseries.Int64,
	} {
		a, b := jsonBranchSeries(tt), jsonBranchSeries(tt)
		ds := &dataset.DataSet{TimeRangeQuery: testTRQ, Results: []*dataset.Result{
			{SeriesList: []*dataset.Series{a, b}},
			{Name: "second", SeriesList: []*dataset.Series{a}},
			{SeriesList: []*dataset.Series{}},
		}}
		// a series held in parts reads as one
		ds.Results[1].SeriesList = append(ds.Results[1].SeriesList, parts.Of(&dataset.DataSet{Results: []*dataset.Result{
			{SeriesList: []*dataset.Series{jsonBranchSeries(tt)}},
		}}, epoch.Epoch(2*time.Second)).Results[0].SeriesList[0])
		for _, d := range []*dataset.DataSet{ds, testDataSet(), {Results: []*dataset.Result{}}} {
			var want, got bytes.Buffer
			require.NoError(t, legacyWriteJSON(d, &want))
			require.NoError(t, writeJSON(d, &got))
			// the legacy writer wrote nothing for a NaN or an infinity, where null is written now
			require.Equal(t, legacyMissingValue.ReplaceAllString(want.String(), `:null$1`), got.String())
			require.True(t, json.Valid(got.Bytes()))
		}
	}
}

func BenchmarkWriteJSON(b *testing.B) {
	ts, err := UnmarshalTimeseries(fluxBody(100, 1000), decoderTRQ())
	require.NoError(b, err)
	ds := ts.(*dataset.DataSet)
	for name, write := range map[string]func(*dataset.DataSet, io.Writer) error{
		"legacy": legacyWriteJSON, "segments": writeJSON,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := write(ds, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
