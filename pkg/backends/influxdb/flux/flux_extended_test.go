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
	"encoding/csv"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestValidateMarshalerOptionsBranches(t *testing.T) {
	t.Parallel()

	_, _, _, err := validateMarshalerOptions(&fakeTimeseries{}, nil)
	if err != timeseries.ErrUnknownFormat {
		t.Fatalf("err = %v", err)
	}

	ds := &dataset.DataSet{}
	frb := DefaultJSONRequestBody()
	frb.Dialect.DateTimeFormat = RFC3339Nano
	ro := &timeseries.RequestOptions{
		OutputFormat:    byte(iofmt.FluxJSONJSON),
		ProviderRequest: frb,
	}
	gotDS, gotFRB, m, err := validateMarshalerOptions(ds, ro)
	if err != nil || gotDS != ds || m == nil {
		t.Fatalf("validateMarshalerOptions = (%v, %v, %v, %v)", gotDS, gotFRB, m, err)
	}
	if gotFRB.Dialect.DateTimeFormat != RFC3339Nano {
		t.Fatalf("datetime format = %q", gotFRB.Dialect.DateTimeFormat)
	}

	ro.ProviderRequest = "not-a-body"
	_, gotFRB, _, err = validateMarshalerOptions(ds, ro)
	if err != nil || gotFRB == nil || gotFRB.Type != LangFlux {
		t.Fatalf("fallback body = (%v, %v)", gotFRB, err)
	}
}

type fakeTimeseries struct{}

func (fakeTimeseries) SetExtents(timeseries.ExtentList)         {}
func (fakeTimeseries) Extents() timeseries.ExtentList           { return nil }
func (fakeTimeseries) VolatileExtents() timeseries.ExtentList   { return nil }
func (fakeTimeseries) SetVolatileExtents(timeseries.ExtentList) {}
func (fakeTimeseries) SetTimeRangeQuery(*timeseries.TimeRangeQuery) {}
func (fakeTimeseries) Step() time.Duration                      { return 0 }
func (fakeTimeseries) Merge(bool, ...timeseries.Timeseries)     {}
func (fakeTimeseries) Sort()                                    {}
func (fakeTimeseries) ValueCount() int64                        { return 0 }
func (fakeTimeseries) Size() int64                              { return 0 }
func (fakeTimeseries) TimestampCount() int64                    { return 0 }
func (fakeTimeseries) SeriesCount() int                         { return 0 }
func (fakeTimeseries) Clone() timeseries.Timeseries             { return fakeTimeseries{} }
func (fakeTimeseries) CroppedClone(timeseries.Extent) timeseries.Timeseries {
	return fakeTimeseries{}
}
func (fakeTimeseries) CropToRange(timeseries.Extent) {}
func (fakeTimeseries) CropToSize(int, time.Time, timeseries.Extent) {}

func TestGetFormattedTimestamp(t *testing.T) {
	t.Parallel()

	e := epoch.Epoch(1577836800000000000)
	if got := getFormattedTimestamp(e, timeseries.FieldDefinition{
		DataType: timeseries.DateTimeRFC3339,
	}); got != "2020-01-01T00:00:00Z" {
		t.Fatalf("rfc3339 = %v", got)
	}
	if got := getFormattedTimestamp(e, timeseries.FieldDefinition{
		DataType: timeseries.DateTimeRFC3339Nano,
	}); got != "2020-01-01T00:00:00Z" {
		t.Fatalf("rfc3339nano = %v", got)
	}
	if got := getFormattedTimestamp(e, timeseries.FieldDefinition{}); got != e {
		t.Fatalf("raw epoch = %v", got)
	}
}

func TestGetCellValueBranches(t *testing.T) {
	t.Parallel()

	sh := dataset.SeriesHeader{Tags: dataset.Tags{}}
	pt := dataset.Point{Epoch: 1, Values: []any{nil, "", 3.14}}

	b, used := getCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleValue,
		DefaultValue: "fallback",
	}, pt, 0, 0)
	if !used || string(b) != `"fallback"` {
		t.Fatalf("nil value = (%s, %v)", b, used)
	}

	b, used = getCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleValue,
		DefaultValue: "empty",
	}, pt, 1, 0)
	if !used || string(b) != `"empty"` {
		t.Fatalf("empty string = (%s, %v)", b, used)
	}

	b, used = getCellValue(sh, timeseries.FieldDefinition{
		Name:         startColumnName,
		Role:         timeseries.RoleUntracked,
		DefaultValue: "12345",
	}, pt, 0, 0)
	if used || string(b) != "12345" {
		t.Fatalf("numeric start = (%s, %v)", b, used)
	}

	b, used = getCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleTag,
		Name:         "missing",
		DefaultValue: "def",
	}, pt, 0, 0)
	if used || string(b) != `"def"` {
		t.Fatalf("missing tag = (%s, %v)", b, used)
	}

	b, used = getCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleUntracked,
		Name:         "other",
		DefaultValue: "x",
	}, pt, 0, 0)
	if used || string(b) != `"x"` {
		t.Fatalf("default untracked = (%s, %v)", b, used)
	}
}

func TestGetCsvCellValueBranches(t *testing.T) {
	t.Parallel()

	sh := dataset.SeriesHeader{Tags: dataset.Tags{"host": "a"}}
	pt := dataset.Point{Values: []any{nil, "", 9}}

	s, used := getCsvCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleValue,
		DefaultValue: "d",
	}, pt, 0, 1)
	if !used || s != "d" {
		t.Fatalf("nil = (%q, %v)", s, used)
	}

	s, used = getCsvCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleValue,
		DefaultValue: "e",
	}, pt, 1, 1)
	if !used || s != "e" {
		t.Fatalf("empty = (%q, %v)", s, used)
	}

	s, used = getCsvCellValue(sh, timeseries.FieldDefinition{
		Role: timeseries.RoleTag,
		Name: "host",
	}, pt, 0, 1)
	if used || s != "a" {
		t.Fatalf("tag = (%q, %v)", s, used)
	}

	s, used = getCsvCellValue(sh, timeseries.FieldDefinition{
		Role:         timeseries.RoleUntracked,
		Name:         "other",
		DefaultValue: "z",
	}, pt, 0, 1)
	if used || s != "z" {
		t.Fatalf("default = (%q, %v)", s, used)
	}
}

func TestProcessSeriesHeaderNil(t *testing.T) {
	t.Parallel()
	processSeriesHeader(nil)
	processSeriesHeader(&state{})
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write fail") }

func TestCSVWriteErrorPaths(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	fds := timeseries.FieldDefinitions{
		{},
		{Name: huge, SDataType: huge, DefaultValue: huge, Role: timeseries.RoleTag, OutputPosition: 1},
		{Name: tableColumnName, Role: timeseries.RoleUntracked, OutputPosition: 2},
		{
			Name:           timeAltColumnName,
			Role:           timeseries.RoleTimestamp,
			DataType:       timeseries.DateTimeRFC3339,
			OutputPosition: 3,
		},
		{
			Name:           "v",
			Role:           timeseries.RoleValue,
			DefaultValue:   huge,
			OutputPosition: 4,
		},
	}
	s := &dataset.Series{
		Header: dataset.SeriesHeader{
			TagFieldsList:       []timeseries.FieldDefinition{fds[1]},
			UntrackedFieldsList: []timeseries.FieldDefinition{fds[0], fds[2]},
			TimestampField:      fds[3],
			ValueFieldsList:     []timeseries.FieldDefinition{fds[4]},
		},
		Points: []dataset.Point{{Epoch: 1, Values: []any{huge}}},
	}
	st := &state{
		s: s,
		e: timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)},
		w: csv.NewWriter(errWriter{}),
		t: true,
		g: true,
		d: true,
		h: true,
	}
	processSeriesHeader(st)
	// Force buffered write errors on subsequent annotation/data writes.
	st.prev = nil
	processCsvSeriesData(st)
}

func TestMarshalWritersSetHeaders(t *testing.T) {
	ds := testDataSet()

	rw := httptest.NewRecorder()
	if err := marshalTimeseriesJSONWriter(ds, nil, 201, rw); err != nil {
		t.Fatal(err)
	}
	if rw.Code != 201 {
		t.Fatalf("status = %d", rw.Code)
	}
	if ct := rw.Header().Get(headers.NameContentType); ct != headers.ValueApplicationJSON {
		t.Fatalf("content-type = %q", ct)
	}

	rw = httptest.NewRecorder()
	frb := DefaultJSONRequestBody()
	if err := marshalTimeseriesCSVWriter(ds, frb, 202, rw); err != nil {
		t.Fatal(err)
	}
	if rw.Code != 202 {
		t.Fatalf("status = %d", rw.Code)
	}
	if ct := rw.Header().Get(headers.NameContentType); ct != headers.ValueApplicationCSV {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestWriteJSONMultipleResults(t *testing.T) {
	t.Parallel()

	ds := testDataSet()
	ds.Results = append(ds.Results, &dataset.Result{
		SeriesList: []*dataset.Series{ds.Results[0].SeriesList[0]},
	})
	ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, ds.Results[0].SeriesList[0])

	buf := new(bytes.Buffer)
	if err := writeJSON(ds, buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, `"tables":[`) != 2 {
		t.Fatalf("expected two results, got %s", out)
	}
}

func TestUnmarshalTimeseriesReaderErrors(t *testing.T) {
	t.Parallel()

	_, err := UnmarshalTimeseriesReader(strings.NewReader(""), nil)
	if err != timeseries.ErrNoTimerangeQuery {
		t.Fatalf("nil trq = %v", err)
	}

	_, err = UnmarshalTimeseriesReader(strings.NewReader("a\n\"unterminated"), &timeseries.TimeRangeQuery{})
	if err == nil {
		t.Fatal("expected csv read error")
	}

	_, err = UnmarshalTimeseriesReader(strings.NewReader("a,b\nc,d\n"), &timeseries.TimeRangeQuery{})
	if err != timeseries.ErrInvalidBody {
		t.Fatalf("short body = %v", err)
	}

	bad := "#datatype,a\n#group,a\nnot-annotation,a\n,header\n"
	_, err = UnmarshalTimeseriesReader(strings.NewReader(bad), &timeseries.TimeRangeQuery{})
	if err != timeseries.ErrInvalidBody {
		t.Fatalf("missing annotation = %v", err)
	}
}

func TestBuildFieldDefinitionsErrors(t *testing.T) {
	t.Parallel()

	_, err := buildFieldDefinitions([][]string{
		{"#datatype", "string"},
		{"#group", "false"},
		{"#default", ""},
		{","},
	}, nil)
	if err != timeseries.ErrInvalidBody {
		t.Fatalf("short header = %v", err)
	}

	_, err = buildFieldDefinitions([][]string{
		{"#datatype", "string", "long", "double"},
		{"#group", "false", "false"},
		{"#default", "", "", ""},
		{"", "result", "table", "v"},
	}, nil)
	if err != timeseries.ErrInvalidBody {
		t.Fatalf("mismatched lengths = %v", err)
	}
}

func TestTypeToFieldDataTypeUnknown(t *testing.T) {
	t.Parallel()
	if got := typeToFieldDataType("nope"); got != timeseries.Unknown {
		t.Fatalf("got %v", got)
	}
}

func TestParseTimeField(t *testing.T) {
	t.Parallel()

	e, err := parseTimeField("2020-01-01T00:00:00Z", timeseries.FieldDefinition{
		DataType: timeseries.DateTimeRFC3339,
	})
	if err != nil || e == 0 {
		t.Fatalf("rfc3339 = (%v, %v)", e, err)
	}

	_, err = parseTimeField("2020-01-01T00:00:00Z", timeseries.FieldDefinition{
		DataType: timeseries.DateTimeRFC3339Nano,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = parseTimeField("bad", timeseries.FieldDefinition{
		DataType: timeseries.DateTimeRFC3339,
	})
	if err == nil {
		t.Fatal("expected parse error")
	}

	_, err = parseTimeField("2020-01-01T00:00:00Z", timeseries.FieldDefinition{})
	if err != timeseries.ErrInvalidTimeFormat {
		t.Fatalf("invalid format = %v", err)
	}
}
