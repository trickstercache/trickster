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

package healthcheck

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestNewTarget(t *testing.T) {

	_, err := newTarget(nil, "", "", nil, nil, nil)
	if err != ho.ErrNoOptionsProvided {
		t.Errorf("expected %v got %v", ho.ErrNoOptionsProvided, err)
	}

	ctx := context.Background()
	o := ho.New()
	o.FailureThreshold = -1
	o.RecoveryThreshold = -1
	o.Headers = map[string]string{"test-header": "test-header-value"}
	o.ExpectedHeaders = map[string]string{"test-header1": "test-header-value1"}
	o.SetExpectedBody("expectedBody")
	o.ExpectedCodes = nil

	_, err = newTarget(ctx, "test", "test", o, nil, nil)
	if err != nil {
		t.Error(err)
	}

	expected := `net/http: invalid method "INVALID METHOD"`
	o.Verb = "INVALID METHOD"
	_, err = newTarget(ctx, "test", "test", o, nil, nil)
	if err.Error() != expected {
		t.Error("expected error for invalid method, got ", err)
	}

}

func TestIsGoodHeader(t *testing.T) {

	tests := []struct {
		target         *target
		header         http.Header
		expectedResult bool
		expectedDetail string
	}{
		{ // 0
			&target{status: &Status{}},
			nil,
			true,
			"",
		},
		{ // 1
			&target{status: &Status{},
				eh: http.Header{"test-header": []string{"test-header-value"}}},
			nil,
			false,
			"no response headers",
		},
		{ // 2
			&target{status: &Status{},
				eh: http.Header{"Test": []string{"test-header-value"}},
			},
			http.Header{"Test-1": []string{"test-header-value"}},
			false,
			"server response is missing required header [Test]",
		},
		{ // 3
			&target{status: &Status{},
				eh: http.Header{"Test": []string{"test-header-value"}},
			},
			http.Header{"Test": []string{"test-header-value-1"}},
			false,
			"required header mismatch for [Test] got [test-header-value-1] expected [test-header-value]",
		},
		{ // 4
			&target{status: &Status{},
				eh: http.Header{"Test": []string{"test-header-value"}},
			},
			http.Header{"Test": []string{"test-header-value"}},
			true,
			"",
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := test.target.isGoodHeader(test.header)
			if res != test.expectedResult {
				t.Errorf("expected %t got %t", test.expectedResult, res)
			}
			if test.target.status.detail != test.expectedDetail {
				t.Errorf("expected %s got %s", test.expectedDetail, test.target.status.detail)
			}
		})
	}
}

func TestIsGoodCode(t *testing.T) {

	tests := []struct {
		target   *target
		code     int
		expected bool
	}{
		{ // 0
			&target{status: &Status{}},
			0,
			false,
		},
		{ // 1
			&target{status: &Status{},
				ec: []int{200},
			},
			404,
			false,
		},
		{ // 2
			&target{status: &Status{},
				ec: []int{200},
			},
			200,
			true,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := test.target.isGoodCode(test.code)
			if res != test.expected {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}
}

func TestIsGoodBody(t *testing.T) {

	tests := []struct {
		target   *target
		body     string
		expected bool
	}{
		{ // 0
			&target{status: &Status{}},
			"",
			true,
		},
		{ // 1
			&target{status: &Status{},
				ceb: true,
			},
			"",
			true,
		},
		{ // 2
			&target{status: &Status{},
				ceb: true,
				eb:  "trickster",
			},
			"",
			false,
		},
		{ // 3
			&target{status: &Status{},
				ceb: true,
				eb:  "trickster",
			},
			"trickster",
			true,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := test.target.isGoodBody(io.NopCloser(bytes.NewReader([]byte(test.body))))
			if res != test.expected {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}
}

func TestNewHTTPClient(t *testing.T) {

	c := newHTTPClient(0)
	if c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("expected", http.ErrUseLastResponse)
	}
}

func TestProbe(t *testing.T) {

	ts := newTestServer(200, "OK", map[string]string{})

	r, _ := http.NewRequest("GET", ts.URL+"/", nil)
	target := &target{
		status:      &Status{},
		ctx:         context.Background(),
		baseRequest: r,
		httpClient:  ts.Client(),
		ec:          []int{200},
	}
	target.probe()
	if target.successConsecutiveCnt.Load() != 1 {
		t.Error("expected 1 got ", target.successConsecutiveCnt)
	}
	target.ec[0] = 404
	target.probe()
	if target.successConsecutiveCnt.Load() != 0 {
		t.Error("expected 0 got ", target.successConsecutiveCnt)
	}
	if target.failConsecutiveCnt.Load() != 1 {
		t.Error("expected 1 got ", target.failConsecutiveCnt)
	}

}

func TestDemandProbe(t *testing.T) {

	ts := newTestServer(200, "OK", map[string]string{})

	w := httptest.NewRecorder()

	r, _ := http.NewRequest("GET", ts.URL+"/", nil)
	target := &target{
		status:      &Status{},
		ctx:         context.Background(),
		baseRequest: r,
		httpClient:  ts.Client(),
		ec:          []int{200},
	}
	target.demandProbe(w)

	if w.Code != 200 {
		t.Error("expected 200 got ", w.Code)
	}

	// simulate a failed probe (bad response)
	w = httptest.NewRecorder()
	target.status.status.Store(-1)
	target.demandProbe(w)

	if w.Code != 200 {
		t.Error("expected 200 got ", w.Code)
	}

	// simulate a failed probe (unreachable)
	ts.Close()
	w = httptest.NewRecorder()
	target.status.status.Store(-1)
	target.demandProbe(w)

	if w.Code != 500 {
		t.Error("expected 500 got ", w.Code)
	}

}

func newTestServer(responseCode int, responseBody string,
	hdrs map[string]string) *httptest.Server {
	handler := func(w http.ResponseWriter, r *http.Request) {
		headers.UpdateHeaders(w.Header(), hdrs)
		w.WriteHeader(responseCode)
		fmt.Fprint(w, responseBody)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))
	return s
}
