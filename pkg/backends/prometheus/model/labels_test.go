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
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

func TestMergeLabelData(t *testing.T) {

	ld1 := &WFLabelData{
		Envelope: &Envelope{
			Status: "error",
			Error:  "test error",
		},
		Data: []string{"test", "trickster"},
	}
	ld2 := &WFLabelData{
		Envelope: &Envelope{
			Status: "success",
		},
		Data: []string{"test2", "trickster2"},
	}
	ld3 := &WFLabelData{
		Envelope: &Envelope{
			Status: "error",
			Error:  "test error",
		},
		Data: []string{"test3", "trickster3"}, // should not be appended due to error
	}
	ld4 := &WFLabelData{
		Envelope: &Envelope{
			Status:   "success",
			Warnings: []string{"test warning 1"},
		},
		Data: []string{"test3", "trickster3"},
	}
	ld1.Merge(ld2)
	ld1.Merge(ld3)
	ld1.Merge(ld4)
	if len(ld1.Data) != 6 {
		t.Errorf("expected %d got %d", 6, len(ld1.Data))
	}

	if ld1.Envelope.Status != "success" {
		t.Errorf("expected %s got %s", "success", ld1.Envelope.Status)
	}

	if len(ld1.Envelope.Warnings) != 3 {
		t.Errorf("expected %d got %d", 3, len(ld1.Envelope.Warnings))
	}

}

func TestMergeAndWriteLabelData(t *testing.T) {

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
			testResponseGates3(),
			http.StatusOK,
		},
		{ // 3
			nil,
			testResponseGates4(),
			http.StatusBadRequest,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			w := httptest.NewRecorder()
			MergeAndWriteLabelData(w, test.r, test.rgs)
			if w.Code != test.expCode {
				t.Errorf("expected %d got %d", test.expCode, w.Code)
			}
		})
	}

}

func testResponseGates3() merge.ResponseGates {

	b1 := []byte(`{"status":"success","data":["test", "trickster"]}`)
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

	b3 := []byte(`{"status":"success","data":["test2", "trickster2"]}`)
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

func testResponseGates4() merge.ResponseGates {

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

	b2 := []byte(`{"status":"error","data":["should", "not", "append"]`)
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
