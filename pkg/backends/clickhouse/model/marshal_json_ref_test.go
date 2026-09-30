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
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

// the WFDocument encoding/json was given before the marshaler appended its output directly
func referenceWireFormat(ds *dataset.DataSet) (*WFDocument, error) {
	fds, _, _, _ := ds.FieldDefinitions()
	fieldCount := len(fds)
	d := &WFDocument{
		Meta: make(WFMeta, fieldCount),
	}
	for _, fd := range fds {
		if fd.OutputPosition >= fieldCount || fd.OutputPosition < 0 {
			continue
		}
		d.Meta[fd.OutputPosition] = WFMetaItem{
			Name: fd.Name,
			Type: fd.SDataType,
		}
	}
	var maxRowCount, k int
	if len(ds.Results) == 0 {
		d.Rows = &k
		return d, nil
	}
	for _, s := range ds.Results[0].SeriesList {
		maxRowCount += len(s.Points)
	}
	data := make(WFData, maxRowCount)
	for _, s := range ds.Results[0].SeriesList {
		for _, p := range s.Points {
			item := make(WFDataItem, fieldCount)
			var i int
			for _, fd := range fds {
				if fd.OutputPosition > fieldCount {
					continue
				}
				switch fd.Role {
				case timeseries.RoleTimestamp:
					item[fd.OutputPosition] = WFDataItemElement{
						Key:   d.Meta[fd.OutputPosition].Name,
						Value: p.Epoch.Format(ds.TimeRangeQuery.TimestampDefinition.DataType, false),
					}
				case timeseries.RoleTag:
					item[fd.OutputPosition] = WFDataItemElement{
						Key:   d.Meta[fd.OutputPosition].Name,
						Value: s.Header.Tags[fd.Name],
					}
				case timeseries.RoleValue:
					if i >= len(p.Values) {
						continue
					}
					item[fd.OutputPosition] = WFDataItemElement{
						Key:   d.Meta[fd.OutputPosition].Name,
						Value: fmt.Sprintf("%v", p.Values[i]),
					}
					i++
				}
			}
			var j int
			for i := range item {
				if item[i].Key == "" {
					continue
				}
				item[j] = item[i]
				j++
			}
			data[k] = item[:j]
			k++
		}
	}
	d.Data = data[:k]
	d.Rows = &k
	return d, nil
}

func requireJSONReference(t *testing.T, name string, ds *dataset.DataSet) {
	t.Helper()
	wf, err := referenceWireFormat(ds)
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	if err := json.NewEncoder(&want).Encode(wf); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if err := marshalTimeseriesJSON(&got, ds, nil, 200); err != nil || !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("%s:\n got %s, %v\nwant %s", name, got.Bytes(), err, want.Bytes())
	}
}

func jsonTestDataSet(tf timeseries.FieldDataType, fds []timeseries.FieldDefinition, series ...*dataset.Series,
) *dataset.DataSet {
	for _, s := range series {
		for _, fd := range fds {
			switch fd.Role {
			case timeseries.RoleTimestamp:
				s.Header.TimestampField = fd
			case timeseries.RoleTag:
				s.Header.TagFieldsList = append(s.Header.TagFieldsList, fd)
			default:
				s.Header.ValueFieldsList = append(s.Header.ValueFieldsList, fd)
			}
		}
	}
	return &dataset.DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{TimestampDefinition: timeseries.FieldDefinition{DataType: tf}},
		Results:        []*dataset.Result{{SeriesList: series}},
	}
}

func TestMarshalJSONMatchesEncodingJSON(t *testing.T) {
	requireJSONReference(t, "fixture", testDataSet())
	requireJSONReference(t, "no results", &dataset.DataSet{})
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime", OutputPosition: 0},
		{Name: "host<1>", Role: timeseries.RoleTag, SDataType: "String", OutputPosition: 1},
		{Name: "v", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 3},
		{Name: "w", Role: timeseries.RoleValue, OutputPosition: 2},
		{Name: "", Role: timeseries.RoleValue, SDataType: "Int64", OutputPosition: 4},
		{Name: "u", Role: timeseries.RoleUntracked, OutputPosition: 5},
		{Name: "far", Role: timeseries.RoleValue, OutputPosition: 9},
	}
	values := [][]any{
		{1.5, "x\"<y>", nil}, {-0.0, true, int64(-3)}, {1e21, float32(0.1), uint64(7)}, {math.NaN(), math.Inf(-1)},
		{12, []byte("b"), time.Duration(5)}, {"\u2028\xff"}, {},
	}
	var points dataset.Points
	for i, v := range values {
		points = append(points, dataset.Point{Epoch: epoch.Epoch(1577836800123456789 + int64(i)*60e9), Values: v})
	}
	for _, tf := range []timeseries.FieldDataType{
		0, timeseries.DateTimeUnixSecs, timeseries.DateTimeUnixMilli, timeseries.DateTimeUnixNano,
		timeseries.DateTimeSQL, timeseries.DateSQL, timeseries.TimeSQL, timeseries.DateTimeRFC3339,
		timeseries.DateTimeRFC3339Nano,
	} {
		ds := jsonTestDataSet(tf, fds,
			&dataset.Series{Header: dataset.SeriesHeader{Tags: dataset.Tags{"host<1>": "a&b"}}, Points: points},
			&dataset.Series{Header: dataset.SeriesHeader{Tags: dataset.Tags{}}, Points: points[:2]},
			&dataset.Series{})
		requireJSONReference(t, fmt.Sprint("time format ", tf), ds)
	}
	// two fields sharing a position, the later one winning
	shared := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, OutputPosition: 0},
		{Name: "a", Role: timeseries.RoleValue, OutputPosition: 1},
		{Name: "b", Role: timeseries.RoleValue, OutputPosition: 1},
	}
	requireJSONReference(t, "shared position", jsonTestDataSet(timeseries.DateTimeUnixSecs, shared,
		&dataset.Series{Points: dataset.Points{{Epoch: 1e9, Values: []any{1.0, 2.0}}, {Epoch: 2e9, Values: []any{3.0}}}}))

	rng := weaktest.NewRand(9, 4)
	for trial := range 50 {
		width := 1 + rng.IntN(4)
		fds := []timeseries.FieldDefinition{{Name: "time", Role: timeseries.RoleTimestamp, OutputPosition: width}}
		for i := range width {
			fds = append(fds, timeseries.FieldDefinition{Name: fmt.Sprint("f", i), Role: timeseries.RoleValue,
				SDataType: "Float64", OutputPosition: i})
		}
		var series []*dataset.Series
		for range 1 + rng.IntN(3) {
			s := &dataset.Series{}
			for p := range rng.IntN(20) {
				vals := make([]any, rng.IntN(width+1))
				for i := range vals {
					vals[i] = rng.NormFloat64() * math.Pow(10, float64(rng.IntN(40)-20))
				}
				s.Points = append(s.Points, dataset.Point{Epoch: epoch.Epoch(int64(p) * 1e9), Values: vals})
			}
			series = append(series, s)
		}
		requireJSONReference(t, fmt.Sprint("trial ", trial), jsonTestDataSet(timeseries.DateTimeUnixMilli, fds, series...))
	}
}

func TestTimeOrderedRowsMatchesAStableSort(t *testing.T) {
	rng := weaktest.NewRand(14, 2)
	for trial := range 300 {
		r := &dataset.Result{}
		sorted := trial%3 != 0
		for range rng.IntN(5) {
			s := &dataset.Series{}
			at := rng.IntN(4)
			for range rng.IntN(8) {
				if sorted {
					at += rng.IntN(2)
				} else {
					at = rng.IntN(6)
				}
				s.Points = append(s.Points, dataset.Point{Epoch: epoch.Epoch(at), Values: []any{len(s.Points)}})
			}
			r.SeriesList = append(r.SeriesList, s)
		}
		var want []outputRow
		for _, s := range r.SeriesList {
			for i := range s.Points {
				want = append(want, outputRow{series: s, point: &s.Points[i]})
			}
		}
		slices.SortStableFunc(want, func(a, b outputRow) int { return cmp.Compare(a.point.Epoch, b.point.Epoch) })
		rows, n := timeOrderedRows(r)
		got := slices.Collect(rows)
		if n != len(want) || len(got) != len(want) {
			t.Fatalf("trial %d: %d rows, count %d, want %d", trial, len(got), n, len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("trial %d: row %d differs", trial, i)
			}
		}
	}
}

func TestMarshalReadsSeriesParts(t *testing.T) {
	fds := []timeseries.FieldDefinition{
		{Name: "t", Role: timeseries.RoleTimestamp, SDataType: "DateTime64(3)", DataType: timeseries.DateTimeUnixMilli, OutputPosition: 0},
		{Name: "hostname", Role: timeseries.RoleTag, SDataType: "String", OutputPosition: 1},
		{Name: "v", Role: timeseries.RoleValue, SDataType: "Float64", OutputPosition: 2},
	}
	var series []*dataset.Series
	for i := range 3 {
		s := &dataset.Series{Header: dataset.SeriesHeader{Tags: dataset.Tags{"hostname": fmt.Sprint("h", i)}}}
		for j := range 5 {
			s.Points = append(s.Points, dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*j) * 1e9),
				Values: []any{float64(i*j) / 3}})
		}
		series = append(series, s)
	}
	view := parts.Of(jsonTestDataSet(timeseries.DateTimeUnixMilli, fds, series...), 60e9)
	if !view.HasParts() {
		t.Fatal("the view has no parts")
	}
	for of := byte(0); of <= OutputFormatNative; of++ {
		rlo := &timeseries.RequestOptions{OutputFormat: of}
		var got, want bytes.Buffer
		if err := MarshalTimeseriesWriter(view, rlo, 200, &got); err != nil {
			t.Fatal(of, err)
		}
		if err := MarshalTimeseriesWriter(view.Flat(), rlo, 200, &want); err != nil {
			t.Fatal(of, err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) || got.Len() == 0 {
			t.Fatalf("format %d:\n got %q\nwant %q", of, got.Bytes(), want.Bytes())
		}
	}
}
