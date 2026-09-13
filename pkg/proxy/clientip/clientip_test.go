/*
 * Copyright 2026 The Trickster Authors
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

package clientip

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

func TestParseTrusted(t *testing.T) {
	trusted, err := ParseTrusted(nil)
	require.NoError(t, err)
	require.Nil(t, trusted)

	trusted, err = ParseTrusted([]string{"10.0.0.0/8", " 192.168.1.5 ", "", "2001:db8::/32", "::1"})
	require.NoError(t, err)
	require.Len(t, trusted, 4)
	require.True(t, trusted.Contains(netip.MustParseAddr("10.20.30.40")))
	require.True(t, trusted.Contains(netip.MustParseAddr("192.168.1.5")))
	require.False(t, trusted.Contains(netip.MustParseAddr("192.168.1.6")))
	require.True(t, trusted.Contains(netip.MustParseAddr("2001:db8:1::1")))
	require.True(t, trusted.Contains(netip.MustParseAddr("::1")))
	require.True(t, trusted.Contains(netip.MustParseAddr("::ffff:10.1.1.1")))
	require.False(t, trusted.Contains(netip.MustParseAddr("203.0.113.1")))

	_, err = ParseTrusted([]string{"not-an-address"})
	require.ErrorIs(t, err, ErrInvalidTrustedProxy)
	_, err = ParseTrusted([]string{"10.0.0.0/33"})
	require.ErrorIs(t, err, ErrInvalidTrustedProxy)
}

func TestResolve(t *testing.T) {
	trusted, err := ParseTrusted([]string{"10.0.0.0/8", "172.16.0.1"})
	require.NoError(t, err)

	newReq := func(remote string, h map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.RemoteAddr = remote
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}

	tests := []struct {
		name    string
		trusted Trusted
		r       *http.Request
		want    string
	}{
		{"no trusted proxies", nil,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXForwardedFor: "203.0.113.9"}), "10.0.0.1"},
		{"untrusted peer ignores headers", trusted,
			newReq("198.51.100.4:80", map[string]string{headers.NameXForwardedFor: "203.0.113.9"}), "198.51.100.4"},
		{"trusted peer without headers", trusted, newReq("10.0.0.1:1234", nil), "10.0.0.1"},
		{"xff single", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXForwardedFor: "203.0.113.9"}), "203.0.113.9"},
		{"xff skips trusted hops from the right", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXForwardedFor: "203.0.113.9, 172.16.0.1, 10.9.9.9"}),
			"203.0.113.9"},
		{"xff all trusted returns leftmost", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXForwardedFor: "10.1.1.1, 10.2.2.2"}), "10.1.1.1"},
		{"xff invalid entries skipped", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXForwardedFor: "203.0.113.9, unknown"}), "203.0.113.9"},
		{"xff with port and v6 brackets", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXForwardedFor: "[2001:db8::1]:8080"}), "2001:db8::1"},
		{"forwarded header preferred", trusted,
			newReq("10.0.0.1:1234", map[string]string{
				headers.NameForwarded:     `for="[2001:db8::2]";proto=https, for=10.3.3.3`,
				headers.NameXForwardedFor: "203.0.113.9",
			}), "2001:db8::2"},
		{"x-real-ip fallback", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXRealIP: "203.0.113.10"}), "203.0.113.10"},
		{"x-real-ip invalid falls back to peer", trusted,
			newReq("10.0.0.1:1234", map[string]string{headers.NameXRealIP: "bogus"}), "10.0.0.1"},
		{"remote addr without port", trusted, newReq("10.0.0.1", nil), "10.0.0.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, Resolve(tc.r, tc.trusted))
		})
	}
}

func TestMiddleware(t *testing.T) {
	trusted, err := ParseTrusted([]string{"10.0.0.0/8"})
	require.NoError(t, err)
	var got string
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = tctx.ClientIP(r.Context())
	})
	require.Nil(t, Middleware(trusted, nil))
	h := Middleware(nil, next)
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set(headers.NameXForwardedFor, "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.Empty(t, got)

	h = Middleware(trusted, next)
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.Equal(t, "203.0.113.9", got)

	r.RemoteAddr = ""
	h.ServeHTTP(httptest.NewRecorder(), r)
	require.Empty(t, got)
}
