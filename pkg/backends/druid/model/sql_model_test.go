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
	"reflect"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func testSQLPlan() *SQLQueryPlan {
	return NewSQLQueryPlanWithResponseShape(&sqlanalyzer.QueryPlan{
		CanonicalSQL: "SELECT ...",
		TimeColumn:   "__time",
		OutputColumn: "bucket",
		Step:         time.Hour,
		OutputUnit:   timeseries.DateTimeRFC3339Nano,
		GroupColumns: []string{"host"},
	}, nil, SQLResponseObject, false, "bucket", "host", "value")
}

func testSQLTRQ(plan *SQLQueryPlan) *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{
		Statement: "SELECT ...",
		Extent: timeseries.Extent{
			Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC),
		},
		Step:   time.Hour,
		StepNS: time.Hour.Nanoseconds(),
		TimestampDefinition: timeseries.FieldDefinition{
			Name: "bucket", DataType: timeseries.DateTimeRFC3339Nano,
			Role: timeseries.RoleTimestamp,
		},
		TagFieldDefintions: timeseries.FieldDefinitions{{Name: "host", Role: timeseries.RoleTag}},
		ParsedQuery:        plan,
	}
}

func TestSQLQueryPlanDefaultResponseFormat(t *testing.T) {
	plan := NewSQLQueryPlan(&sqlanalyzer.QueryPlan{}, nil)
	if got := plan.ResponseFormat(); got != SQLResponseObject {
		t.Fatalf("default response format = %d, want object (%d)",
			got, SQLResponseObject)
	}
}

func TestSQLModelRoundTripObjectRows(t *testing.T) {
	plan := testSQLPlan()
	trq := testSQLTRQ(plan)
	body := []byte(`[{"bucket":"2024-01-01T00:00:00.000Z","host":"a","value":1.5},{"bucket":"2024-01-01T01:00:00.000Z","host":"a","value":2.5},{"bucket":"2024-01-01T00:00:00.000Z","host":"b","value":3}]`)
	ts, err := UnmarshalTimeseries(body, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 2 {
		t.Fatalf("unexpected series: %#v", ds.Results)
	}
	if len(ds.Results[0].SeriesList[0].Points) != 2 {
		t.Fatalf("unexpected points: %#v", ds.Results[0].SeriesList[0].Points)
	}
	out, err := MarshalTimeseries(ds, &timeseries.RequestOptions{ProviderRequest: plan}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\ngot=%s\nwant=%s", out, body)
	}
}

func TestSQLModelRoundTripArrayRowsWithHeader(t *testing.T) {
	plan := NewSQLQueryPlanWithResponseShape(testSQLPlan().Plan, nil,
		SQLResponseArray, true, "bucket", "host", "value")
	trq := testSQLTRQ(plan)
	body := []byte(`[["bucket","host","value"],["2024-01-01T00:00:00.000Z","a",1.5],["2024-01-01T01:00:00.000Z","a",2.5],["2024-01-01T00:00:00.000Z","b",3]]`)
	ts, err := UnmarshalTimeseries(body, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 2 {
		t.Fatalf("unexpected series: %#v", ds.Results)
	}
	out, err := MarshalTimeseries(ds, &timeseries.RequestOptions{ProviderRequest: plan}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("array round trip mismatch\ngot=%s\nwant=%s", out, body)
	}
}

func TestSQLModelEmptyArrayPreservesSelectHeader(t *testing.T) {
	plan := NewSQLQueryPlanWithResponseShape(testSQLPlan().Plan, nil,
		SQLResponseArray, true, "bucket", "value", "host")
	trq := testSQLTRQ(plan)
	ts, err := UnmarshalTimeseries([]byte(`[["bucket","value","host"]]`), trq)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalTimeseries(ts,
		&timeseries.RequestOptions{ProviderRequest: plan}, 200)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `[["bucket","value","host"]]`+"\n"; got != want {
		t.Fatalf("empty array response = %q, want %q", got, want)
	}
}

func TestSQLModelAcceptsDruidNumericMilliseconds(t *testing.T) {
	plan := testSQLPlan()
	trq := testSQLTRQ(plan)
	ts, err := UnmarshalTimeseries([]byte(`[{"bucket":1704067200000,"host":"a","value":1}]`), trq)
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if got := ds.Results[0].SeriesList[0].Points[0].Epoch; got != 1704067200000000000 {
		t.Fatalf("epoch = %d", got)
	}
}

func TestSQLModelAcceptsRowsWithDifferentObjectOrder(t *testing.T) {
	plan := testSQLPlan()
	trq := testSQLTRQ(plan)
	body := []byte(`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1},{"value":2,"host":"a","bucket":"2024-01-01T01:00:00Z"}]`)
	ts, err := UnmarshalTimeseries(body, trq)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalTimeseries(ts, &timeseries.RequestOptions{ProviderRequest: plan}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1]["value"] != float64(2) {
		t.Fatalf("unexpected rows: %s", out)
	}
}

func TestSQLModelPreservesTypedGroupColumns(t *testing.T) {
	shared := testSQLPlan().Plan
	shared.GroupColumns = []string{"code", "active", "missing"}
	plan := NewSQLQueryPlanWithResponseShape(shared, nil, SQLResponseArray, true,
		"bucket", "code", "active", "missing", "value")
	trq := testSQLTRQ(plan)
	body := []byte(`[["bucket","code","active","missing","value"],` +
		`["2024-01-01T00:00:00.000Z",7,true,null,1.5]]`)
	ts, err := UnmarshalTimeseries(body, trq)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalTimeseries(ts,
		&timeseries.RequestOptions{ProviderRequest: plan}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed group round trip mismatch\ngot=%s\nwant=%s", out, body)
	}
}

func TestSQLModelDistinguishesNullAndStringGroupValues(t *testing.T) {
	shared := testSQLPlan().Plan
	shared.GroupColumns = []string{"label"}
	plan := NewSQLQueryPlanWithResponseShape(shared, nil, SQLResponseArray, true,
		"bucket", "label", "value")
	trq := testSQLTRQ(plan)
	body := []byte(`[["bucket","label","value"],` +
		`["2024-01-01T00:00:00.000Z",null,1],` +
		`["2024-01-01T00:00:00.000Z","null",2]]`)
	ts, err := UnmarshalTimeseries(body, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds := ts.(*dataset.DataSet)
	if got := len(ds.Results[0].SeriesList); got != 2 {
		t.Fatalf("series count = %d, want 2", got)
	}
	out, err := MarshalTimeseries(ts,
		&timeseries.RequestOptions{ProviderRequest: plan}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]any
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}
	var foundNull, foundString bool
	for _, row := range rows[1:] {
		if row[1] == nil {
			foundNull = true
		}
		if row[1] == "null" {
			foundString = true
		}
	}
	if !foundNull || !foundString {
		t.Fatalf("group values were not preserved: %s", out)
	}
}

func TestSQLModelMergesNullOnlyAndNumericExtents(t *testing.T) {
	plan := testSQLPlan()
	firstTRQ := testSQLTRQ(plan)
	secondTRQ := testSQLTRQ(plan)
	secondTRQ.Extent.Start = secondTRQ.Extent.Start.Add(time.Hour)
	secondTRQ.Extent.End = secondTRQ.Extent.End.Add(time.Hour)

	first, err := UnmarshalTimeseries([]byte(
		`[{"bucket":"2024-01-01T00:00:00.000Z","host":"a","value":null}]`), firstTRQ)
	if err != nil {
		t.Fatal(err)
	}
	second, err := UnmarshalTimeseries([]byte(
		`[{"bucket":"2024-01-01T01:00:00.000Z","host":"a","value":2}]`), secondTRQ)
	if err != nil {
		t.Fatal(err)
	}
	first.Merge(true, second)
	ds := first.(*dataset.DataSet)
	if got := len(ds.Results[0].SeriesList); got != 1 {
		t.Fatalf("series count after merge = %d, want 1", got)
	}
	if got := len(ds.Results[0].SeriesList[0].Points); got != 2 {
		t.Fatalf("point count after merge = %d, want 2", got)
	}
}

func TestSQLModelRejectsMalformedRows(t *testing.T) {
	plan := testSQLPlan()
	trq := testSQLTRQ(plan)
	for _, body := range []string{
		`[{"bucket":"bad","host":"a","value":1}]`,
		`[{"bucket":1704067200000.5,"host":"a","value":1}]`,
		`[{"bucket":9223372036854775808,"host":"a","value":1}]`,
		`[{"bucket":"2024-01-01T00:00:00Z","host":"a"}]`,
		`[{"bucket":"2024-01-01T00:00:00Z","host":"a"},{"bucket":"2024-01-01T01:00:00Z","host":"a","value":1}]`,
		`[{"bucket":"2024-01-01T00:00:00Z","host":"a","value":1}] {}`,
	} {
		if _, err := UnmarshalTimeseries([]byte(body), trq); err == nil {
			t.Fatalf("body unexpectedly accepted: %s", body)
		}
	}
	var buf bytes.Buffer
	if err := MarshalTimeseriesWriter(&dataset.DataSet{}, &timeseries.RequestOptions{ProviderRequest: plan}, 200, &buf); err != nil {
		// An empty DataSet is a valid SQL response and should still marshal.
		t.Fatal(err)
	}
	if string(buf.Bytes()) != "[]\n" {
		t.Fatalf("empty output = %q", buf.String())
	}
}
