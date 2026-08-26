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
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/tinylib/msgp/msgp"
)

// captured from graphite-web 1.1.10 with now=1787350000
const (
	sampleJSON = `[{"target": "dev.fast.cpu.host01.percent", "tags": {"name": "dev.fast.cpu.host01.percent"}, "datapoints": [[27.082, 1787349970], [31.115, 1787349980], [33.164, 1787349990], [32.952, 1787350000]]}, {"target": "dev.coarse.users.active", "tags": {"name": "dev.coarse.users.active"}, "datapoints": [[10819.906, 1787350200]]}]`
	sampleRaw  = "dev.fast.cpu.host01.percent,1787349970,1787350010,10|27.082,31.115,33.164,32.952\ndev.coarse.users.active,1787350200,1787350500,300|10819.906\n"
	sampleCSV  = "dev.fast.cpu.host01.percent,2026-08-21 22:06:10,27.082\r\ndev.fast.cpu.host01.percent,2026-08-21 22:06:20,31.115\r\ndev.fast.cpu.host01.percent,2026-08-21 22:06:30,33.164\r\ndev.fast.cpu.host01.percent,2026-08-21 22:06:40,32.952\r\ndev.coarse.users.active,2026-08-21 22:10:00,10819.906\r\n"
	// from=-60s maxDataPoints=2: six 10s points consolidated by 3 with the
	// start nudged to ...960 and (the quirk) no value dropped
	sample60s     = `[{"target": "dev.fast.cpu.host01.percent", "tags": {"name": "dev.fast.cpu.host01.percent"}, "datapoints": [[27.082, 1787349950], [28.5, 1787349960], [29.567, 1787349970], [31.115, 1787349980], [33.164, 1787349990], [32.952, 1787350000]]}]`
	sample60sMDP2 = `[{"target": "dev.fast.cpu.host01.percent", "tags": {"name": "dev.fast.cpu.host01.percent"}, "datapoints": [[28.383, 1787349960], [32.410333333333334, 1787349990]]}]`
	sampleNulls   = `[{"target": "dev.fast.cpu.host01.percent", "tags": {"name": "dev.fast.cpu.host01.percent"}, "datapoints": [[null, 1786996410], [null, 1786996420], [1.5, 1786996430]]}]`
)

func trq(step time.Duration) *timeseries.TimeRangeQuery {
	return &timeseries.TimeRangeQuery{Statement: "x", Step: step,
		Extent: timeseries.Extent{Start: time.Unix(1787349960, 0), End: time.Unix(1787350000, 0)}}
}

func mustUnmarshal(t *testing.T, body string, step time.Duration) *dataset.DataSet {
	t.Helper()
	ts, err := UnmarshalTimeseries([]byte(body), trq(step))
	if err != nil {
		t.Fatal(err)
	}
	return ts.(*dataset.DataSet)
}

func render(t *testing.T, ds *dataset.DataSet, ro RenderOptions) string {
	t.Helper()
	b, err := MarshalTimeseries(ds, &timeseries.RequestOptions{ProviderRequest: ro}, 200)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUnmarshalJSON(t *testing.T) {
	ds := mustUnmarshal(t, sampleJSON, 0)
	if len(ds.Results) != 1 || len(ds.Results[0].SeriesList) != 2 {
		t.Fatalf("unexpected shape %+v", ds.Results)
	}
	s := ds.Results[0].SeriesList[0]
	if s.Header.Name != "dev.fast.cpu.host01.percent" || s.Header.Tags["name"] != s.Header.Name ||
		len(s.Points) != 4 || s.Points[0].Values[0] != 27.082 || s.Points[0].Epoch != 1787349970*1e9 {
		t.Errorf("unexpected series %+v", s)
	}
	if ds.TimeRangeQuery.Step != 10*time.Second {
		t.Errorf("the step must be adopted from the timestamps when unknown: %v", ds.TimeRangeQuery.Step)
	}
	if ds.Step() != 10*time.Second || len(ds.ExtentList) != 1 {
		t.Error("dataset step/extent")
	}
	// nulls round-trip as nil values, not zeros, and count as points
	ds = mustUnmarshal(t, sampleNulls, 10*time.Second)
	p := ds.Results[0].SeriesList[0].Points
	if len(p) != 3 || p[0].Values[0] != nil || p[2].Values[0] != 1.5 {
		t.Errorf("null handling: %+v", p)
	}
	// a predicted step that disagrees with the timestamps is an error
	_, err := UnmarshalTimeseries([]byte(sampleJSON), trq(time.Minute))
	var mismatch *StepMismatchError
	if !errors.As(err, &mismatch) || !errors.Is(err, ErrStepMismatch) || mismatch.Predicted != time.Minute ||
		mismatch.Observed != 10*time.Second || !strings.Contains(err.Error(), "predicted 1m0s") {
		t.Errorf("expected a step mismatch, got %v", err)
	}
	_, err = UnmarshalTimeseries([]byte(`[{"target":"a","tags":{"name":"a"},"datapoints":[[1, 100]]}]`), trq(time.Minute))
	if !errors.Is(err, ErrStepAmbiguous) {
		t.Errorf("expected ErrStepAmbiguous for a one-point predicted fetch, got %v", err)
	}
	if _, err := UnmarshalTimeseries([]byte(`[]`), trq(time.Minute)); !errors.Is(err, ErrStepAmbiguous) {
		t.Errorf("expected ErrStepAmbiguous for an empty predicted fetch, got %v", err)
	}
	if _, err := UnmarshalTimeseries([]byte(`[{"target":"a","tags":{"name":"a"},"datapoints":[[1, 100]]}]`), trq(0)); err != nil {
		t.Error(err)
	}
	// empty and invalid bodies
	ds = mustUnmarshal(t, "", 0)
	if len(ds.Results[0].SeriesList) != 0 {
		t.Error("empty body must yield no series")
	}
	ds = mustUnmarshal(t, "[]", 0)
	if len(ds.Results[0].SeriesList) != 0 {
		t.Error("empty array must yield no series")
	}
	for _, bad := range []string{"[{", `[{"target":"a","datapoints":[[1, null]]}]`,
		`[{"target":"a","datapoints":[[1, 200],[2, 100]]}]`, `[{"target":"a","datapoints":[[1, 100],[2, 100]]}]`} {
		if _, err := UnmarshalTimeseries([]byte(bad), trq(0)); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
	}
	if _, err := UnmarshalTimeseries([]byte(sampleJSON), nil); !errors.Is(err, timeseries.ErrNoTimerangeQuery) {
		t.Error("nil trq")
	}
	// missing tags default to name
	ds = mustUnmarshal(t, `[{"target":"a.b","datapoints":[[1, 100]]}]`, 0)
	if ds.Results[0].SeriesList[0].Header.Tags["name"] != "a.b" {
		t.Error("default tags")
	}
}

func TestUnmarshalRaw(t *testing.T) {
	ds := mustUnmarshal(t, sampleRaw, 0)
	if len(ds.Results[0].SeriesList) != 2 || len(ds.Results[0].SeriesList[0].Points) != 4 ||
		ds.Results[0].SeriesList[1].Points[0].Epoch != 1787350200*1e9 {
		t.Fatalf("unexpected raw parse %+v", ds.Results[0].SeriesList)
	}
	// raw and json parses render identically
	if got := render(t, ds, RenderOptions{Format: FormatJSON}); got != sampleJSON {
		t.Errorf("raw->json:\n%s\n%s", got, sampleJSON)
	}
	ds = mustUnmarshal(t, "a.b,100,130,10|None,1.5,None\n", 0)
	if p := ds.Results[0].SeriesList[0].Points; len(p) != 3 || p[0].Values[0] != nil || p[1].Values[0] != 1.5 {
		t.Errorf("raw nulls %+v", p)
	}
	ds = mustUnmarshal(t, "a,b,c,100,110,10|\n", 0)
	if s := ds.Results[0].SeriesList[0]; s.Header.Name != "a,b,c" || len(s.Points) != 0 {
		t.Errorf("raw comma name / empty values: %+v", s)
	}
	for _, bad := range []string{"nopipe", "a,1,2|1", "a,x,2,3|1", "a,1,x,3|1", "a,1,2,x|1", "a,1,2,0|1", "a,1,2,10|x"} {
		if _, err := UnmarshalTimeseries([]byte(bad), trq(0)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	if _, err := UnmarshalTimeseries([]byte("a,1,2,10|1,2"), trq(time.Minute)); !errors.Is(err, ErrStepMismatch) {
		t.Error("raw step mismatch")
	}
}

func TestMarshalFormats(t *testing.T) {
	ds := mustUnmarshal(t, sampleJSON, 0)
	if got := render(t, ds, RenderOptions{}); got != sampleJSON {
		t.Errorf("json:\n%s\n%s", got, sampleJSON)
	}
	// a single-point series carries no step in JSON and takes the query's
	// (10s here): in the real flow each target has its own query and step
	if got := render(t, ds, RenderOptions{Format: FormatRaw}); got != strings.Replace(sampleRaw, "1787350500,300|", "1787350210,10|", 1) {
		t.Errorf("raw:\n%q", got)
	}
	if got := render(t, mustUnmarshal(t, sampleRaw, 0), RenderOptions{Format: FormatRaw}); got != strings.Replace(sampleRaw, "1787350500,300|", "1787350210,10|", 1) {
		t.Errorf("raw from raw:\n%q", got)
	}
	if got := render(t, ds, RenderOptions{Format: FormatCSV}); got != sampleCSV {
		t.Errorf("csv:\n%q\n%q", got, sampleCSV)
	}
	ny, _ := time.LoadLocation("America/New_York")
	if got := render(t, ds, RenderOptions{Format: FormatCSV, Location: ny}); !strings.HasPrefix(got, "dev.fast.cpu.host01.percent,2026-08-21 18:06:10,27.082\r\n") {
		t.Errorf("csv tz: %q", got)
	}
	// jsonp and pretty
	if got := render(t, ds, RenderOptions{JSONP: "cb"}); got != "cb("+sampleJSON+")" {
		t.Errorf("jsonp: %s", got)
	}
	pretty := render(t, ds, RenderOptions{Pretty: true})
	if !strings.HasPrefix(pretty, "[\n  {\n    \"target\": \"dev.fast.cpu.host01.percent\",\n    \"tags\": {\n      \"name\": \"dev.fast.cpu.host01.percent\"\n    },\n    \"datapoints\": [\n      [\n        27.082,\n        1787349970\n      ],") {
		t.Errorf("pretty:\n%s", pretty)
	}
	// nulls in every format
	ds = mustUnmarshal(t, sampleNulls, 0)
	if got := render(t, ds, RenderOptions{}); got != sampleNulls {
		t.Errorf("json nulls: %s", got)
	}
	if got := render(t, ds, RenderOptions{Format: FormatRaw}); got != "dev.fast.cpu.host01.percent,1786996410,1786996440,10|None,None,1.5\n" {
		t.Errorf("raw nulls: %q", got)
	}
	if got := render(t, ds, RenderOptions{Format: FormatCSV}); !strings.HasPrefix(got, "dev.fast.cpu.host01.percent,2026-08-17 19:53:30,\r\n") {
		t.Errorf("csv nulls: %q", got)
	}
	if got := render(t, ds, RenderOptions{NoNullPoints: true}); got != `[{"target": "dev.fast.cpu.host01.percent", "tags": {"name": "dev.fast.cpu.host01.percent"}, "datapoints": [[1.5, 1786996430]]}]` {
		t.Errorf("noNullPoints: %s", got)
	}
	// a series left with no points by noNullPoints is dropped entirely
	ds = mustUnmarshal(t, `[{"target":"a","tags":{"name":"a"},"datapoints":[[null, 100]]}]`, 0)
	if got := render(t, ds, RenderOptions{NoNullPoints: true}); got != "[]" {
		t.Errorf("noNullPoints empty: %s", got)
	}
	// msgpack: decode and compare structure
	ds = mustUnmarshal(t, sampleJSON, 0)
	b := []byte(render(t, ds, RenderOptions{Format: FormatMsgPack, XFilesFactor: 0.5, PathExpressions: []string{"dev.fast.cpu.*.percent"}}))
	n, b, err := msgp.ReadArrayHeaderBytes(b)
	if err != nil || n != 2 {
		t.Fatalf("msgpack array: %v %d", err, n)
	}
	m, b, err := msgp.ReadMapHeaderBytes(b)
	if err != nil || m != 9 {
		t.Fatalf("msgpack map: %v %d", err, m)
	}
	var key, name string
	key, b, _ = msgp.ReadStringBytes(b)
	name, b, _ = msgp.ReadStringBytes(b)
	if key != "name" || name != "dev.fast.cpu.host01.percent" {
		t.Errorf("msgpack name: %s %s", key, name)
	}
	got := map[string]any{}
	for range 8 {
		key, b, _ = msgp.ReadStringBytes(b)
		var v any
		v, b, err = msgp.ReadIntfBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		got[key] = v
	}
	// ints are encoded in the smallest unsigned form, as Python's msgpack
	// does, so the decoder yields a mix of uint64 and (fixint) int64
	if fmt.Sprint(got["start"]) != "1787349970" || fmt.Sprint(got["end"]) != "1787350010" ||
		fmt.Sprint(got["step"]) != "10" || got["pathExpression"] != "dev.fast.cpu.*.percent" ||
		fmt.Sprint(got["valuesPerPoint"]) != "1" || got["consolidationFunc"] != "average" ||
		got["xFilesFactor"] != 0.5 {
		t.Errorf("msgpack fields: %+v", got)
	}
	if vals, ok := got["values"].([]any); !ok || len(vals) != 4 || vals[0] != 27.082 {
		t.Errorf("msgpack values: %+v", got["values"])
	}
	// unknown format, wrong type
	if _, err := MarshalTimeseries(ds, &timeseries.RequestOptions{ProviderRequest: RenderOptions{Format: "png"}}, 200); !errors.Is(err, timeseries.ErrUnknownFormat) {
		t.Error("unknown format")
	}
	if err := MarshalTimeseriesWriter(nil, nil, 200, &bytes.Buffer{}); !errors.Is(err, timeseries.ErrUnknownFormat) {
		t.Error("nil timeseries")
	}
	// http response writer gets content type and status; RenderOptions by
	// pointer and nil RequestOptions also work
	w := httptest.NewRecorder()
	if err := MarshalTimeseriesWriter(ds, &timeseries.RequestOptions{ProviderRequest: &RenderOptions{Format: FormatCSV}}, 201, w); err != nil {
		t.Fatal(err)
	}
	if w.Code != 201 || w.Header().Get("Content-Type") != "text/csv" || w.Body.String() != sampleCSV {
		t.Errorf("writer: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	_ = MarshalTimeseriesWriter(ds, nil, 0, w)
	if w.Header().Get("Content-Type") != "application/json" || w.Body.String() != sampleJSON {
		t.Error("default json")
	}
	w = httptest.NewRecorder()
	_ = MarshalTimeseriesWriter(ds, &timeseries.RequestOptions{ProviderRequest: RenderOptions{JSONP: "x"}}, 200, w)
	if w.Header().Get("Content-Type") != "text/javascript" {
		t.Error("jsonp content type")
	}
	var nilRO *RenderOptions
	if ro := renderOptions(&timeseries.RequestOptions{ProviderRequest: nilRO}); ro.Format != "" {
		t.Error("nil pointer render options")
	}
}

func TestConsolidation(t *testing.T) {
	ds := mustUnmarshal(t, sample60s, 0)
	// graphite-web's own answer for maxDataPoints=2 (nudge, no value lost)
	if got := render(t, ds, RenderOptions{MaxDataPoints: 2}); got != sample60sMDP2 {
		t.Errorf("maxDataPoints=2:\n%s\n%s", got, sample60sMDP2)
	}
	// enough points: no consolidation
	if got := render(t, ds, RenderOptions{MaxDataPoints: 6}); got != sample60s {
		t.Errorf("maxDataPoints=6 must not consolidate: %s", got)
	}
	// maxDataPoints=1: everything into one point at the start, no nudge
	if got := render(t, ds, RenderOptions{MaxDataPoints: 1}); got != `[{"target": "dev.fast.cpu.host01.percent", "tags": {"name": "dev.fast.cpu.host01.percent"}, "datapoints": [[30.396666666666665, 1787349950]]}]` {
		t.Errorf("maxDataPoints=1: %s", got)
	}
	// consolidateBy tag changes the function; nulls honor xFilesFactor
	ds = mustUnmarshal(t, `[{"target": "c", "tags": {"name": "c", "consolidateBy": "max"}, "datapoints": [[1, 100], [null, 110], [3, 120], [null, 130], [null, 140], [null, 150]]}]`, 0)
	if got := render(t, ds, RenderOptions{MaxDataPoints: 2}); got != `[{"target": "c", "tags": {"name": "c", "consolidateBy": "max"}, "datapoints": [[3.0, 120], [null, 150]]}]` {
		t.Errorf("consolidateBy max: %s", got)
	}
	if got := render(t, ds, RenderOptions{MaxDataPoints: 2, XFilesFactor: 0.9}); !strings.Contains(got, `[[null, 120], [null, 150]]`) {
		t.Errorf("xFilesFactor: %s", got)
	}
	// every consolidation function (nulls carried as an explicit bitmap)
	mkSeries := func(vals []float64, okv []bool, perPoint int) *series {
		sr := &series{values: vals, valid: make([]uint64, (len(vals)+63)/64), valuesPerPoint: perPoint}
		for i, ok := range okv {
			if ok {
				sr.valid[i>>6] |= 1 << (uint(i) & 63)
			}
		}
		return sr
	}
	bit := func(b []uint64, i int) bool { return b[i>>6]&(1<<(uint(i)&63)) != 0 }
	vals := []float64{1, 4, 0, 2}
	okv := []bool{true, true, false, true}
	for cf, want := range map[string]float64{"sum": 7, "average": 7.0 / 3, "avg": 7.0 / 3, "max": 4, "min": 1,
		"first": 1, "last": 2, "avg_zero": 7.0 / 4} {
		out, ok := mkSeries(vals, okv, 4).consolidate(cf, 0)
		if len(out) != 1 || !bit(ok, 0) || math.Abs(out[0]-want) > 1e-12 {
			t.Errorf("%s: got %v %v want %v", cf, out, ok, want)
		}
	}
	if out, ok := mkSeries([]float64{0, 0}, []bool{false, false}, 2).consolidate("sum", 0); len(out) != 1 || bit(ok, 0) {
		t.Error("all-null bucket must be null")
	}
	if out, ok := mkSeries(vals, okv, 3).consolidate("sum", 0); len(out) != 2 || !bit(ok, 1) || out[1] != 2 {
		t.Errorf("tail bucket: %v %v", out, ok)
	}
	// a real NaN is a value, not a null: it participates and propagates
	if out, ok := mkSeries([]float64{1, math.NaN()}, []bool{true, true}, 2).consolidate("sum", 0); len(out) != 1 ||
		!bit(ok, 0) || !math.IsNaN(out[0]) {
		t.Errorf("NaN must propagate through consolidation: %v %v", out, ok)
	}
	if pyMod(-7, 3) != 2 || pyMod(7, 3) != 1 || pyMod(6, 3) != 0 {
		t.Error("pyMod")
	}
	// a series with no points and a step-less query renders safely
	empty := &dataset.DataSet{TimeRangeQuery: &timeseries.TimeRangeQuery{}, Results: []*dataset.Result{{SeriesList: []*dataset.Series{{Header: dataset.SeriesHeader{Name: "e"}}}}, nil}}
	if got := render(t, empty, RenderOptions{MaxDataPoints: 2}); got != `[{"target": "e", "tags": {}, "datapoints": []}]` {
		t.Errorf("empty series: %s", got)
	}
	if got := render(t, empty, RenderOptions{Format: FormatRaw}); got != "e,0,0,0|\n" {
		t.Errorf("empty raw: %q", got)
	}
}

func TestPyFloat(t *testing.T) {
	for in, want := range map[float64]string{
		27.082: "27.082", 32.410333333333334: "32.410333333333334", 33: "33.0", 0: "0.0",
		3746862590477: "3746862590477.0", 3.746862590477e+17: "3.746862590477e+17", 1e16: "1e+16",
		1e15: "1000000000000000.0", 3.3164e-06: "3.3164e-06", 0.0001: "0.0001", 0.00001: "1e-05",
		-1.5: "-1.5", 123456789.123: "123456789.123", 1.5e300: "1.5e+300",
		math.Inf(1): "inf", math.Inf(-1): "-inf", 100: "100.0", 0.5: "0.5",
	} {
		if got := PyFloat(in); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
	if PyFloat(math.NaN()) != "nan" || PyFloat(math.Copysign(0, -1)) != "-0.0" {
		t.Error("nan / negative zero")
	}
}

func TestPyJSONString(t *testing.T) {
	for in, want := range map[string]string{
		"plain": `"plain"`, `q"uote`: `"q\"uote"`, `back\slash`: `"back\\slash"`,
		"nl\ntab\tcr\rbs\bff\f": `"nl\ntab\tcr\rbs\bff\f"`, "\x01\x7f": `"\u0001\u007f"`,
		"<&>": `"<&>"`, "\u00e9": `"\u00e9"`, "\u65e5\u672c": `"\u65e5\u672c"`, "\U0001F600": `"\ud83d\ude00"`,
	} {
		var b bytes.Buffer
		writePyJSONString(&b, in)
		if b.String() != want {
			t.Errorf("%q: got %s want %s", in, b.String(), want)
		}
	}
	// tags: name first, then sorted
	var b bytes.Buffer
	writeTags(&b, dataset.Tags{"zeta": "1", "name": "n", "alpha": "2"})
	if b.String() != `{"name": "n", "alpha": "2", "zeta": "1"}` {
		t.Errorf("tags: %s", b.String())
	}
	b.Reset()
	writeTags(&b, nil)
	if b.String() != "{}" {
		t.Error("empty tags")
	}
	// infinities in JSON
	ds := &dataset.DataSet{TimeRangeQuery: trq(10 * time.Second), Results: []*dataset.Result{{SeriesList: []*dataset.Series{{
		Header: dataset.SeriesHeader{Name: "i", Tags: dataset.Tags{"name": "i"}},
		Points: dataset.Points{newPoint(100e9, new(math.Inf(1))), newPoint(110e9, new(math.Inf(-1))), newPoint(120e9, new(math.NaN()))},
	}}}}}
	if got := render(t, ds, RenderOptions{}); got != `[{"target": "i", "tags": {"name": "i"}, "datapoints": [[1e9999, 100], [-Infinity, 110], [null, 120]]}]` {
		t.Errorf("infinities: %s", got)
	}
	if NewModeler().WireUnmarshaler == nil || NewModeler().CacheMarshaler == nil {
		t.Error("modeler")
	}
}

func TestNaNVersusNull(t *testing.T) {
	// one null, one NaN, one ordinary value
	ds := mustUnmarshal(t, "a.b,100,130,10|None,nan,1.5\n", 0)
	pts := ds.Results[0].SeriesList[0].Points
	if pts[0].Values[0] != nil {
		t.Fatal("None must decode as null")
	}
	if f, ok := pts[1].Values[0].(float64); !ok || !math.IsNaN(f) {
		t.Fatalf("nan must decode as a real NaN, got %v", pts[1].Values[0])
	}

	// raw round-trips both states
	if got := render(t, ds, RenderOptions{Format: FormatRaw}); got != "a.b,100,130,10|None,nan,1.5\n" {
		t.Errorf("raw: %q", got)
	}
	// CSV: empty field for null, nan for NaN
	csv := render(t, ds, RenderOptions{Format: FormatCSV})
	if !strings.Contains(csv, "1970-01-01 00:01:40,\r\n") || !strings.Contains(csv, "1970-01-01 00:01:50,nan\r\n") {
		t.Errorf("csv: %q", csv)
	}
	// msgpack: nil for null, a float64 NaN for NaN
	mp := render(t, ds, RenderOptions{Format: FormatMsgPack})
	rd := msgp.NewReader(strings.NewReader(mp))
	sz, err := rd.ReadArrayHeader()
	if err != nil || sz != 1 {
		t.Fatalf("msgpack outer: %v %d", err, sz)
	}
	msz, _ := rd.ReadMapHeader()
	var sawNil, sawNaN, sawVal bool
	for range msz {
		k, _ := rd.ReadString()
		if k != "values" {
			if err := rd.Skip(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		vsz, _ := rd.ReadArrayHeader()
		for range vsz {
			typ, _ := rd.NextType()
			if typ == msgp.NilType {
				_ = rd.ReadNil()
				sawNil = true
				continue
			}
			f, err := rd.ReadFloat64()
			if err != nil {
				t.Fatal(err)
			}
			if math.IsNaN(f) {
				sawNaN = true
			} else if f == 1.5 {
				sawVal = true
			}
		}
	}
	if !sawNil || !sawNaN || !sawVal {
		t.Errorf("msgpack values: nil=%t nan=%t val=%t", sawNil, sawNaN, sawVal)
	}
	// JSON renders both null and NaN as null, as graphite-web does
	js := render(t, ds, RenderOptions{Format: FormatJSON})
	if !strings.Contains(js, "[[null, 100], [null, 110], [1.5, 120]]") {
		t.Errorf("json: %q", js)
	}
}

type ambiguityRecorder struct {
	series string
}

func (a *ambiguityRecorder) NoteAmbiguousStep(name string, _ time.Duration) { a.series = name }

func TestNonUniformTimestampsRejected(t *testing.T) {
	for _, bad := range []string{
		`[{"target":"a","datapoints":[[1, 100], [2, 110], [3, 125]]}]`,           // gap after the first interval
		`[{"target":"a","datapoints":[[1, 100], [2, 110], [3, 110]]}]`,           // duplicate
		`[{"target":"a","datapoints":[[1, 100], [2, 110], [3, 105]]}]`,           // reversal
		`[{"target":"a","datapoints":[[1, 100], [2, 110], [3, 120], [4, 140]]}]`, // late step change
	} {
		// under a prediction the ambiguity noter must fire, so the handler
		// invalidates the stale leaf binding instead of retrying forever
		q := trq(10 * time.Second)
		noter := &ambiguityRecorder{}
		q.ParsedQuery = noter
		if _, err := UnmarshalTimeseries([]byte(bad), q); err == nil {
			t.Errorf("%s: expected an error", bad)
		}
		if noter.series == "" {
			t.Errorf("%s: expected the ambiguity noter to fire", bad)
		}
		// with no prediction either: the malformation is the same
		if _, err := UnmarshalTimeseries([]byte(bad), trq(0)); err == nil {
			t.Errorf("%s (no prediction): expected an error", bad)
		}
	}
	// a first-interval contradiction still reports a step mismatch
	q := trq(time.Minute)
	if _, err := UnmarshalTimeseries([]byte(`[{"target":"a","datapoints":[[1, 100], [2, 110], [3, 120]]}]`), q); !errors.Is(err, ErrStepMismatch) {
		t.Errorf("expected a step mismatch, got %v", err)
	}
}
