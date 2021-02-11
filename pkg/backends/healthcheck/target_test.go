/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"io/ioutil"
	"net/http"
	"strconv"
	"testing"

	ho "github.com/tricksterproxy/trickster/pkg/backends/healthcheck/options"
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
			res := test.target.isGoodBody(ioutil.NopCloser(bytes.NewReader([]byte(test.body))))
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
