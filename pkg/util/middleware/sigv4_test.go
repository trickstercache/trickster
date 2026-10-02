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

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

func TestStripSigV4(t *testing.T) {
	tests := []struct {
		name, auth, wantAuth string
	}{
		{"sigv4", "AWS4-HMAC-SHA256 Credential=ASIA/20261002/us-east-1/aps/aws4_request, Signature=x", ""},
		{"sigv4a", "AWS4-ECDSA-P256-SHA256 Credential=ASIA/20261002/aps/aws4_request, Signature=x", ""},
		{"basic auth is kept", "Basic dXNlcjpwYXNz", "Basic dXNlcjpwYXNz"},
		{"no auth", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			h := StripSigV4(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
			}))
			r := httptest.NewRequest(http.MethodGet, "http://trickster/api/v1/query", nil)
			if tc.auth != "" {
				r.Header.Set(headers.NameAuthorization, tc.auth)
			}
			r.Header.Set(headers.NameXAmzDate, "20261002T000000Z")
			r.Header.Set(headers.NameXAmzSecurityToken, "client-token")
			r.Header.Set(headers.NameXAmzContentSHA256, "e3b0")
			r.Header.Set("X-Keep", "1")
			h.ServeHTTP(httptest.NewRecorder(), r)
			require.Equal(t, tc.wantAuth, got.Get(headers.NameAuthorization))
			for _, name := range sigV4Fields {
				require.Empty(t, got.Get(name), name)
			}
			require.Equal(t, "1", got.Get("X-Keep"))
		})
	}
}

func TestStripSigV4RecordsClientScope(t *testing.T) {
	var scope taws.Scope
	var ok bool
	h := StripSigV4(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		scope, ok = taws.ClientScope(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "http://trickster/api/v1/query", nil)
	r.Header.Set(headers.NameAuthorization,
		"AWS4-HMAC-SHA256 Credential=ASIA/20261002/us-west-2/monitoring/aws4_request, Signature=x")
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.True(t, ok)
	require.Equal(t, taws.Scope{Service: "monitoring", Region: "us-west-2"}, scope)

	r = httptest.NewRequest(http.MethodGet, "http://trickster/api/v1/query", nil)
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.False(t, ok, "an unsigned request records no scope")
}
