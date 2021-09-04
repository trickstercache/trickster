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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
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

	var nilRG *merge.ResponseGate

	tests := []struct {
		r       *http.Request
		rgs     merge.ResponseGates
		expCode int
	}{
		{ // 0
			nil,
			nil,
			http.StatusBadGateway,
		},
		{ // 1
			nil,
			merge.ResponseGates{nilRG},
			http.StatusBadGateway,
		},
		{ // 2
			nil,
			testResponseGates5(),
			http.StatusOK,
		},
		{ // 3
			nil,
			testResponseGates6(),
			http.StatusBadRequest,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			w := httptest.NewRecorder()
			MergeAndWriteSeries(w, test.r, test.rgs)
			if w.Code != test.expCode {
				t.Errorf("expected %d got %d", test.expCode, w.Code)
			}
		})
	}

}

func testResponseGates5() merge.ResponseGates {

	b1 := []byte(`{"status":"success","data":[{"__name__":"test1","instance":"i1","job":"trickster"}]}`)
	closer1 := io.NopCloser(bytes.NewReader(b1))
	rsc1 := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc1.Response = &http.Response{
		Body:       closer1,
		StatusCode: 200,
	}
	rg1 := merge.NewResponseGate(
		nil, // w
		nil, // r
		rsc1,
	)
	rg1.Write(b1)

	b2bad := []byte(`{"stat`)
	closer2 := io.NopCloser(bytes.NewReader(b2bad))
	rsc2 := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc2.Response = &http.Response{
		Body:       closer2,
		StatusCode: 200,
	}
	rg2 := merge.NewResponseGate(
		nil, // w
		nil, // r
		rsc2,
	)
	rg2.Write(b2bad)

	b3 := []byte(`{"status":"success","data":[{"__name__":"test1","instance":"i2","job":"trickster"}]}`)
	closer3 := io.NopCloser(bytes.NewReader(b3))
	rsc3 := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc3.Response = &http.Response{
		Body:       closer3,
		StatusCode: 200,
	}
	rg3 := merge.NewResponseGate(
		nil, // w
		nil, // r
		rsc3,
	)
	rg3.Write(b3)

	return merge.ResponseGates{rg1, rg2, rg3}

}

func testResponseGates6() merge.ResponseGates {

	b1 := []byte(`{"status":"error"`)
	closer1 := io.NopCloser(bytes.NewReader(b1))
	rsc1 := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc1.Response = &http.Response{
		Body:       closer1,
		StatusCode: 400,
	}
	rg1 := merge.NewResponseGate(
		nil, // w
		nil, // r
		rsc1,
	)
	rg1.Write(b1)

	b2 := []byte(`{"status":"error","data":[{"__name__":"should","instance":"not","job":"append"}]}`)
	closer2 := io.NopCloser(bytes.NewReader(b1))
	rsc2 := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc2.Response = &http.Response{
		Body:       closer2,
		StatusCode: 400,
	}
	rg2 := merge.NewResponseGate(
		nil, // w
		nil, // r
		rsc1,
	)
	rg2.Write(b2)

	return merge.ResponseGates{rg1, rg2}

}
