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

func TestCalculateHash(t *testing.T) {

	a := &WFAlert{
		State: "test",
	}

	i := a.CalculateHash()
	const expected = 640439168010397861

	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}

}

func TestMerge(t *testing.T) {

	a1 := &WFAlerts{
		Envelope: &Envelope{
			Status: "error",
		},
		Data: &WFAlertData{
			Alerts: []WFAlert{
				{
					State:  "test",
					Labels: map[string]string{"test": "trickster"},
				}},
		},
	}
	a2 := &WFAlerts{
		Envelope: &Envelope{
			Status: "success",
		},
		Data: &WFAlertData{
			Alerts: []WFAlert{
				{
					State:  "test2",
					Labels: map[string]string{"test2": "trickster"},
				},
			},
		},
	}
	a1.Merge(a2)
	if len(a1.Data.Alerts) != 2 {
		t.Errorf("expected %d got %d", 2, len(a1.Data.Alerts))
	}

	if a1.Envelope.Status != "success" {
		t.Errorf("expected %s got %s", "success", a1.Envelope.Status)
	}

}

func TestMergeAndWriteAlerts(t *testing.T) {

	var nilRG *merge.ResponseGate

	tests := []struct {
		r       *http.Request
		rgs     merge.ResponseGates
		expCode int
	}{
		{
			nil,
			nil,
			http.StatusBadGateway,
		},
		{
			nil,
			merge.ResponseGates{nilRG},
			http.StatusBadGateway,
		},
		{
			nil,
			testResponseGates1(),
			http.StatusOK,
		},
		{
			nil,
			testResponseGates2(),
			http.StatusBadRequest,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			w := httptest.NewRecorder()
			MergeAndWriteAlerts(w, test.r, test.rgs)
			if w.Code != test.expCode {
				t.Errorf("expected %d got %d", test.expCode, w.Code)
			}
		})
	}

}

func testResponseGates1() merge.ResponseGates {

	b1 := []byte(`{"status":"success","data":{"alerts":[
		{"state":"test","labels":{},"annotations":{},"value":"x","activeAt":"y"}
	]}}`)
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

	b3 := []byte(`{"status":"success","data":{"alerts":[]}}`)
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

func testResponseGates2() merge.ResponseGates {

	b1 := []byte(`{"status":"error","data":{"alerts":[]}}`)
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

	b2 := []byte(`{"status":"error","data":{"alerts":[]}}`)
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
