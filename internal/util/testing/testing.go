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

package testing

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"time"
)

// NewTestServer returns a new httptest.Server that responds with the provided code and body
func NewTestServer(responseCode int, responseBody string) *httptest.Server {
	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(responseCode)
		fmt.Fprint(w, responseBody)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))
	return s
}

// NewTestWebClient returns a new *http.Client configured with reasonable defaults
func NewTestWebClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial:                (&net.Dialer{KeepAlive: 300 * time.Second}).Dial,
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 20,
		},
	}
}
