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
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func testDataSet() *dataset.DataSet {
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC)
	return &dataset.DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{
			Extent: timeseries.Extent{Start: t1, End: t2},
		},
		ExtentList: timeseries.ExtentList{{Start: t1, End: t2}},
		Results: dataset.Results{
			&dataset.Result{
				SeriesList: dataset.SeriesList{
					&dataset.Series{
						Header: dataset.SeriesHeader{
							Name: "default",
							TimestampField: timeseries.FieldDefinition{
								Name:     "time",
								DataType: timeseries.DateTimeRFC3339Nano,
								Role:     timeseries.RoleTimestamp,
							},
							ValueFieldsList: timeseries.FieldDefinitions{
								{Name: "temperature", DataType: timeseries.Float64, OutputPosition: 0, Role: timeseries.RoleValue},
							},
							Tags: map[string]string{},
						},
						Points: dataset.Points{
							{Epoch: epoch.Epoch(t1.UnixNano()), Values: []any{float64(72.5)}},
							{Epoch: epoch.Epoch(t2.UnixNano()), Values: []any{float64(73.2)}},
						},
					},
				},
			},
		},
	}
}

func TestMarshalJSON(t *testing.T) {
	ds := testDataSet()
	rlo := &timeseries.RequestOptions{OutputFormat: iofmt.V3OutputJSON}
	data, err := MarshalTimeseries(ds, rlo, 200)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if _, ok := rows[0]["time"]; !ok {
		t.Error("missing time field")
	}
	if _, ok := rows[0]["temperature"]; !ok {
		t.Error("missing temperature field")
	}
}

func TestMarshalJSONL(t *testing.T) {
	ds := testDataSet()
	rlo := &timeseries.RequestOptions{OutputFormat: iofmt.V3OutputJSONL}
	var buf bytes.Buffer
	if err := MarshalTimeseriesWriter(ds, rlo, 200, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for _, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("invalid JSONL line: %v", err)
		}
	}
}

func TestMarshalCSV(t *testing.T) {
	ds := testDataSet()
	rlo := &timeseries.RequestOptions{OutputFormat: iofmt.V3OutputCSV}
	data, err := MarshalTimeseries(ds, rlo, 200)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 { // header + 2 data rows
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "time") {
		t.Error("missing time in header")
	}
	if !strings.Contains(lines[0], "temperature") {
		t.Error("missing temperature in header")
	}
}

func TestMarshalNilTimeseries(t *testing.T) {
	rlo := &timeseries.RequestOptions{OutputFormat: iofmt.V3OutputJSON}
	_, err := MarshalTimeseries(nil, rlo, 200)
	if err == nil {
		t.Error("expected error for nil timeseries")
	}
}
