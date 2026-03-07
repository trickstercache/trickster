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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

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
	// All 3 slots exist; middle one has zero epoch from failed parse
	require.Len(t, pts, 3)
	require.Equal(t, epoch.Epoch(1435781430000000000), pts[0].Epoch)
	require.Equal(t, epoch.Epoch(0), pts[1].Epoch, "malformed point should have zero epoch")
	require.Equal(t, epoch.Epoch(1435781460000000000), pts[2].Epoch)
}
