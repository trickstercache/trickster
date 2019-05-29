/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package promsim

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewTestServer(t *testing.T) {
	if NewTestServer() == nil {
		t.Errorf("failed to get test server object")
	}
}

func TestQueryRangeHandler(t *testing.T) {

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query_range?query=up&start=0&end=30&step=15", nil)
	queryRangeHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	const expected = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"series_id":"0"},"values":[[0,"29"],[15,"81"],[30,"23"]]}]}}`

	if string(bodyBytes) != expected {
		t.Errorf("expected %s got %s", expected, bodyBytes)
	}
}

func TestQueryRangeHandlerMissingParam(t *testing.T) {

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query_range?q=up&start=0&end=30&step=15", nil)
	queryRangeHandler(w, r)

	resp := w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}
}

func TestQueryRangeHandlerInvalidParam(t *testing.T) {

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query_range?query=up&start=foo&end=30&step=15", nil)
	queryRangeHandler(w, r)

	resp := w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "http://0/query_range?query=up&start=0&end=foo&step=15", nil)
	queryRangeHandler(w, r)

	resp = w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "http://0/query_range?query=up&start=0&end=30&step=foo", nil)
	queryRangeHandler(w, r)

	resp = w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "http://0/query_range?query=up{status_code=400}&start=0&end=30&step=15", nil)
	queryRangeHandler(w, r)

	resp = w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}
}

func TestQueryHandler(t *testing.T) {

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query?query=up&time=0", nil)
	queryHandler(w, r)

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	const expected = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"series_id":"0"},"value":[0,"29"]}]}}`

	if string(bodyBytes) != expected {
		t.Errorf("expected %s got %s", expected, bodyBytes)
	}
}

func TestQueryHandlerMissingParam(t *testing.T) {

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query?q=up", nil)
	queryHandler(w, r)
	resp := w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}
}

func TestQueryHandlerInvalidParam(t *testing.T) {

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/query?query=up&time=foo", nil)
	queryHandler(w, r)
	resp := w.Result()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected %d got %d", http.StatusBadRequest, resp.StatusCode)
	}
}
