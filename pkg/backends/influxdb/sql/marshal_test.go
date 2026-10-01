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
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/iofmt"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
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
					dataset.NewSeries(dataset.SeriesHeader{
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
					}, dataset.Points{
						{Epoch: epoch.Epoch(t1.UnixNano()), Values: []any{float64(72.5)}},
						{Epoch: epoch.Epoch(t2.UnixNano()), Values: []any{float64(73.2)}},
					}),
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

func TestMarshalOrdersRowsAcrossSeries(t *testing.T) {
	ds := testDataSet()
	t1 := ds.TimeRangeQuery.Extent.Start
	t2 := ds.TimeRangeQuery.Extent.End
	first := ds.Results[0].SeriesList[0]
	first.Header.TagFieldsList = timeseries.FieldDefinitions{{Name: "host", Role: timeseries.RoleTag}}
	first.Header.Tags = map[string]string{"host": "a"}
	second := dataset.NewSeries(first.Header, dataset.Points{{Epoch: epoch.Epoch(t1.UnixNano()), Values: []any{float64(80)}}})
	second.Header.Tags = map[string]string{"host": "b"}
	ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, second)
	ds.TimeRangeQuery.Ordering = []timeseries.OrderTerm{
		{Column: "time", Descending: true}, {Column: "host"},
	}

	data, err := MarshalTimeseries(ds,
		&timeseries.RequestOptions{OutputFormat: iofmt.V3OutputJSON}, 200)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	wantTimes := []string{
		t2.Format(v3TimestampOutputLayout),
		t1.Format(v3TimestampOutputLayout),
		t1.Format(v3TimestampOutputLayout),
	}
	wantHosts := []string{"a", "a", "b"}
	for i := range wantTimes {
		if rows[i]["time"] != wantTimes[i] || rows[i]["host"] != wantHosts[i] {
			t.Fatalf("row %d = %v, want time=%s host=%s", i, rows[i], wantTimes[i], wantHosts[i])
		}
	}
}

func TestMarshalNilTimeseries(t *testing.T) {
	rlo := &timeseries.RequestOptions{OutputFormat: iofmt.V3OutputJSON}
	_, err := MarshalTimeseries(nil, rlo, 200)
	if err == nil {
		t.Error("expected error for nil timeseries")
	}
}

func TestMarshalWritesNothingForAValueJSONCannotHold(t *testing.T) {
	for _, of := range []byte{iofmt.V3OutputJSON, iofmt.V3OutputJSONL} {
		rlo := &timeseries.RequestOptions{OutputFormat: of}
		ds := testDataSet()
		s := ds.Results[0].SeriesList[0]
		pts := dspoints.Of(s)
		// a value past the series' columns is never written, so it can't fail the marshal
		pts[0].Values = append(pts[0].Values, math.NaN())
		s.SetPoints(pts)
		var w bytes.Buffer
		if err := MarshalTimeseriesWriter(ds, rlo, 200, &w); err != nil || w.Len() == 0 {
			t.Fatalf("format %d: %v", of, err)
		}
		pts[1].Values[0] = math.Inf(1)
		s.SetPoints(pts)
		w.Reset()
		if err := MarshalTimeseriesWriter(ds, rlo, 200, &w); err == nil || w.Len() != 0 {
			t.Fatalf("format %d: wrote %q, %v", of, w.Bytes(), err)
		}
	}
}

func BenchmarkMarshalJSON(b *testing.B) {
	for _, shape := range []struct{ series, points int }{{100, 1000}, {10, 60}} {
		ds := &dataset.DataSet{Results: dataset.Results{{}}}
		for i := range shape.series {
			s := dataset.NewSeries(dataset.SeriesHeader{
				TimestampField:  timeseries.FieldDefinition{Name: "time"},
				TagFieldsList:   timeseries.FieldDefinitions{{Name: "host"}},
				ValueFieldsList: timeseries.FieldDefinitions{{Name: "usage_user"}, {Name: "count"}},
				Tags:            dataset.Tags{"host": fmt.Sprintf("host-%d", i)},
			}, nil)
			pts := make(dataset.Points, shape.points)
			for j := range pts {
				pts[j] = dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
					Values: []any{float64(i*j%9973) / 7, int64(j)}}
			}
			s.SetPoints(pts)
			ds.Results[0].SeriesList = append(ds.Results[0].SeriesList, s)
		}
		b.Run(fmt.Sprintf("%dx%d", shape.series, shape.points), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := MarshalTimeseriesWriter(ds, nil, 200, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestMarshalReadsSeriesParts(t *testing.T) {
	ds := testDataSet()
	s := ds.Results[0].SeriesList[0]
	s.Header.TagFieldsList = timeseries.FieldDefinitions{{Name: "host"}}
	s.Header.Tags = map[string]string{"host": "a"}
	view := parts.Of(ds, epoch.Epoch(time.Minute))
	if !view.HasParts() {
		t.Fatal("the view has no parts")
	}
	for _, rlo := range []*timeseries.RequestOptions{nil, {OutputFormat: iofmt.V3OutputJSONL}, {OutputFormat: iofmt.V3OutputCSV}} {
		for _, ordering := range [][]timeseries.OrderTerm{nil, {{Column: "time", Descending: true}}} {
			view.TimeRangeQuery.Ordering = ordering
			var got, want bytes.Buffer
			if err := MarshalTimeseriesWriter(view, rlo, 200, &got); err != nil {
				t.Fatal(err)
			}
			if err := MarshalTimeseriesWriter(view.Flat(), rlo, 200, &want); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) || got.Len() == 0 {
				t.Fatalf("got %s\nwant %s", got.Bytes(), want.Bytes())
			}
		}
	}
}
