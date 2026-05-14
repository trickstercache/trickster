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

package sql

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func testTRQ() *timeseries.TimeRangeQuery {
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
	return &timeseries.TimeRangeQuery{
		Extent: timeseries.Extent{Start: t1, End: t2},
		TimestampDefinition: timeseries.FieldDefinition{
			Name: "time",
		},
		ParsedQuery: &Query{},
	}
}

func TestUnmarshalJSON(t *testing.T) {
	data := []byte(`[{"time":"2024-01-01T00:00:00Z","temperature":72.5},{"time":"2024-01-01T01:00:00Z","temperature":73.2}]`)
	trq := testTRQ()
	ts, err := UnmarshalTimeseries(data, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	if len(ds.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(ds.Results))
	}
	if len(ds.Results[0].SeriesList) != 1 {
		t.Fatalf("expected 1 series, got %d", len(ds.Results[0].SeriesList))
	}
	if len(ds.Results[0].SeriesList[0].Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(ds.Results[0].SeriesList[0].Points))
	}
}

func TestUnmarshalJSONL(t *testing.T) {
	data := []byte("{\"time\":\"2024-01-01T00:00:00Z\",\"temperature\":72.5}\n{\"time\":\"2024-01-01T01:00:00Z\",\"temperature\":73.2}\n")
	trq := testTRQ()
	ts, err := UnmarshalTimeseries(data, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	if len(ds.Results[0].SeriesList[0].Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(ds.Results[0].SeriesList[0].Points))
	}
}

func TestUnmarshalCSV(t *testing.T) {
	data := []byte("time,temperature\n2024-01-01T00:00:00Z,72.5\n2024-01-01T01:00:00Z,73.2\n")
	trq := testTRQ()
	ts, err := UnmarshalTimeseries(data, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	if len(ds.Results[0].SeriesList[0].Points) != 2 {
		t.Fatalf("expected 2 points, got %d", len(ds.Results[0].SeriesList[0].Points))
	}
}

func TestUnmarshalEmpty(t *testing.T) {
	data := []byte(`[]`)
	trq := testTRQ()
	ts, err := UnmarshalTimeseries(data, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	if len(ds.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(ds.Results))
	}
}

func TestUnmarshalNilData(t *testing.T) {
	_, err := UnmarshalTimeseries(nil, testTRQ())
	if err == nil {
		t.Error("expected error for nil data")
	}
}

func TestUnmarshalJSONNullThenNumeric(t *testing.T) {
	data := []byte(`[{"time":"2024-01-01T00:00:00Z","temperature":null},{"time":"2024-01-01T01:00:00Z","temperature":72.5}]`)
	trq := testTRQ()
	ts, err := UnmarshalTimeseries(data, trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	pts := ds.Results[0].SeriesList[0].Points
	if len(pts) != 2 {
		t.Fatalf("expected 2 points, got %d", len(pts))
	}
}

func TestRoundTripJSON(t *testing.T) {
	ds := testDataSet()
	rlo := &timeseries.RequestOptions{OutputFormat: 32} // V3OutputJSON
	data, err := MarshalTimeseries(ds, rlo, 200)
	if err != nil {
		t.Fatal(err)
	}
	trq := testTRQ()
	ts2, err := UnmarshalTimeseries(data, trq)
	if err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	ds2, ok := ts2.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	if len(ds2.Results[0].SeriesList[0].Points) != 2 {
		t.Fatalf("expected 2 points after round-trip, got %d",
			len(ds2.Results[0].SeriesList[0].Points))
	}
}
