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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

const testSeries = `{
	"status": "success",
	"data": [
	  {
		"__name__": "up",
		"instance": "localhost:8481",
		"job": "trickster"
	  },
	  {
		"__name__": "up",
		"instance": "localhost:9090",
		"job": "prometheus"
	  }
	]
  }`

func TestSeries(t *testing.T) {
	s := &WFSeries{}
	json.Unmarshal([]byte(testSeries), &s)

	if len(s.Data) != 2 {
		t.Error("expected 2 data points")
	}

	s1 := &WFSeries{
		Envelope: &Envelope{
			Status: "success",
		},
		Data: []WFSeriesData{
			{
				Name:     "test1",
				Instance: "instance1",
				Job:      "job1",
			},
		},
	}

	s2 := &WFSeries{
		Envelope: &Envelope{
			Status:    "error",
			ErrorType: "bad_data",
			Error:     "cannot parse",
		},
	}

	s3 := &WFSeries{
		Envelope: &Envelope{
			Status:   "success",
			Warnings: []string{"test warning"},
		},
		Data: []WFSeriesData{
			{
				Name:     "test1",
				Instance: "instance1",
				Job:      "job1",
			},
			{
				Name:     "test2",
				Instance: "instance",
				Job:      "job1",
			},
		},
	}

	s1.Merge(s2)

	if len(s1.Warnings) != 1 || s1.Warnings[0] != "cannot parse" {
		t.Error("expected error-to-warning")
	}

	if len(s1.Data) != 1 {
		t.Error("expected 1 element")
	}

	s1.Merge(s3)

	if len(s1.Data) != 2 {
		t.Error("expected 2 elements")
	}

	if len(s1.Warnings) != 2 || s1.Warnings[1] != "test warning" {
		t.Error("expected test warning")
	}

	s1.Merge(s2)

	if len(s1.Warnings) != 3 || s1.Warnings[2] != "cannot parse" {
		t.Error("expected error-to-warning")
	}

	s1.Warnings = nil

	s1.Merge(s3)

	if len(s1.Warnings) != 1 || s1.Warnings[0] != "test warning" {
		t.Error("expected test warning")
	}
}

func TestMergeAndWriteSeries(t *testing.T) {
	tests := []struct {
		name    string
		bodies  [][]byte
		expCode int
	}{
		{
			name:    "nil bodies",
			bodies:  nil,
			expCode: http.StatusOK,
		},
		{
			name:    "empty bodies",
			bodies:  [][]byte{},
			expCode: http.StatusOK,
		},
		{
			name: "valid merge",
			bodies: [][]byte{
				[]byte(`{"status":"success","data":[{"__name__":"test1","instance":"i1","job":"trickster"}]}`),
				[]byte(`{"stat`),
				[]byte(`{"status":"success","data":[{"__name__":"test1","instance":"i2","job":"trickster"}]}`),
			},
			expCode: http.StatusOK,
		},
		{
			name: "error status",
			bodies: [][]byte{
				[]byte(`{"status":"error"`),
				[]byte(`{"status":"error","data":[{"__name__":"should","instance":"not","job":"append"}]}`),
			},
			expCode: http.StatusBadRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "/", nil)
			accum := merge.NewAccumulator()
			mergeFunc := MergeAndWriteSeriesMergeFunc()
			respondFunc := MergeAndWriteSeriesRespondFunc()

			for i, body := range test.bodies {
				_ = mergeFunc(accum, body, i)
			}

			respondFunc(w, r, accum, test.expCode)

			if w.Code != test.expCode {
				t.Errorf("expected %d got %d", test.expCode, w.Code)
			}
		})
	}
}
