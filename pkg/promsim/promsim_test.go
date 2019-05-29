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
	"net/http"
	"testing"
	"time"
)

const testQuery = "myQuery{other_label=5,latency_ms=1,range_latency_ms=1,series_count=1,test}"
const expectedRangeOutput = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"other_label":"5","latency_ms":"1","range_latency_ms":"1","series_count":"1","test":"","series_id":"0"},"values":[[0,"25"],[1800,"92"],[3600,"89"]]}]}}`
const expectedInstantOutput = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"other_label":"5","latency_ms":"1","range_latency_ms":"1","series_count":"1","test":"","series_id":"0"},"value":[0,"25"]}]}}`

const testQueryInvalidResponse = "myQuery{invalid_response_body=1}"
const expectedInvalidResponse = "foo"

const testFullQuery = `myQuery{other_label=a5,max_val=1,min_val=1,series_id=1,status_code=200,test}`
const expectedFullRawstring = `"other_label":"a5","max_val":"1","min_val":"1","series_id":"1","status_code":"200","test":""`

func TestGetTimeSeriesData(t *testing.T) {
	out, code, err := GetTimeSeriesData(testQuery, time.Unix(0, 0), time.Unix(3600, 0), time.Duration(1800)*time.Second)
	if err != nil {
		t.Error(err)
	}

	if code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, code)
	}

	if out != expectedRangeOutput {
		t.Errorf("expected %s got %s", expectedRangeOutput, out)
	}
}

func TestGetTimeSeriesDataInvalidResponseBody(t *testing.T) {
	out, code, err := GetTimeSeriesData(testQueryInvalidResponse, time.Unix(0, 0), time.Unix(3600, 0), time.Duration(1800)*time.Second)
	if err != nil {
		t.Error(err)
	}

	if code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, code)
	}

	if out != expectedInvalidResponse {
		t.Errorf("expected %s got %s", expectedInvalidResponse, out)
	}
}

func TestGetInstantData(t *testing.T) {
	out, code, err := GetInstantData(testQuery, time.Unix(0, 0))
	if err != nil {
		t.Error(err)
	}

	if code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, code)
	}

	if out != expectedInstantOutput {
		t.Errorf("expected %s got %s", expectedInstantOutput, out)
	}
}

func TestGetInstantDataInvalidResponseBody(t *testing.T) {
	out, code, err := GetInstantData(testQueryInvalidResponse, time.Time{})
	if err != nil {
		t.Error(err)
	}

	if code != http.StatusOK {
		t.Errorf("expected %d got %d", http.StatusOK, code)
	}

	if out != expectedInvalidResponse {
		t.Errorf("expected %s got %s", expectedInvalidResponse, out)
	}
}

func TestAddLabel(t *testing.T) {

	d := &Directives{}

	const label1 = "test1"
	const label2 = "test2"
	const labels = "test1,test2"

	d.addLabel(label1)
	if d.rawString != label1 {
		t.Errorf("expected %s got %s", label1, d.rawString)
	}

	d.addLabel(label2)
	if d.rawString != labels {
		t.Errorf("expected %s got %s", labels, d.rawString)
	}
}

func TestGetDirectives(t *testing.T) {

	d := getDirectives(testFullQuery)
	if d.rawString != expectedFullRawstring {
		t.Errorf("expected %s got %s", expectedFullRawstring, d.rawString)
	}

}
