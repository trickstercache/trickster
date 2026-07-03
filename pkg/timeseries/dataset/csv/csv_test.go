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

package csv

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func testFieldParser(_ [][]string, _ *timeseries.TimeRangeQuery) (timeseries.SeriesFields, error) {
	return timeseries.SeriesFields{
		Timestamp: timeseries.FieldDefinition{
			Name:           "time",
			OutputPosition: 2,
			DataType:       timeseries.Int64,
			Role:           timeseries.RoleTimestamp,
		},
		Tags: timeseries.FieldDefinitions{
			{
				Name:           "host",
				OutputPosition: 1,
				DataType:       timeseries.String,
				Role:           timeseries.RoleTag,
			},
		},
		Values: timeseries.FieldDefinitions{
			{
				Name:           "value",
				OutputPosition: 3,
				DataType:       timeseries.Float64,
				Role:           timeseries.RoleValue,
			},
			{
				Name:           "count",
				OutputPosition: 4,
				DataType:       timeseries.Int64,
				Role:           timeseries.RoleValue,
			},
		},
		ResultNameCol: 0,
	}, nil
}

func testDataTypeParser(string) timeseries.FieldDataType {
	return timeseries.Unknown
}

func testTimestampParser(input string, _ timeseries.FieldDefinition) (epoch.Epoch, error) {
	i, err := strconv.ParseInt(input, 10, 64)
	return epoch.Epoch(i), err
}

func testParser(t *testing.T, firstDataRow int) Parser {
	t.Helper()
	p, err := NewParser(testFieldParser, testDataTypeParser, testTimestampParser, firstDataRow)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	return p
}

func testMatrix() [][]string {
	return [][]string{
		{"result", "host", "time", "value", "count"},
		{"_result1", "host-a", "1000", "1.5", "10"},
		{"_result1", "host-b", "2000", "2.5", "20"},
		{"_result2", "host-a", "3000", "3.5", "30"},
	}
}

func testTimeRangeQuery() *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{
			Start: time.Unix(0, 1000),
			End:   time.Unix(0, 3000),
		},
		Step: time.Second,
	}
}

func TestNewParser(t *testing.T) {
	t.Parallel()

	fp := testFieldParser
	dp := testDataTypeParser
	tp := testTimestampParser

	cases := []struct {
		name    string
		fp      FieldParserFunc
		dp      DataTypeParserFunc
		tp      TimestampParserFunc
		first   int
		wantErr error
	}{
		{name: "valid", fp: fp, dp: dp, tp: tp, first: 1},
		{name: "nil-field-parser", fp: nil, dp: dp, tp: tp, first: 1, wantErr: ErrInvalidFieldParserFunc},
		{name: "nil-data-type-parser", fp: fp, dp: nil, tp: tp, first: 1, wantErr: ErrInvalidDataTypeParserFunc},
		{name: "nil-timestamp-parser", fp: fp, dp: dp, tp: nil, first: 1, wantErr: ErrInvalidTimestampParserFunc},
		{name: "negative-first-data-row-clamped", fp: fp, dp: dp, tp: tp, first: -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewParser(tc.fp, tc.dp, tc.tp, tc.first)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewParser() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewParser() unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("NewParser() returned nil parser")
			}
		})
	}
}

func TestNewParserMust(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		p := NewParserMust(testFieldParser, testDataTypeParser, testTimestampParser, 1)
		if p == nil {
			t.Fatal("NewParserMust returned nil parser")
		}
	})

	t.Run("panics-on-invalid", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected panic from NewParserMust")
			}
		}()
		NewParserMust(nil, testDataTypeParser, testTimestampParser, 1)
	})
}

func TestParserToDataSet(t *testing.T) {
	t.Parallel()

	p := testParser(t, 1)
	ds, err := p.ToDataSet(testMatrix(), testTimeRangeQuery())
	if err != nil {
		t.Fatalf("ToDataSet: %v", err)
	}
	if ds == nil {
		t.Fatal("ToDataSet returned nil dataset")
	}
	if ds.TimeRangeQuery == nil {
		t.Fatal("expected TimeRangeQuery to be set")
	}
	if len(ds.ExtentList) != 1 {
		t.Fatalf("expected 1 extent, got %d", len(ds.ExtentList))
	}
	if len(ds.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(ds.Results))
	}

	seriesByResult := map[string]int{}
	pointsBySeries := map[string]int{}
	for _, r := range ds.Results {
		seriesByResult[r.Name] = len(r.SeriesList)
		for _, s := range r.SeriesList {
			pointsBySeries[s.Header.Name] = len(s.Points)
			if s.Header.Tags["host"] == "" {
				t.Fatalf("series %q missing host tag", s.Header.Name)
			}
		}
	}

	if seriesByResult["_result1"] != 2 {
		t.Fatalf("_result1 series count = %d, want 2", seriesByResult["_result1"])
	}
	if seriesByResult["_result2"] != 1 {
		t.Fatalf("_result2 series count = %d, want 1", seriesByResult["_result2"])
	}

	for name, count := range pointsBySeries {
		if count != 1 {
			t.Fatalf("series %q point count = %d, want 1", name, count)
		}
	}

	var found dataset.Point
	for _, r := range ds.Results {
		for _, s := range r.SeriesList {
			if s.Header.Tags["host"] == "host-a" && r.Name == "_result1" {
				found = s.Points[0]
			}
		}
	}
	if found.Epoch != 1000 {
		t.Fatalf("host-a epoch = %d, want 1000", found.Epoch)
	}
	if v, ok := found.Values[0].(float64); !ok || v != 1.5 {
		t.Fatalf("host-a value = %#v, want 1.5", found.Values[0])
	}
	if v, ok := found.Values[1].(int64); !ok || v != 10 {
		t.Fatalf("host-a count = %#v, want 10", found.Values[1])
	}
}

func TestParserToTimeseries(t *testing.T) {
	t.Parallel()

	p := testParser(t, 1)
	ts, err := p.ToTimeseries(testMatrix(), testTimeRangeQuery())
	if err != nil {
		t.Fatalf("ToTimeseries: %v", err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatalf("ToTimeseries returned %T, want *dataset.DataSet", ts)
	}
	if len(ds.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(ds.Results))
	}
}

func TestParserSkipsHeaderAndMalformedRows(t *testing.T) {
	t.Parallel()

	p := testParser(t, 1)
	matrix := testMatrix()
	matrix = append(matrix, []string{"only", "three", "cells"})

	ds, err := p.ToDataSet(matrix, testTimeRangeQuery())
	if err != nil {
		t.Fatalf("ToDataSet: %v", err)
	}

	totalPoints := 0
	for _, r := range ds.Results {
		for _, s := range r.SeriesList {
			totalPoints += len(s.Points)
		}
	}
	if totalPoints != 3 {
		t.Fatalf("total points = %d, want 3", totalPoints)
	}
}

func TestParserInvalidTimestampSkipped(t *testing.T) {
	t.Parallel()

	p := testParser(t, 1)
	matrix := [][]string{
		{"result", "host", "time", "value", "count"},
		{"_result1", "host-a", "not-a-number", "1.5", "10"},
		{"_result1", "host-a", "2000", "2.5", "20"},
	}

	ds, err := p.ToDataSet(matrix, testTimeRangeQuery())
	if err != nil {
		t.Fatalf("ToDataSet: %v", err)
	}
	if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 1 {
		t.Fatalf("unexpected result layout: %#v", ds.Results)
	}
	s := ds.Results[0].SeriesList[0]
	if len(s.Points) != 1 {
		t.Fatalf("expected 1 point after skipping invalid timestamp, got %d", len(s.Points))
	}
	if s.Points[0].Epoch != 2000 {
		t.Fatalf("point epoch = %d, want 2000", s.Points[0].Epoch)
	}
}

func TestParserFieldParserError(t *testing.T) {
	t.Parallel()

	p, err := NewParser(
		func(_ [][]string, _ *timeseries.TimeRangeQuery) (timeseries.SeriesFields, error) {
			return timeseries.SeriesFields{}, timeseries.ErrInvalidBody
		},
		testDataTypeParser,
		testTimestampParser,
		1,
	)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	_, err = p.ToDataSet(testMatrix(), testTimeRangeQuery())
	if !errors.Is(err, timeseries.ErrInvalidBody) {
		t.Fatalf("ToDataSet() error = %v, want ErrInvalidBody", err)
	}
}

func TestParserInvalidTimestampField(t *testing.T) {
	t.Parallel()

	p, err := NewParser(
		func(_ [][]string, _ *timeseries.TimeRangeQuery) (timeseries.SeriesFields, error) {
			sf, _ := testFieldParser(nil, nil)
			sf.Timestamp.OutputPosition = -1
			return sf, nil
		},
		testDataTypeParser,
		testTimestampParser,
		1,
	)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	_, err = p.ToDataSet(testMatrix(), testTimeRangeQuery())
	if !errors.Is(err, timeseries.ErrInvalidBody) {
		t.Fatalf("ToDataSet() error = %v, want ErrInvalidBody", err)
	}
}

func TestParserEmptyValueSkipped(t *testing.T) {
	t.Parallel()

	p := testParser(t, 1)
	matrix := [][]string{
		{"result", "host", "time", "value", "count"},
		{"_result1", "host-a", "1000", "", "10"},
	}

	ds, err := p.ToDataSet(matrix, testTimeRangeQuery())
	if err != nil {
		t.Fatalf("ToDataSet: %v", err)
	}
	pt := ds.Results[0].SeriesList[0].Points[0]
	if pt.Values[0] != nil {
		t.Fatalf("expected empty value slot, got %#v", pt.Values[0])
	}
	if v, ok := pt.Values[1].(int64); !ok || v != 10 {
		t.Fatalf("count value = %#v, want 10", pt.Values[1])
	}
}

func TestAddValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    string
		typ      timeseries.FieldDataType
		wantVal  any
		wantSize int
	}{
		{name: "int64", input: "42", typ: timeseries.Int64, wantVal: int64(42), wantSize: 8},
		{name: "float64", input: "3.14", typ: timeseries.Float64, wantVal: 3.14, wantSize: 8},
		{name: "string", input: "hello", typ: timeseries.String, wantVal: "hello", wantSize: 5},
		{name: "bool-true", input: "true", typ: timeseries.Bool, wantVal: true, wantSize: 1},
		{name: "byte", input: "7", typ: timeseries.Byte, wantVal: int64(7), wantSize: 1},
		{name: "int16", input: "1000", typ: timeseries.Int16, wantVal: int64(1000), wantSize: 2},
		{name: "uint64", input: "9007199254740991", typ: timeseries.Uint64, wantVal: uint64(9007199254740991), wantSize: 8},
		{name: "datetime-rfc3339", input: "2020-01-02T03:04:05Z", typ: timeseries.DateTimeRFC3339, wantVal: "2020-01-02T03:04:05Z", wantSize: 20},
		{name: "unknown", input: "x", typ: timeseries.Unknown, wantVal: nil, wantSize: 0},
		{name: "null", input: "x", typ: timeseries.Null, wantVal: nil, wantSize: 0},
		{name: "invalid-int64", input: "nope", typ: timeseries.Int64, wantVal: nil, wantSize: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			vals := make([]any, 1)
			size := addValue(tc.input, vals, 0, tc.typ)
			if size != tc.wantSize {
				t.Fatalf("addValue size = %d, want %d", size, tc.wantSize)
			}
			if tc.wantVal == nil {
				if vals[0] != nil {
					t.Fatalf("addValue value = %#v, want nil", vals[0])
				}
				return
			}
			if vals[0] != tc.wantVal {
				t.Fatalf("addValue value = %#v, want %#v", vals[0], tc.wantVal)
			}
		})
	}
}

func TestSeriesKeyData(t *testing.T) {
	t.Parallel()

	sf := timeseries.SeriesFields{
		Tags: timeseries.FieldDefinitions{
			{Name: "host", OutputPosition: 1},
			{Name: "region", OutputPosition: 2},
		},
		ResultNameCol: 0,
	}

	t.Run("string-with-tags", func(t *testing.T) {
		t.Parallel()
		row := []string{"result-a", "host-1", "us-east"}
		item := getSeriesKeyData(row, sf)
		if item.resultID != "result-a" {
			t.Fatalf("resultID = %q, want result-a", item.resultID)
		}
		want := seriesKeyData{
			{name: "host", value: "host-1"},
			{name: "region", value: "us-east"},
		}.String("result-a")
		if item.s != want {
			t.Fatalf("series key = %q, want %q", item.s, want)
		}
	})

	t.Run("map", func(t *testing.T) {
		t.Parallel()
		row := []string{"result-a", "host-1", "us-east"}
		item := getSeriesKeyData(row, sf)
		m := item.seriesKeyData.Map()
		if m["host"] != "host-1" || m["region"] != "us-east" {
			t.Fatalf("unexpected tag map: %#v", m)
		}
	})

	t.Run("skips-empty-tag-values", func(t *testing.T) {
		t.Parallel()
		row := []string{"result-a", "host-1", ""}
		item := getSeriesKeyData(row, sf)
		if len(item.seriesKeyData) != 1 {
			t.Fatalf("seriesKeyData length = %d, want 1", len(item.seriesKeyData))
		}
		want := seriesKeyData{{name: "host", value: "host-1"}}.String("result-a")
		if item.s != want {
			t.Fatalf("series key = %q, want %q", item.s, want)
		}
	})

	t.Run("string-skips-empty-name-or-value", func(t *testing.T) {
		t.Parallel()
		d := seriesKeyData{
			{name: "", value: "ignored"},
			{name: "host", value: ""},
			{name: "region", value: "west"},
		}
		got := d.String("result-b")
		want := seriesKeyData{{name: "region", value: "west"}}.String("result-b")
		if got != want {
			t.Fatalf("String() = %q, want %q", got, want)
		}
	})
}
