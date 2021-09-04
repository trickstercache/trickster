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
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
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
		t.Error("expected JSON content type header")
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
		t.Error("expected JSON content type header")
	}

	rlo.OutputFormat = 2
	w = httptest.NewRecorder()
	err = MarshalTimeseriesWriter(ts, rlo, 200, w)
	if err != nil {
		t.Error(err)
	}
	if !strings.HasPrefix(w.Header().Get(headers.NameContentType), headers.ValueApplicationCSV) {
		t.Error("expected CSV content type header")
	}

}

func TestMarshalTimeseriesJSON(t *testing.T) {

	err := marshalTimeseriesJSON(nil, nil, 200, nil)
	if err != nil {
		t.Error(err)
	}

}

func TestWriteEpochTime(t *testing.T) {

	now := time.Now()
	w := httptest.NewRecorder()
	writeEpochTime(w, epoch.Epoch(now.UnixNano()), 1000000)

	b, err := io.ReadAll(w.Body)
	if err != nil {
		t.Error(err)
	}

	expected := strconv.FormatInt(now.UnixNano()/1000000, 10)
	if string(b) != expected {
		t.Errorf("expected %s got %s", expected, string(b))
	}

}

func TestWriteValue(t *testing.T) {

	tests := []struct {
		val         interface{}
		nilVal      string
		expectedErr error
		expectedVal string
	}{
		{ // 0
			val:         nil,
			nilVal:      "NULL",
			expectedErr: nil,
			expectedVal: "NULL",
		},
		{ // 1
			val:         "trickster",
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `"trickster"`,
		},
		{ // 2
			val:         true,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `true`,
		},
		{ // 3
			val:         int64(1),
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `1`,
		},
		{ // 4
			val:         1,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `1`,
		},
		{ // 5
			val:         1.1,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `1.1`,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			w := httptest.NewRecorder()
			writeValue(w, test.val, test.nilVal)
			b, err := io.ReadAll(w.Body)
			if err != test.expectedErr {
				t.Errorf("expected %s got %s", test.expectedErr.Error(), err.Error())
			}
			if string(b) != test.expectedVal {
				t.Errorf("expected %s got %s", test.expectedVal, string(b))
			}
		})
	}
}

func TestWriteCSVValue(t *testing.T) {

	tests := []struct {
		val         interface{}
		nilVal      string
		expectedErr error
		expectedVal string
	}{
		{ // 0
			val:         nil,
			nilVal:      "NULL",
			expectedErr: nil,
			expectedVal: "NULL",
		},
		{ // 1
			val:         `"trickster"`,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `"\"trickster\""`,
		},
		{ // 1
			val:         `trickster 2.0`,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `"trickster 2.0"`,
		},
		{ // 2
			val:         true,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `true`,
		},
		{ // 3
			val:         int64(1),
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `1`,
		},
		{ // 4
			val:         1,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `1`,
		},
		{ // 5
			val:         1.1,
			nilVal:      "",
			expectedErr: nil,
			expectedVal: `1.1`,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			w := httptest.NewRecorder()
			writeCSVValue(w, test.val, test.nilVal)
			b, err := io.ReadAll(w.Body)
			if err != test.expectedErr {
				t.Errorf("expected %s got %s", test.expectedErr.Error(), err.Error())
			}
			if string(b) != test.expectedVal {
				t.Errorf("expected %s got %s", test.expectedVal, string(b))
			}
		})
	}

}

func TestMarshalTimeseriesJSONPretty(t *testing.T) {

	err := marshalTimeseriesJSONPretty(nil, nil, 200, nil)
	if err != nil {
		t.Error(err)
	}

}

func TestGetDateWriter(t *testing.T) {

	rlo := &timeseries.RequestOptions{TimeFormat: 1}
	dw, m := getDateWriter(rlo)
	if dw == nil {
		t.Error("expected non-nil func value")
	}
	if m != 1 {
		t.Errorf("expected %d got %d", 1, m)
	}

	rlo.TimeFormat = 3
	dw, m = getDateWriter(rlo)
	if dw == nil {
		t.Error("expected non-nil func value")
	}
	if m != 1000000 {
		t.Errorf("expected %d got %d", 1000000, m)
	}

	rlo.TimeFormat = 250
	dw, m = getDateWriter(rlo)
	if dw == nil {
		t.Error("expected non-nil func value")
	}
	if m != 1 {
		t.Errorf("expected %d got %d", 1, m)
	}
}
