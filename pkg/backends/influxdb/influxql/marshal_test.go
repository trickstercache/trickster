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

package influxql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	"github.com/trickstercache/trickster/v2/pkg/util/weak/weaktest"
)

func TestMarshalTimeseries(t *testing.T) {
	_, err := MarshalTimeseries(nil, nil, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Error("expected ErrUnknownFormat got", err)
	}

	trq := &timeseries.TimeRangeQuery{
		Statement: "hello",
	}

	ts, err := UnmarshalTimeseries([]byte(testDoc01), trq)
	if err != nil {
		t.Error(err)
	}

	rlo := &timeseries.RequestOptions{}
	w := httptest.NewRecorder()
	err = MarshalTimeseriesWriter(ts, rlo, 200, w)
	if err != nil {
		t.Error(err)
	}

	if !strings.HasPrefix(w.Header().Get(headers.NameContentType), headers.ValueApplicationJSON) {
		t.Error("expected JSON content type header; got", w.Header().Get(headers.NameContentType))
	}

	rlo.OutputFormat = 5

	_, err = MarshalTimeseries(ts, rlo, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Error("expected ErrUnknownFormat got", err)
	}

	rlo.OutputFormat = 1
	w = httptest.NewRecorder()
	err = MarshalTimeseriesWriter(ts, rlo, 200, w)
	if err != nil {
		t.Error(err)
	}
	if !strings.HasPrefix(w.Header().Get(headers.NameContentType), headers.ValueApplicationJSON) {
		t.Error("expected JSON content type header; got", w.Header().Get(headers.NameContentType))
	}
}

// the output encoding/json gives the wire format document, which the marshalers must match
func referenceMarshal(t *testing.T, ds *dataset.DataSet, rlo *timeseries.RequestOptions) []byte {
	t.Helper()
	wfdoc, err := legacyToWireFormat(ds, rlo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(wfdoc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func requireReferenceOutput(t *testing.T, name string, ds *dataset.DataSet) {
	t.Helper()
	for _, tf := range []byte{0, 1, 2, 3, 4, 5, 6, 7} {
		rlo := &timeseries.RequestOptions{TimeFormat: tf}
		want := referenceMarshal(t, ds, rlo)
		got, err := MarshalTimeseries(ds, rlo, 200)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s, time format %d:\n got %s, %v\nwant %s", name, tf, got, err, want)
		}
		var w bytes.Buffer
		if err := MarshalTimeseriesWriter(ds, rlo, 200, &w); err != nil ||
			!bytes.Equal(w.Bytes(), append(want, '\n')) {
			t.Fatalf("%s, time format %d: the writer wrote\n%s, %v\nwant %s", name, tf, w.Bytes(), err, want)
		}
	}
}

func marshalTestSeries(name string, tags dataset.Tags, at int, fields []string, points ...dataset.Point,
) *dataset.Series {
	s := dataset.NewSeries(dataset.SeriesHeader{Name: name, Tags: tags,
		TimestampField: timeseries.FieldDefinition{Name: "time", OutputPosition: at}}, points)
	for _, f := range fields {
		s.Header.ValueFieldsList = append(s.Header.ValueFieldsList, timeseries.FieldDefinition{Name: f})
	}
	return s
}

func TestMarshalMatchesEncodingJSON(t *testing.T) {
	at := func(sec int64, nanos int64, values ...any) dataset.Point {
		return dataset.Point{Epoch: epoch.Epoch(sec*1e9 + nanos), Values: values}
	}
	cases := map[string]*dataset.DataSet{
		"nil":        nil,
		"no results": {},
		"empty":      {Results: []*dataset.Result{{StatementID: 3}, {StatementID: 4, SeriesList: []*dataset.Series{nil}}}},
		"scalars": {Results: []*dataset.Result{{StatementID: 0, SeriesList: []*dataset.Series{
			marshalTestSeries("cpu", dataset.Tags{"host": "a", "dc": "<east>&\"1\""}, 0, []string{"f", "s", "b", "n"},
				at(1700000000, 0, 1.5, "x", true, nil), at(1700000060, 123456789, -0.0, "\u2028\xff", false, 1e21),
				at(1700000120, 1, 1e-7, "", true, float32(0.1)), dataset.Point{Epoch: 5}),
			marshalTestSeries("", nil, 1, []string{"a", "b"}, at(1, 500, int64(-7), 12), at(2, 0, int(3), uint64(9))),
			nil,
			marshalTestSeries("mem\n", dataset.Tags{}, 9, []string{"used"}, at(10, 0, 123456789.125)),
			marshalTestSeries("points without values", nil, 0, []string{"x"}, dataset.Point{Epoch: 1}),
		}}, {StatementID: 1}}},
		"other values": {Results: []*dataset.Result{{SeriesList: []*dataset.Series{
			marshalTestSeries("m", dataset.Tags{"k": "v"}, 2, []string{"a", "b", "c"},
				at(1, 0, json.Number("1.50"), []any{1.0, "a"}, map[string]any{"z": 1, "a": "<"}, int32(5)),
				at(-1, 0, uint(1), uint32(2), "tail")),
		}}}},
	}
	for name, ds := range cases {
		requireReferenceOutput(t, name, ds)
	}

	// random datasets, of every value type the decoders produce
	rng := weaktest.NewRand(3, 5)
	value := func() any {
		switch rng.IntN(7) {
		case 0:
			return nil
		case 1:
			return rng.NormFloat64() * math.Pow(10, float64(rng.IntN(50)-25))
		case 2:
			return int64(rng.Uint64())
		case 3:
			return rng.IntN(2) == 0
		case 4:
			return string([]byte{byte(rng.IntN(256)), '<', byte(rng.IntN(128))})
		case 5:
			return float64(rng.IntN(1000))
		default:
			return "value"
		}
	}
	for trial := range 100 {
		ds := &dataset.DataSet{}
		for r := range 1 + rng.IntN(3) {
			res := &dataset.Result{StatementID: r}
			for range rng.IntN(4) {
				width := 1 + rng.IntN(4)
				fields := make([]string, width)
				for i := range fields {
					fields[i] = fmt.Sprintf("f%d", i)
				}
				s := marshalTestSeries(fmt.Sprintf("s%d", trial), dataset.Tags{"t": fmt.Sprint(rng.IntN(9))},
					rng.IntN(width+2), fields)
				for p := range rng.IntN(20) {
					values := make([]any, width)
					for i := range values {
						values[i] = value()
					}
					s.SetPoints(append(s.Points(), at(int64(1700000000+60*p), int64(rng.IntN(1e9)), values...)))
				}
				res.SeriesList = append(res.SeriesList, s)
			}
			ds.Results = append(ds.Results, res)
		}
		requireReferenceOutput(t, fmt.Sprint("trial ", trial), ds)
	}
}

func TestMarshalWritesNothingForAValueJSONCannotHold(t *testing.T) {
	for _, v := range []any{math.NaN(), math.Inf(-1), float32(math.Inf(1)), func() {}} {
		ds := &dataset.DataSet{Results: []*dataset.Result{{SeriesList: []*dataset.Series{
			marshalTestSeries("m", nil, 0, []string{"v"}, dataset.Point{Epoch: 1, Values: []any{1.0}},
				dataset.Point{Epoch: 2, Values: []any{v}}),
		}}}}
		_, want := json.Marshal(v)
		var w bytes.Buffer
		if err := MarshalTimeseriesWriter(ds, nil, 200, &w); err == nil || err.Error() != want.Error() || w.Len() != 0 {
			t.Fatalf("%v: wrote %q, %v; want %v", v, w.Bytes(), err, want)
		}
		if b, err := MarshalTimeseries(ds, nil, 200); err == nil || b != nil {
			t.Fatalf("%v: marshaled %q, %v", v, b, err)
		}
	}
}

func TestMarshalIndented(t *testing.T) {
	ds := &dataset.DataSet{Results: []*dataset.Result{{SeriesList: []*dataset.Series{
		marshalTestSeries("m", dataset.Tags{"k": "v"}, 0, []string{"v"}, dataset.Point{Epoch: 1, Values: []any{1.0}}),
	}}}}
	rlo := &timeseries.RequestOptions{OutputFormat: 1}
	wfdoc, _ := legacyToWireFormat(ds, rlo)
	want, _ := json.MarshalIndent(wfdoc, "", "  ")
	got, err := MarshalTimeseries(ds, rlo, 200)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got %s, %v; want %s", got, err, want)
	}
	var w bytes.Buffer
	if err := MarshalTimeseriesWriter(ds, rlo, 200, &w); err != nil || !bytes.Equal(w.Bytes(), append(want, '\n')) {
		t.Fatalf("the writer wrote %s, %v", w.Bytes(), err)
	}
	if err := MarshalTimeseriesWriter(nil, rlo, 200, &w); err != timeseries.ErrUnknownFormat {
		t.Fatal(err)
	}
	// a value JSON can't hold fails the document before any of it is written
	nan := &dataset.DataSet{Results: []*dataset.Result{{SeriesList: []*dataset.Series{
		marshalTestSeries("m", nil, 0, []string{"v"}, dataset.Point{Epoch: 1, Values: []any{math.NaN()}}),
	}}}}
	w.Reset()
	if b, err := MarshalTimeseries(nan, rlo, 200); err == nil || b != nil {
		t.Fatalf("marshaled %s, %v", b, err)
	}
	if err := MarshalTimeseriesWriter(nan, rlo, 200, &w); err == nil || w.Len() != 0 {
		t.Fatalf("wrote %s, %v", w.Bytes(), err)
	}
}

func TestMarshalReadsSeriesParts(t *testing.T) {
	rng := weaktest.NewRand(40, 1)
	for trial := range 100 {
		ds := &dataset.DataSet{}
		r := &dataset.Result{}
		for range 1 + rng.IntN(3) {
			width := 1 + rng.IntN(3)
			fields := make([]string, width)
			for i := range fields {
				fields[i] = fmt.Sprint("f", i)
			}
			s := marshalTestSeries(fmt.Sprint("s", trial), dataset.Tags{"t": fmt.Sprint(rng.IntN(9))}, rng.IntN(width+1), fields)
			for p := range 1 + rng.IntN(10) {
				values := make([]any, width)
				for i := range values {
					values[i] = float64(rng.IntN(100))
				}
				s.SetPoints(append(s.Points(), dataset.Point{Epoch: epoch.Epoch(int64(1700000000+60*p) * 1e9), Values: values}))
			}
			r.SeriesList = append(r.SeriesList, s)
		}
		ds.Results = append(ds.Results, r)
		view := parts.Of(ds, 60e9)
		if !view.HasParts() {
			t.Fatal("the view has no parts")
		}
		for _, rlo := range []*timeseries.RequestOptions{nil, {TimeFormat: 3}, {OutputFormat: 1}} {
			var got, want bytes.Buffer
			if err := MarshalTimeseriesWriter(view, rlo, 200, &got); err != nil {
				t.Fatal(err)
			}
			if err := MarshalTimeseriesWriter(view.Flat(), rlo, 200, &want); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("trial %d:\n got %s\nwant %s", trial, got.Bytes(), want.Bytes())
			}
		}
	}
}
