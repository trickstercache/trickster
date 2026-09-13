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

package urls

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitHostPort(t *testing.T) {
	tests := []struct{ host, hostname, port string }{
		{"", "", ""},
		{"example.com", "example.com", ""},
		{"example.com:8080", "example.com", "8080"},
		{"[::1]", "::1", ""},
		{"[::1]:8080", "::1", "8080"},
		{"10.0.0.1:443", "10.0.0.1", "443"},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			hostname, port := SplitHostPort(tt.host)
			require.Equal(t, tt.hostname, hostname)
			require.Equal(t, tt.port, port)
		})
	}
}

func TestJoinHostPort(t *testing.T) {
	require.Equal(t, "example.com", JoinHostPort("example.com", ""))
	require.Equal(t, "example.com:80", JoinHostPort("example.com", "80"))
	require.Equal(t, "[::1]", JoinHostPort("::1", ""))
	require.Equal(t, "[::1]", JoinHostPort("[::1]", ""))
	require.Equal(t, "[::1]:80", JoinHostPort("::1", "80"))
	require.Equal(t, "[::1]:80", JoinHostPort("[::1]", "80"))
	require.Empty(t, JoinHostPort("", ""))
}

func TestReplaceHostnameAndPort(t *testing.T) {
	require.Equal(t, "other.example.com:8080", ReplaceHostname("example.com:8080", "other.example.com"))
	require.Equal(t, "other.example.com", ReplaceHostname("example.com", "other.example.com"))
	require.Equal(t, "[::1]:8080", ReplaceHostname("example.com:8080", "::1"))
	require.Equal(t, "example.com:9000", ReplacePort("example.com:8080", "9000"))
	require.Equal(t, "example.com", ReplacePort("example.com:8080", ""))
	require.Equal(t, "[::1]:9000", ReplacePort("[::1]:8080", "9000"))
}

func TestRequestScheme(t *testing.T) {
	require.Equal(t, "http", RequestScheme(nil))
	r, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	require.Equal(t, "http", RequestScheme(r))
	r.TLS = &tls.ConnectionState{}
	require.Equal(t, "https", RequestScheme(r))
}
