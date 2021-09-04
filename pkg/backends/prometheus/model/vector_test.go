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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testVector = `{"status":"success","data":{"resultType":"vector","result":[` +
	`{"metric":{"__name__":"go_memstats_alloc_bytes","instance":` +
	`"docker.for.mac.localhost:8481","job":"trickster"},` +
	`"value":[1577836800,"1"]}]}}`

const testVector2 = `{"status":"success","data":{"resultType":"vector","result":[` +
	`{"metric":{"__name__":"go_memstats_alloc_bytes","instance":` +
	`"trickstercache.org:8481","job":"trickster"},` +
	`"value":[1577836800,"1"]}]}}`

func TestMergeAndWriteVector(t *testing.T) {

	w := httptest.NewRecorder()
	MergeAndWriteVector(w, nil, nil)
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
	}

	w = httptest.NewRecorder()
	MergeAndWriteVector(w, nil, testResponseGates7())
	if w.Code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, w.Code)
	}

	w = httptest.NewRecorder()
	MergeAndWriteVector(w, nil, testResponseGates8())
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusOK, w.Code)
	}
}

func testResponseGates7() merge.ResponseGates {

	b1 := []byte(testVector)
	closer1 := io.NopCloser(bytes.NewReader(b1))
	rsc1 := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc1.Response = &http.Response{
		Body:       closer1,
		StatusCode: 200,
	}
	rsc1.TimeRangeQuery = &timeseries.TimeRangeQuery{}
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

	b3 := []byte(testVector2)
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

	var rg4 *merge.ResponseGate

	return merge.ResponseGates{rg1, rg2, rg4, rg3}

}

func testResponseGates8() merge.ResponseGates {

	b1 := []byte(`{"status":"error","data":{}`)
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

	b2 := []byte(`{"status":"error","data":{}`)
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
