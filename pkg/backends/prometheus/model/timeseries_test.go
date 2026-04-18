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
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func FuzzUnmarshalMarshalTimeseries(f *testing.F) {
	f.Add([]byte(testMatrix))
	f.Add([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","label":"val with \"quotes\" and \\backslash and \nnewline and \u00e9"},"values":[[1435781430,"1"]]}]}}`))
	f.Add([]byte(testHistogramMatrix))
	f.Add([]byte(testMixedMatrix))
	f.Add([]byte(testHistogramVector))
	f.Fuzz(func(t *testing.T, data []byte) {
		trq := &timeseries.TimeRangeQuery{}
		ts, err := UnmarshalTimeseries(data, trq)
		if err != nil {
			return
		}
		ds, ok := ts.(*dataset.DataSet)
		if !ok {
			return
		}
		b, err := MarshalTimeseries(ds, nil, 200)
		if err != nil {
			return
		}
		if !json.Valid(b) {
			t.Fatalf("MarshalTimeseries produced invalid JSON: %s", string(b))
		}
	})
}

func TestNewModeler(t *testing.T) {
	m := NewModeler()
	if m == nil {
		t.Error("expected non-nil modeler")
	} else if m.WireMarshaler == nil {
		t.Error("expected non-nil funcs")
	}
}

func TestUnmarshalTimeseriesReader(t *testing.T) {
	t.Run("nil TimeRangeQuery", func(t *testing.T) {
		ts, err := UnmarshalTimeseriesReader(nil, nil)
		if ts != nil {
			t.Error("expected nil timeseries")
		}
		if err != timeseries.ErrNoTimerangeQuery {
			t.Errorf("expected ErrNoTimerangeQuery, got %v", err)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		r := strings.NewReader("{sta")
		ts, err := UnmarshalTimeseriesReader(r, &timeseries.TimeRangeQuery{})
		if ts != nil {
			t.Error("expected nil timeseries")
		}
		if err == nil || !strings.Contains(err.Error(), "invalid character") {
			t.Errorf("expected JSON parse error, got %v", err)
		}
	})

	t.Run("valid matrix", func(t *testing.T) {
		r := strings.NewReader(testMatrix)
		result, err := UnmarshalTimeseriesReader(r, &timeseries.TimeRangeQuery{})
		if err != nil {
			t.Fatal(err)
		}
		ds, ok := result.(*dataset.DataSet)
		require.True(t, ok)
		require.Len(t, ds.Results, 1)
		require.Len(t, ds.Results[0].SeriesList, 2)
		require.Len(t, ds.Results[0].SeriesList[0].Points, 3)
		require.Equal(t, epoch.Epoch(1435781430000000000), ds.Results[0].SeriesList[0].Points[0].Epoch)
		require.Equal(t, epoch.Epoch(1435781445000000000), ds.Results[0].SeriesList[0].Points[1].Epoch)
		require.Equal(t, epoch.Epoch(1435781460000000000), ds.Results[0].SeriesList[0].Points[2].Epoch)
	})
}

func TestPointFromValues(t *testing.T) {
	tests := []struct {
		name   string
		values []any
		expP   epoch.Epoch
		expE   error
	}{
		{
			name:   "nil values",
			values: nil,
			expE:   timeseries.ErrInvalidBody,
		},
		{
			name:   "non-float first element",
			values: []any{"abc", 85},
			expE:   timeseries.ErrInvalidBody,
		},
		{
			name:   "non-string second element",
			values: []any{86.7, 85},
			expE:   timeseries.ErrInvalidBody,
		},
		{
			name:   "valid point",
			values: []any{1435781430.0, "1"},
			expP:   epoch.Epoch(1435781430000000000),
			expE:   nil,
		},
		{
			// Prometheus emits timestamps as `seconds.millis`, e.g.
			// `[1435781430.5, "1"]`. Casting the float to int64 *before*
			// multiplying by 1e9 drops the sub-second portion, silently
			// re-bucketing every point to the top of its second. Using an
			// exactly-representable binary fraction (0.5) so the assertion
			// is immune to float64 rounding noise.
			name:   "sub-second precision preserved",
			values: []any{1435781430.5, "1"},
			expP:   epoch.Epoch(1435781430500000000),
			expE:   nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p, err := pointFromValues(test.values)
			if p.Epoch != test.expP {
				t.Errorf("expected %v got %v", test.expP, p.Epoch)
			}
			if err != test.expE {
				t.Errorf("expected %v got %v", test.expE, err)
			}
		})
	}
}

func TestMarshalTSOrVectorWriter(t *testing.T) {
	t.Run("nil writer", func(t *testing.T) {
		err := MarshalTSOrVectorWriter(nil, nil, 0, nil, false)
		if err != errors.ErrNilWriter {
			t.Errorf("expected ErrNilWriter, got %v", err)
		}
	})

	t.Run("nil timeseries", func(t *testing.T) {
		w := httptest.NewRecorder()
		err := MarshalTSOrVectorWriter(nil, nil, 0, w, false)
		if err != timeseries.ErrUnknownFormat {
			t.Errorf("expected ErrUnknownFormat, got %v", err)
		}
	})

	t.Run("empty results", func(t *testing.T) {
		w := httptest.NewRecorder()
		err := MarshalTSOrVectorWriter(&dataset.DataSet{}, nil, 0, w, false)
		if err != timeseries.ErrUnknownFormat {
			t.Errorf("expected ErrUnknownFormat, got %v", err)
		}
	})

	t.Run("valid vector write", func(t *testing.T) {
		w := httptest.NewRecorder()
		var s1 *dataset.Series
		s2 := &dataset.Series{
			Points: []dataset.Point{
				{Epoch: 1234567980, Values: []any{"12345"}},
			},
		}
		err := MarshalTSOrVectorWriter(&dataset.DataSet{
			Results: []*dataset.Result{
				{SeriesList: []*dataset.Series{s1, s2}},
			},
		}, nil, 0, w, true)
		if err != nil {
			t.Error(err)
		}
	})

	t.Run("matrix write with sorted output", func(t *testing.T) {
		w := httptest.NewRecorder()
		s := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "up", "job": "test"},
			},
			Points: dataset.Points{
				{Epoch: 1435781460000000000, Values: []any{"3"}},
				{Epoch: 1435781430000000000, Values: []any{"1"}},
				{Epoch: 1435781445000000000, Values: []any{"2"}},
			},
		}
		err := MarshalTSOrVectorWriter(&dataset.DataSet{
			Status: "success",
			Results: []*dataset.Result{
				{SeriesList: []*dataset.Series{s}},
			},
		}, nil, 200, w, false)
		if err != nil {
			t.Fatal(err)
		}
		body := w.Body.String()
		if !strings.Contains(body, `"resultType":"matrix"`) {
			t.Errorf("expected matrix resultType: %s", body)
		}
		// Points must appear sorted by epoch
		if !strings.Contains(body, `[1435781430,"1"],[1435781445,"2"],[1435781460,"3"]`) {
			t.Errorf("expected sorted points: %s", body)
		}
	})

	t.Run("exact JSON output", func(t *testing.T) {
		w := httptest.NewRecorder()
		s := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "up"},
			},
			Points: dataset.Points{
				{Epoch: 1000000000000000000, Values: []any{"1"}},
				{Epoch: 2000000000000000000, Values: []any{"2"}},
			},
		}
		err := MarshalTSOrVectorWriter(&dataset.DataSet{
			Status: "success",
			Results: []*dataset.Result{
				{SeriesList: []*dataset.Series{s}},
			},
		}, nil, 200, w, false)
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up"},"values":[[1000000000,"1"],[2000000000,"2"]]}]}}`
		if body := w.Body.String(); body != expected {
			t.Errorf("exact JSON mismatch\nexpected: %s\n     got: %s", expected, body)
		}
	})

	t.Run("matrix multi-series", func(t *testing.T) {
		w := httptest.NewRecorder()
		s1 := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "up", "instance": "a"},
			},
			Points: dataset.Points{
				{Epoch: 1000000000000000000, Values: []any{"1"}},
			},
		}
		s2 := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "up", "instance": "b"},
			},
			Points: dataset.Points{
				{Epoch: 2000000000000000000, Values: []any{"2"}},
			},
		}
		err := MarshalTSOrVectorWriter(&dataset.DataSet{
			Status: "success",
			Results: []*dataset.Result{
				{SeriesList: []*dataset.Series{s1, s2}},
			},
		}, nil, 200, w, false)
		if err != nil {
			t.Fatal(err)
		}
		body := w.Body.String()
		// Both series should appear separated by comma
		if !strings.Contains(body, `"instance":"a"`) || !strings.Contains(body, `"instance":"b"`) {
			t.Errorf("expected both series in output: %s", body)
		}
		if !strings.Contains(body, `},{`) {
			t.Errorf("expected series separator in output: %s", body)
		}
	})

	t.Run("vector skips empty series", func(t *testing.T) {
		w := httptest.NewRecorder()
		empty := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "empty"},
			},
			Points: dataset.Points{},
		}
		withPoints := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "has_data"},
			},
			Points: dataset.Points{
				{Epoch: 1000000000000000000, Values: []any{"42"}},
			},
		}
		err := MarshalTSOrVectorWriter(&dataset.DataSet{
			Status: "success",
			Results: []*dataset.Result{
				{SeriesList: []*dataset.Series{empty, withPoints}},
			},
		}, nil, 200, w, true)
		if err != nil {
			t.Fatal(err)
		}
		body := w.Body.String()
		if strings.Contains(body, `"empty"`) {
			t.Errorf("empty series should be skipped: %s", body)
		}
		if !strings.Contains(body, `"has_data"`) {
			t.Errorf("non-empty series should be present: %s", body)
		}
	})

	t.Run("vector uses first point only", func(t *testing.T) {
		w := httptest.NewRecorder()
		s := &dataset.Series{
			Header: dataset.SeriesHeader{
				Tags: dataset.Tags{"__name__": "multi"},
			},
			Points: dataset.Points{
				{Epoch: 1000000000000000000, Values: []any{"first"}},
				{Epoch: 2000000000000000000, Values: []any{"second"}},
			},
		}
		err := MarshalTSOrVectorWriter(&dataset.DataSet{
			Status: "success",
			Results: []*dataset.Result{
				{SeriesList: []*dataset.Series{s}},
			},
		}, nil, 200, w, true)
		if err != nil {
			t.Fatal(err)
		}
		body := w.Body.String()
		if !strings.Contains(body, `"first"`) {
			t.Errorf("expected first point value: %s", body)
		}
		if strings.Contains(body, `"second"`) {
			t.Errorf("second point should not appear in vector output: %s", body)
		}
	})
}

func TestUnmarshalScalar(t *testing.T) {
	t.Run("valid scalar", func(t *testing.T) {
		const input = `{"status":"success","data":{"resultType":"scalar","result":[1435781430,"1"]}}`
		trq := &timeseries.TimeRangeQuery{}
		ts, err := UnmarshalTimeseries([]byte(input), trq)
		if err != nil {
			t.Fatal(err)
		}
		ds, ok := ts.(*dataset.DataSet)
		if !ok {
			t.Fatal("expected *dataset.DataSet")
		}
		require.Len(t, ds.Results, 1)
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Len(t, ds.Results[0].SeriesList[0].Points, 1)
		require.Equal(t, epoch.Epoch(1435781430000000000), ds.Results[0].SeriesList[0].Points[0].Epoch)
	})

	t.Run("malformed result not array", func(t *testing.T) {
		const input = `{"status":"success","data":{"resultType":"scalar","result":"not_array"}}`
		trq := &timeseries.TimeRangeQuery{}
		_, err := UnmarshalTimeseries([]byte(input), trq)
		if err == nil {
			t.Error("expected error for non-array scalar result")
		}
	})

	t.Run("empty result array", func(t *testing.T) {
		const input = `{"status":"success","data":{"resultType":"scalar","result":[]}}`
		trq := &timeseries.TimeRangeQuery{}
		ts, err := UnmarshalTimeseries([]byte(input), trq)
		if err != nil {
			t.Fatal(err)
		}
		ds, ok := ts.(*dataset.DataSet)
		if !ok {
			t.Fatal("expected *dataset.DataSet")
		}
		// Empty scalar → populateSeries with len(pr.Value)==0, isVector=true
		// → no points created
		require.Len(t, ds.Results, 1)
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Empty(t, ds.Results[0].SeriesList[0].Points)
	})

	t.Run("single element array", func(t *testing.T) {
		const input = `{"status":"success","data":{"resultType":"scalar","result":[1435781430]}}`
		trq := &timeseries.TimeRangeQuery{}
		ts, err := UnmarshalTimeseries([]byte(input), trq)
		if err != nil {
			t.Fatal(err)
		}
		ds, ok := ts.(*dataset.DataSet)
		if !ok {
			t.Fatal("expected *dataset.DataSet")
		}
		// Single element → len(pr.Value)==1 != 2, so no points
		require.Len(t, ds.Results, 1)
		require.Len(t, ds.Results[0].SeriesList, 1)
		require.Empty(t, ds.Results[0].SeriesList[0].Points)
	})
}

func TestPopulateSeriesMalformedPoints(t *testing.T) {
	// Matrix with mix of valid and malformed values.
	// populateSeries silently ignores pointFromValues errors;
	// malformed points get zero-epoch which remain in the slice.
	const input = `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{"__name__":"test"},"values":[` +
		`[1435781430,"1"],["bad","not_float"],[1435781460,"3"]` +
		`]}]}}`
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries([]byte(input), trq)
	if err != nil {
		t.Fatal(err)
	}
	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		t.Fatal("expected *dataset.DataSet")
	}
	require.Len(t, ds.Results, 1)
	require.Len(t, ds.Results[0].SeriesList, 1)
	pts := ds.Results[0].SeriesList[0].Points
	require.Len(t, pts, 2, "malformed points must be compacted out")
	require.Equal(t, epoch.Epoch(1435781430000000000), pts[0].Epoch)
	require.Equal(t, epoch.Epoch(1435781460000000000), pts[1].Epoch)

	b, err := MarshalTimeseries(ds, nil, 200)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(b, &env), "marshal of dataset with malformed points produced invalid JSON: %s", string(b))
}

const testHistogramMatrix = `{"status":"success","data":{"resultType":"matrix","result":[` +
	`{"metric":{"__name__":"test_histogram","job":"prom"},` +
	`"histograms":[[1435781430,{"count":"10","sum":"3.14","schema":3,` +
	`"zero_threshold":0.001,"zero_count":"2",` +
	`"positive_spans":[{"offset":0,"length":2}],"positive_deltas":[1,3],` +
	`"negative_spans":[{"offset":0,"length":1}],"negative_deltas":[4]}],` +
	`[1435781445,{"count":"20","sum":"6.28","schema":3,` +
	`"zero_threshold":0.001,"zero_count":"4",` +
	`"positive_spans":[{"offset":0,"length":2}],"positive_deltas":[2,6],` +
	`"negative_spans":[{"offset":0,"length":1}],"negative_deltas":[8]}]]}]}}`

const testMixedMatrix = `{"status":"success","data":{"resultType":"matrix","result":[` +
	`{"metric":{"__name__":"mixed","job":"prom"},` +
	`"values":[[1435781430,"1"],[1435781445,"2"]],` +
	`"histograms":[[1435781460,{"count":"10","sum":"3.14","schema":3,` +
	`"zero_threshold":0.001,"zero_count":"2",` +
	`"positive_spans":[{"offset":0,"length":1}],"positive_deltas":[1]}]]}]}}`

const testHistogramVector = `{"status":"success","data":{"resultType":"vector","result":[` +
	`{"metric":{"__name__":"test_histogram","job":"prom"},` +
	`"histogram":[1435781430,{"count":"10","sum":"3.14","schema":3,` +
	`"zero_threshold":0.001,"zero_count":"2",` +
	`"positive_spans":[{"offset":0,"length":2}],"positive_deltas":[1,3]}]}]}}`

func TestUnmarshalHistogramMatrix(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries([]byte(testHistogramMatrix), trq)
	require.NoError(t, err)
	ds, ok := ts.(*dataset.DataSet)
	require.True(t, ok)
	require.Len(t, ds.Results, 1)
	require.Len(t, ds.Results[0].SeriesList, 1)

	s := ds.Results[0].SeriesList[0]
	require.Equal(t, fieldNameHistogram, s.Header.ValueFieldsList[0].Name)
	require.Len(t, s.Points, 2)
	require.Equal(t, epoch.Epoch(1435781430000000000), s.Points[0].Epoch)
	require.Equal(t, epoch.Epoch(1435781445000000000), s.Points[1].Epoch)

	// Round-trip: marshal and verify valid JSON
	b, err := MarshalTimeseries(ds, nil, 200)
	require.NoError(t, err)
	require.True(t, json.Valid(b), "invalid JSON: %s", string(b))

	// Verify histograms key present
	require.Contains(t, string(b), `"histograms":`)
	require.NotContains(t, string(b), `"values":`)
}

func TestUnmarshalMixedMatrix(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries([]byte(testMixedMatrix), trq)
	require.NoError(t, err)
	ds, ok := ts.(*dataset.DataSet)
	require.True(t, ok)
	require.Len(t, ds.Results, 1)
	// Mixed series produces two internal series: one for values, one for histograms
	require.Len(t, ds.Results[0].SeriesList, 2)

	valueSer := ds.Results[0].SeriesList[0]
	histSer := ds.Results[0].SeriesList[1]
	require.Equal(t, "value", valueSer.Header.ValueFieldsList[0].Name)
	require.Equal(t, fieldNameHistogram, histSer.Header.ValueFieldsList[0].Name)
	require.Len(t, valueSer.Points, 2)
	require.Len(t, histSer.Points, 1)

	// Round-trip marshal
	b, err := MarshalTimeseries(ds, nil, 200)
	require.NoError(t, err)
	require.True(t, json.Valid(b), "invalid JSON: %s", string(b))

	// Both values and histograms should appear under the same metric
	require.Contains(t, string(b), `"values":`)
	require.Contains(t, string(b), `"histograms":`)
	// Only one result object (same metric tags grouped)
	require.Equal(t, 1, strings.Count(string(b), `"__name__":"mixed"`))
}

func TestUnmarshalHistogramVector(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries([]byte(testHistogramVector), trq)
	require.NoError(t, err)
	ds, ok := ts.(*dataset.DataSet)
	require.True(t, ok)
	require.Len(t, ds.Results, 1)
	require.Len(t, ds.Results[0].SeriesList, 1)

	s := ds.Results[0].SeriesList[0]
	require.Equal(t, fieldNameHistogram, s.Header.ValueFieldsList[0].Name)
	require.Len(t, s.Points, 1)

	// Vector marshal: call MarshalTSOrVectorWriter directly with isVector=true
	var buf strings.Builder
	require.NoError(t, MarshalTSOrVectorWriter(ds, nil, 200, &buf, true))
	body := buf.String()
	require.True(t, json.Valid([]byte(body)), "invalid JSON: %s", body)
	require.Contains(t, body, `"histogram":`)
	require.Contains(t, body, `"resultType":"vector"`)
	require.NotContains(t, body, `"value":`)
}

func TestHistogramRoundTrip(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{}
	ts, err := UnmarshalTimeseries([]byte(testHistogramMatrix), trq)
	require.NoError(t, err)
	ds := ts.(*dataset.DataSet)

	// Marshal → Unmarshal → Marshal should produce identical output
	b1, err := MarshalTimeseries(ds, nil, 200)
	require.NoError(t, err)

	ts2, err := UnmarshalTimeseries(b1, &timeseries.TimeRangeQuery{})
	require.NoError(t, err)
	ds2 := ts2.(*dataset.DataSet)

	b2, err := MarshalTimeseries(ds2, nil, 200)
	require.NoError(t, err)
	require.Equal(t, string(b1), string(b2))
}

func TestPointFromHistogram(t *testing.T) {
	t.Run("valid histogram", func(t *testing.T) {
		v := []any{1435781430.0, map[string]any{"count": "10", "sum": "3.14"}}
		p, err := pointFromHistogram(v)
		require.NoError(t, err)
		require.Equal(t, epoch.Epoch(1435781430000000000), p.Epoch)
		require.Len(t, p.Values, 1)
		require.Contains(t, p.Values[0].(string), `"count":"10"`)
	})

	t.Run("wrong length", func(t *testing.T) {
		_, err := pointFromHistogram([]any{1.0})
		require.Error(t, err)
	})

	t.Run("bad epoch type", func(t *testing.T) {
		_, err := pointFromHistogram([]any{"bad", map[string]any{}})
		require.Error(t, err)
	})
}
