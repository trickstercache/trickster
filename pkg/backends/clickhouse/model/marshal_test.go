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
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func FuzzWFDataItemMarshalJSON(f *testing.F) {
	f.Add("key1", "value1", "key2", "value2")
	f.Add("query", `SELECT * WHERE x = "y"`, "path", `C:\Users\test`)
	f.Add("unicode", "héllo\nwörld", "null\x00byte", "a\tb")
	f.Fuzz(func(t *testing.T, k1, v1, k2, v2 string) {
		item := WFDataItem{
			{Key: k1, Value: v1},
			{Key: k2, Value: v2},
		}
		b, err := item.MarshalJSON()
		if err != nil {
			return
		}
		if !json.Valid(b) {
			t.Fatalf("WFDataItem.MarshalJSON produced invalid JSON: %s", string(b))
		}
		var roundTrip map[string]string
		if err := json.Unmarshal(b, &roundTrip); err != nil {
			t.Fatalf("WFDataItem.MarshalJSON unmarshal failed: %v\nbody: %s", err, string(b))
		}
	})
}

func TestWFDataItem_MarshalJSON_EscapesSpecialChars(t *testing.T) {
	item := WFDataItem{
		{Key: "name", Value: `O'Brien`},
		{Key: "query", Value: `SELECT * WHERE x = "y"`},
		{Key: "path", Value: `C:\Users\test`},
	}
	b, err := item.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid JSON: %v (body=%s)", err, string(b))
	}
	if out["query"] != `SELECT * WHERE x = "y"` {
		t.Errorf("value not escaped, got %q", out["query"])
	}
	if out["path"] != `C:\Users\test` {
		t.Errorf("backslash not escaped, got %q", out["path"])
	}
}

func TestMarshalJSON(t *testing.T) {
	b := new(bytes.Buffer)
	err := marshalTimeseriesJSON(b, testDataSet(), &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if strings.TrimSpace(b.String()) != strings.TrimSpace(testDataJSONMinified) {
		t.Error("unexpected json body\n", b.String(), "\nexpected\n", testDataJSONMinified)
	}
}

func TestMarshalXSV(t *testing.T) {
	b := new(bytes.Buffer)
	err := marshalTimeseriesXSV(b, testDataSet(), &timeseries.RequestOptions{},
		false, false, ',')
	if err != nil {
		t.Error(err)
	}
	if strings.TrimSpace(b.String()) != strings.TrimSpace(testDataCSV) {
		t.Error("unexpected json body\n" + b.String() + "\nexpected\n" + testDataCSV)
	}
}

func TestMarshalTimeseries(t *testing.T) {
	b, err := MarshalTimeseries(testDataSet(), &timeseries.RequestOptions{OutputFormat: 5}, 200)
	if err != nil {
		t.Error(err)
	}
	if string(b) != testDataTSVWithNamesAndTypes {
		t.Errorf("unexpected output:\n%s", string(b))
	}
}

func TestMarshalTimeseriesWriterFormats(t *testing.T) {
	ds := testDataSet()
	tests := []struct {
		name   string
		format byte
		want   string
	}{
		{name: "json", format: 0, want: testDataJSONMinified},
		{name: "csv", format: 1, want: testDataCSV},
		{name: "csv with names", format: 2, want: "t,hostname,avg_query,avg_global_thread\n" + testDataCSV},
		{name: "tsv", format: 3, want: strings.ReplaceAll(testDataCSV, ",", "\t")},
		{name: "tsv with names", format: 4, want: "t\thostname\tavg_query\tavg_global_thread\n" +
			strings.ReplaceAll(testDataCSV, ",", "\t")},
		{name: "tsv with names and types", format: 5, want: testDataTSVWithNamesAndTypes},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := MarshalTimeseries(ds, &timeseries.RequestOptions{OutputFormat: tc.format}, 200)
			if err != nil {
				t.Fatalf("MarshalTimeseries: %v", err)
			}
			if strings.TrimSpace(string(b)) != strings.TrimSpace(tc.want) {
				t.Errorf("got:\n%s\nwant:\n%s", string(b), tc.want)
			}
		})
	}
}

func TestMarshalTimeseriesErrors(t *testing.T) {
	_, err := MarshalTimeseries(nil, nil, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Fatalf("nil ts: got %v", err)
	}

	err = MarshalTimeseriesWriter(nil, nil, 200, new(bytes.Buffer))
	if err != timeseries.ErrUnknownFormat {
		t.Fatalf("nil writer input: got %v", err)
	}

	// Unsupported output format.
	err = MarshalTimeseriesWriter(testDataSet(),
		&timeseries.RequestOptions{OutputFormat: 99}, 200, new(bytes.Buffer))
	if err != timeseries.ErrUnknownFormat {
		t.Fatalf("bad format: got %v", err)
	}

	// Non-dataset timeseries.
	fake := &fakeTimeseries{}
	err = MarshalTimeseriesWriter(fake, &timeseries.RequestOptions{}, 200, new(bytes.Buffer))
	if err != timeseries.ErrUnknownFormat {
		t.Fatalf("non-dataset: got %v", err)
	}
}

type fakeTimeseries struct{}

func (f *fakeTimeseries) SetExtents(timeseries.ExtentList)             {}
func (f *fakeTimeseries) Extents() timeseries.ExtentList               { return nil }
func (f *fakeTimeseries) VolatileExtents() timeseries.ExtentList       { return nil }
func (f *fakeTimeseries) SetVolatileExtents(timeseries.ExtentList)     {}
func (f *fakeTimeseries) SetTimeRangeQuery(*timeseries.TimeRangeQuery) {}
func (f *fakeTimeseries) Step() time.Duration                          { return 0 }
func (f *fakeTimeseries) Merge(bool, ...timeseries.Timeseries)         {}
func (f *fakeTimeseries) Sort()                                        {}
func (f *fakeTimeseries) CropToRange(timeseries.Extent)                {}
func (f *fakeTimeseries) CropToSize(int, time.Time, timeseries.Extent) {}
func (f *fakeTimeseries) CroppedClone(timeseries.Extent) timeseries.Timeseries {
	return f
}
func (f *fakeTimeseries) SeriesCount() int             { return 0 }
func (f *fakeTimeseries) ValueCount() int64            { return 0 }
func (f *fakeTimeseries) Size() int64                  { return 0 }
func (f *fakeTimeseries) Clone() timeseries.Timeseries { return f }
func (f *fakeTimeseries) TimestampCount() int64        { return 0 }

func TestMarshalXSVHeadersAndEdges(t *testing.T) {
	ds := testDataSet()
	rw := httptest.NewRecorder()
	err := marshalTimeseriesXSV(rw, ds, nil, true, false, ',')
	if err != nil {
		t.Fatal(err)
	}
	if ct := rw.Header().Get(headers.NameContentType); !strings.Contains(ct, "comma-separated") {
		t.Errorf("unexpected content-type %q", ct)
	}
	if got := rw.Header().Get(formatHeader); got != "CSVWithNames" {
		t.Errorf("unexpected format header %q", got)
	}
	body := rw.Body.String()
	if !strings.HasPrefix(body, "t,hostname,avg_query,avg_global_thread\n") {
		t.Errorf("expected names row, got %s", body)
	}

	rw = httptest.NewRecorder()
	err = marshalTimeseriesXSV(rw, ds, nil, true, true, '\t')
	if err != nil {
		t.Fatal(err)
	}
	if ct := rw.Header().Get(headers.NameContentType); !strings.Contains(ct, "tab-separated") {
		t.Errorf("unexpected content-type %q", ct)
	}
	if got := rw.Header().Get(formatHeader); got != "TSVWithNamesAndTypes" {
		t.Errorf("unexpected format header %q", got)
	}

	// No value/tag fields => ErrNoTimerangeQuery
	empty := &dataset.DataSet{
		Results: []*dataset.Result{{
			SeriesList: []*dataset.Series{{
				Header: dataset.SeriesHeader{
					TimestampField: timeseries.FieldDefinition{
						Name:     "t",
						DataType: 0,
					},
				},
			}},
		}},
	}
	err = marshalTimeseriesXSV(new(bytes.Buffer), empty, nil, false, false, ',')
	if err != timeseries.ErrNoTimerangeQuery {
		t.Fatalf("expected ErrNoTimerangeQuery, got %v", err)
	}

	// Timestamp OutputPosition beyond field count.
	badPos := testDataSet()
	badPos.Results[0].SeriesList[0].Header.TimestampField.OutputPosition = 99
	err = marshalTimeseriesXSV(new(bytes.Buffer), badPos, nil, false, false, ',')
	if err != timeseries.ErrTableHeader {
		t.Fatalf("expected ErrTableHeader, got %v", err)
	}
}

func TestMarshalXSVUntrackedAndSkips(t *testing.T) {
	ds := &dataset.DataSet{
		Results: []*dataset.Result{{
			SeriesList: []*dataset.Series{{
				Header: dataset.SeriesHeader{
					TimestampField: timeseries.FieldDefinition{
						Name:           "t",
						DataType:       timeseries.DateTimeUnixMilli,
						SDataType:      "UInt64",
						Role:           timeseries.RoleTimestamp,
						OutputPosition: 0,
					},
					TagFieldsList: []timeseries.FieldDefinition{
						{
							// Same name as timestamp is skipped in header rows.
							Name:           "t",
							Role:           timeseries.RoleTag,
							OutputPosition: 0,
							SDataType:      "String",
						},
						{
							Name:           "host",
							Role:           timeseries.RoleTag,
							OutputPosition: 99, // skipped: beyond fieldCount
							SDataType:      "String",
						},
						{
							Name:           "env",
							Role:           timeseries.RoleTag,
							OutputPosition: 1,
							SDataType:      "String",
						},
					},
					ValueFieldsList: []timeseries.FieldDefinition{
						{
							Name:           "v",
							Role:           timeseries.RoleValue,
							OutputPosition: 2,
							SDataType:      "Float64",
						},
					},
					UntrackedFieldsList: []timeseries.FieldDefinition{
						{
							Name:           "meta",
							Role:           timeseries.RoleUntracked,
							OutputPosition: 3,
							DefaultValue:   "x",
							SDataType:      "String",
						},
						{
							Name:           "skip",
							Role:           timeseries.RoleUntracked,
							OutputPosition: -1, // skipped in data rows
							DefaultValue:   "nope",
							SDataType:      "String",
						},
					},
					Tags: dataset.Tags{"env": "prod"},
				},
				Points: []dataset.Point{
					{Epoch: 1577836800000000000, Values: []any{"1.5"}},
				},
			}},
		}},
	}

	// Avoid name/type header rows: vals with OutputPosition < 0 are not
	// bounds-checked there, but data-row emission skips invalid positions.
	b := new(bytes.Buffer)
	err := marshalTimeseriesXSV(b, ds, nil, false, false, ',')
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(b.String())
	if !strings.Contains(line, "prod") {
		t.Errorf("expected tag value in %s", line)
	}
	if !strings.Contains(line, "x") {
		t.Errorf("expected untracked default in %s", line)
	}
	if !strings.Contains(line, "1.5") {
		t.Errorf("expected value in %s", line)
	}
	if strings.Contains(line, "nope") {
		t.Errorf("did not expect skipped untracked default in %s", line)
	}
}

func TestMarshalXSVHeaderTagSkips(t *testing.T) {
	ds := &dataset.DataSet{
		Results: []*dataset.Result{{
			SeriesList: []*dataset.Series{{
				Header: dataset.SeriesHeader{
					TimestampField: timeseries.FieldDefinition{
						Name:           "t",
						DataType:       timeseries.DateTimeUnixMilli,
						SDataType:      "UInt64",
						Role:           timeseries.RoleTimestamp,
						OutputPosition: 0,
					},
					TagFieldsList: []timeseries.FieldDefinition{
						{
							Name:           "t", // same as timestamp: skipped in header row
							Role:           timeseries.RoleTag,
							OutputPosition: 0,
							SDataType:      "String",
						},
						{
							Name:           "host",
							Role:           timeseries.RoleTag,
							OutputPosition: 99, // > fieldCount: skipped in header row
							SDataType:      "String",
						},
						{
							Name:           "env",
							Role:           timeseries.RoleTag,
							OutputPosition: 1,
							SDataType:      "String",
						},
					},
					ValueFieldsList: []timeseries.FieldDefinition{
						{
							Name:           "v",
							Role:           timeseries.RoleValue,
							OutputPosition: 2,
							SDataType:      "Float64",
						},
					},
					Tags: dataset.Tags{"env": "prod"},
				},
				Points: []dataset.Point{
					{Epoch: 1577836800000000000, Values: []any{"1.5"}},
				},
			}},
		}},
	}

	b := new(bytes.Buffer)
	if err := marshalTimeseriesXSV(b, ds, nil, true, false, ','); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected names + data, got %v", lines)
	}
	if !strings.HasPrefix(lines[0], "t,") {
		t.Errorf("unexpected names row %q", lines[0])
	}
	if strings.Contains(lines[0], "host") {
		t.Errorf("host tag with invalid output position should be skipped: %q", lines[0])
	}
}
