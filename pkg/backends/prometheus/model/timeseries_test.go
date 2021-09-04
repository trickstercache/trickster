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
	"net/http/httptest"
	"strconv"
	"testing"

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
	ts, err := UnmarshalTimeseriesReader(nil, nil)
	if ts != nil {
		t.Error("expedted nil timeseries")
	}
	if err != timeseries.ErrNoTimerangeQuery {
		t.Error(err)
	}

	r := bytes.NewReader([]byte("{sta"))
	ts, err = UnmarshalTimeseriesReader(r, &timeseries.TimeRangeQuery{})
	if ts != nil {
		t.Error("expedted nil timeseries")
	}
	const expected = "invalid character 's' looking for beginning of object key string"
	if err == nil || err.Error() != expected {
		t.Error("expected error for invalid character, got", err)
	}

	r = bytes.NewReader([]byte(testMatrix))
	_, err = UnmarshalTimeseriesReader(r, &timeseries.TimeRangeQuery{})
	if err != nil {
		t.Error(err)
	}

}

func TestPointFromValues(t *testing.T) {

	tests := []struct {
		values []interface{}
		expP   epoch.Epoch
		expE   error
	}{
		{
			values: nil,
			expP:   0,
			expE:   timeseries.ErrInvalidBody,
		},
		{
			values: []interface{}{"abc", 85},
			expP:   0,
			expE:   timeseries.ErrInvalidBody,
		},
		{
			values: []interface{}{86.7, 85},
			expP:   0,
			expE:   timeseries.ErrInvalidBody,
		},
	}
	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			p, err := pointFromValues(test.values)
			if p.Epoch != test.expP {
				t.Errorf("expected %v got %v", test.expP, p.Epoch)
			}
			if err != test.expE {
				t.Errorf("expected %s got %s", test.expE, err)
			}
		})
	}
}

func TestMarshalTSOrVectorWriter(t *testing.T) {

	w := httptest.NewRecorder()

	err := MarshalTSOrVectorWriter(nil, nil, 0, nil, false)
	if err != errors.ErrNilWriter {
		t.Errorf("expected error for nil writer, got %v", err)
	}

	err = MarshalTSOrVectorWriter(nil, nil, 0, w, false)
	if err != timeseries.ErrUnknownFormat {
		t.Errorf("expected error for Unknown Format, got %v", err)
	}

	err = MarshalTSOrVectorWriter(&dataset.DataSet{}, nil, 0, w, false)
	if err != timeseries.ErrUnknownFormat {
		t.Errorf("expected error for Unknown Format, got %v", err)
	}

	var s1 *dataset.Series
	s2 := &dataset.Series{
		Points: []dataset.Point{
			{
				Epoch:  1234567980,
				Values: []interface{}{"12345"},
			},
		},
	}

	err = MarshalTSOrVectorWriter(&dataset.DataSet{
		Results: []*dataset.Result{
			{
				SeriesList: []*dataset.Series{s1, s2},
			},
		},
	}, nil, 0, w, true)
	if err != nil {
		t.Error(err)
	}

}
