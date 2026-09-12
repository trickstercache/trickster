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

package parts

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func testRequest(t *testing.T) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost,
		"https://user:pw@example.com:8480/path1/path2?param1=value&empty=", nil)
	require.NoError(t, err)
	r.Header.Set("X-Tenant", "a")
	r.Header.Set("X-Empty", "")
	return r
}

func TestGetters(t *testing.T) {
	r := testRequest(t)
	require.Equal(t, http.MethodPost, Method(r))
	require.Equal(t, "https", Scheme(r))
	require.Equal(t, "example.com:8480", Host(r))
	require.Equal(t, "example.com", Hostname(r))
	require.Equal(t, "8480", Port(r))
	require.Equal(t, "/path1/path2", Path(r))
	require.Equal(t, "param1=value&empty=", RawQuery(r))
	require.Equal(t, "https://user:pw@example.com:8480/path1/path2?param1=value&empty=", URL(r))
	require.Equal(t, "https://user:pw@example.com:8480/path1/path2", URLNoParams(r))
	require.Equal(t, "a", Header(r, "X-Tenant"))
	require.Empty(t, Header(r, "X-Empty"))
	require.True(t, HasHeader(r, "X-Empty"))
	require.False(t, HasHeader(r, "X-Absent"))

	q := Query(r)
	require.Equal(t, "value", Param(q, "param1"))
	require.Empty(t, Param(q, "empty"))
	require.True(t, HasParam(q, "empty"))
	require.False(t, HasParam(q, "absent"))

	r.URL.ForceQuery = true
	r.URL.RawQuery = ""
	require.Equal(t, "https://user:pw@example.com:8480/path1/path2", URLNoParams(r))
}

func TestGettersTolerateNil(t *testing.T) {
	for _, r := range []*http.Request{nil, {}} {
		require.Empty(t, Method(r))
		require.Empty(t, Scheme(r))
		require.Empty(t, Host(r))
		require.Empty(t, Hostname(r))
		require.Empty(t, Port(r))
		require.Empty(t, Path(r))
		require.Empty(t, RawQuery(r))
		require.Empty(t, URL(r))
		require.Empty(t, URLNoParams(r))
		require.Empty(t, Header(r, "X-Tenant"))
		require.False(t, HasHeader(r, "X-Tenant"))
		require.Nil(t, Query(r))
		SetMethod(r, "GET")
		SetScheme(r, "http")
		SetRawQuery(r, "a=1")
		SetHost(r, "h")
		SetHostname(r, "h")
		SetPort(r, "1")
		SetPath(r, "/")
	}
	require.Empty(t, Param(nil, "a"))
	require.False(t, HasParam(nil, "a"))
}

func TestSetters(t *testing.T) {
	r := testRequest(t)
	SetMethod(r, http.MethodGet)
	SetScheme(r, "http")
	SetRawQuery(r, "z=1")
	SetPath(r, "/other")
	require.Equal(t, http.MethodGet, r.Method)
	require.Equal(t, "http", r.URL.Scheme)
	require.Equal(t, "z=1", r.URL.RawQuery)
	require.Equal(t, "/other", r.URL.Path)

	SetHostname(r, "other.example.com")
	require.Equal(t, "other.example.com:8480", r.URL.Host)
	SetPort(r, "9000")
	require.Equal(t, "other.example.com:9000", r.URL.Host)
	SetPort(r, "")
	require.Equal(t, "other.example.com", r.URL.Host)
	SetHostname(r, "::1")
	require.Equal(t, "[::1]", r.URL.Host)
	SetPort(r, "443")
	require.Equal(t, "[::1]:443", r.URL.Host)
	SetHost(r, "third.example.com:1")
	require.Equal(t, "third.example.com:1", r.URL.Host)

	r.URL = &url.URL{Host: "[fe80::1]:8080"}
	SetHostname(r, "example.com")
	require.Equal(t, "example.com:8080", r.URL.Host)
}
