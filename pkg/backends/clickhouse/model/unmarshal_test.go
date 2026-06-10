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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func TestStripSize(t *testing.T) {
	input := "myInput(10)"
	out := stripSize(input)
	if out != "myInput" {
		t.Error("failed to properly strip size")
	}
}

func TestUnmarshalTimeseries(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testDataTSVWithNamesAndTypes), testTRQ.Clone())
	if err != nil {
		t.Error(err)
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil {
		t.Error("expected non-nil dataset")
		return
	}

	el := testDataSet().ExtentList

	if len(ds.ExtentList) != 1 || !ds.ExtentList[0].Start.Equal(el[0].Start) ||
		!ds.ExtentList[0].End.Equal(el[0].End) {
		t.Error("unexpected extents: ", ds.ExtentList)
	}
}

func TestUnmarshalTimeseriesReaderErrors(t *testing.T) {
	_, err := UnmarshalTimeseriesReader(strings.NewReader("only-one-row"), testTRQ.Clone())
	if err != timeseries.ErrInvalidBody {
		t.Fatalf("UnmarshalTimeseriesReader() = %v, want ErrInvalidBody", err)
	}
}

func TestTypeToFieldDataType(t *testing.T) {
	tests := map[string]timeseries.FieldDataType{
		"String":    timeseries.String,
		"UUID":      timeseries.String,
		"FixedString(16)": timeseries.String,
		"Int64":     timeseries.Int64,
		"UInt64":    timeseries.Uint64,
		"Float64":   timeseries.Float64,
		"Decimal128(18)": timeseries.Float64,
		"DateTime":  timeseries.DateTimeSQL,
		"DateTime64(3)": timeseries.DateTimeSQL,
		"Date":      timeseries.DateSQL,
		"Nothing":   timeseries.Null,
		"UnknownType": timeseries.Unknown,
	}
	for input, want := range tests {
		if got := typeToFieldDataType(input); got != want {
			t.Errorf("typeToFieldDataType(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestLoadFieldDef(t *testing.T) {
	trq := testTRQ.Clone()

	fd := loadFieldDef("", "String", 0, trq)
	if fd.OutputPosition != -1 {
		t.Fatalf("empty field OutputPosition = %d, want -1", fd.OutputPosition)
	}

	fd = loadFieldDef("t", "DateTime", 0, trq)
	if fd.Role != timeseries.RoleTimestamp || fd.DataType != timeseries.DateTimeSQL {
		t.Fatalf("DateTime timestamp field = %+v", fd)
	}

	fd = loadFieldDef("t", "Date", 0, trq)
	if fd.DataType != timeseries.DateSQL {
		t.Fatalf("Date timestamp DataType = %v, want DateSQL", fd.DataType)
	}

	fd = loadFieldDef("hostname", "String", 1, trq)
	if fd.Role != timeseries.RoleTag {
		t.Fatalf("tag field role = %v, want RoleTag", fd.Role)
	}

	trq.TimestampDefinition.DataType = timeseries.DateTimeUnixMilli
	fd = loadFieldDef("t", "UnknownType", 0, trq)
	if fd.DataType != timeseries.DateTimeUnixMilli {
		t.Fatalf("custom timestamp DataType = %v, want DateTimeUnixMilli", fd.DataType)
	}
}

func TestBuildFieldDefinitionsInvalidRows(t *testing.T) {
	rows := [][]string{
		{"a", "b", "c"},
		{"String", "String"},
	}
	_, err := buildFieldDefinitions(rows, testTRQ.Clone())
	if err != timeseries.ErrInvalidBody {
		t.Fatalf("buildFieldDefinitions() = %v, want ErrInvalidBody", err)
	}
}

func TestParseTimeField(t *testing.T) {
	tfd := timeseries.FieldDefinition{DataType: timeseries.DateTimeSQL}
	ep, err := parseTimeField("2020-01-01 12:00:00", tfd)
	if err != nil {
		t.Fatalf("parseTimeField DateTimeSQL: %v", err)
	}
	if ep == 0 {
		t.Fatal("expected non-zero epoch")
	}

	tfd = timeseries.FieldDefinition{DataType: timeseries.DateSQL}
	ep, err = parseTimeField("2020-01-01", tfd)
	if err != nil {
		t.Fatalf("parseTimeField DateSQL: %v", err)
	}
	if ep == 0 {
		t.Fatal("expected non-zero epoch")
	}

	tfd = timeseries.FieldDefinition{DataType: timeseries.Uint64}
	ep, err = parseTimeField("1577836800", tfd)
	if err != nil {
		t.Fatalf("parseTimeField seconds: %v", err)
	}
	want := epoch.Epoch(int64(1577836800) * 1000000000)
	if ep != want {
		t.Fatalf("seconds epoch = %v, want %v", ep, want)
	}

	ep, err = parseTimeField("1577836800000", tfd)
	if err != nil {
		t.Fatalf("parseTimeField milliseconds: %v", err)
	}
	want = epoch.Epoch(int64(1577836800000) * 1000000)
	if ep != want {
		t.Fatalf("milliseconds epoch = %v, want %v", ep, want)
	}

	ep, err = parseTimeField("1577836800000000", tfd)
	if err != nil {
		t.Fatalf("parseTimeField microseconds: %v", err)
	}
	want = epoch.Epoch(int64(1577836800000000) * 1000)
	if ep != want {
		t.Fatalf("microseconds epoch = %v, want %v", ep, want)
	}

	ep, err = parseTimeField("1577836800000000000", tfd)
	if err != nil {
		t.Fatalf("parseTimeField nanoseconds: %v", err)
	}
	want = epoch.Epoch(1577836800000000000)
	if ep != want {
		t.Fatalf("nanoseconds epoch = %v, want %v", ep, want)
	}

	tfd = timeseries.FieldDefinition{DataType: timeseries.TimeSQL}
	ep, err = parseTimeField("12:00:00", tfd)
	if err != nil {
		t.Fatalf("parseTimeField TimeSQL: %v", err)
	}
	if ep == 0 {
		t.Fatal("expected non-zero epoch for TimeSQL")
	}

	tfd = timeseries.FieldDefinition{DataType: timeseries.Uint64}
	_, err = parseTimeField("not-a-time", tfd)
	if err != timeseries.ErrInvalidTimeFormat {
		t.Fatalf("parseTimeField invalid = %v, want ErrInvalidTimeFormat", err)
	}
}

func TestParseClickHouseTimestamp(t *testing.T) {
	for i := 1; i <= 9; i++ {
		frac := strings.Repeat("0", i)
		input := "2020-01-01 12:00:00." + frac
		if _, err := parseClickHouseTimestamp("", input); err != nil {
			t.Fatalf("parseClickHouseTimestamp(%q) = %v", input, err)
		}
	}

	for _, input := range []string{
		"2020-01-01 12:00:00",
		"2020-01-01 12:00:00.",
	} {
		if _, err := parseClickHouseTimestamp("", input); err != nil {
			t.Fatalf("parseClickHouseTimestamp(%q) = %v", input, err)
		}
	}

	longFrac := "2020-01-01 12:00:00." + strings.Repeat("1", 12)
	if _, err := parseClickHouseTimestamp("", longFrac); err != nil {
		t.Fatalf("parseClickHouseTimestamp long fraction = %v", err)
	}
}
