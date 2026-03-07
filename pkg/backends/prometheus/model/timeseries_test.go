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
}

func TestUnmarshalScalar(t *testing.T) {
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
}
