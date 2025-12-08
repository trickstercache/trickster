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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

func expectCounter(backendName, providerName, method, statusCode string, value float64) func(t *testing.T, basepath string, route string) {
	return func(t *testing.T, basepath string, route string) {
		metric := metrics.FrontendRequestStatus.WithLabelValues(backendName, providerName, method, route, statusCode)
		m := &dto.Metric{}
		require.NoError(t, metric.Write(m))
		require.Equal(t, value, *m.Counter.Value)
	}
}

func TestDecorate(t *testing.T) {
	const (
		backendName  = "backend1"
		providerName = "providerA"
	)
	cases := []struct {
		name           string
		handler        http.HandlerFunc
		do             func(t *testing.T, basepath string, route string) (*http.Response, error)
		expectedError  string
		expectedStatus int
		expect         func(t *testing.T, basepath string, route string)
	}{
		{
			name: "200 OK, GET",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				w.Write([]byte("hello world"))
			}),
			do: func(t *testing.T, basepath, route string) (*http.Response, error) {
				return http.Get(basepath + route)
			},
			expectedStatus: http.StatusOK,
			expect:         expectCounter(backendName, providerName, "GET", "2xx", float64(1)),
		},
		{
			name: "assumed 200, GET",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("hello world"))
			}),
			do: func(t *testing.T, basepath, route string) (*http.Response, error) {
				return http.Get(basepath + route)
			},
			expectedStatus: http.StatusOK,
			expect:         expectCounter(backendName, providerName, "GET", "2xx", float64(1)),
		},
		{
			name: "200 OK, POST",
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte("hello world"))
			}),
			do: func(t *testing.T, basepath, route string) (*http.Response, error) {
				return http.Post(basepath+route, "", nil)
			},
			expectedStatus: http.StatusOK,
			expect:         expectCounter(backendName, providerName, "POST", "2xx", float64(1)),
		},
	}

	for _, tc := range cases {
		// reset metrics between test cases
		metrics.FrontendRequestDuration.Reset()
		metrics.FrontendRequestStatus.Reset()
		metrics.FrontendRequestWrittenBytes.Reset()
		t.Run(tc.name, func(t *testing.T) {
			const (
				path = "/testpath"
			)
			decoratedHandler := Decorate(backendName, providerName, path, tc.handler)

			ts := httptest.NewServer(decoratedHandler)
			defer ts.Close()

			if tc.do == nil {
				return // nothing to do
			}
			resp, err := tc.do(t, ts.URL, path)
			if err != nil {
				if tc.expectedError == "" {
					require.NoError(t, err)
				}
				if err.Error() != tc.expectedError {
					require.ErrorContains(t, err, tc.expectedError)
				}
				return
			} else {
				require.Equal(t, tc.expectedStatus, resp.StatusCode, "unexpected status code")
			}
			if tc.expect != nil {
				tc.expect(t, ts.URL, path)
			}
		})
	}
}
