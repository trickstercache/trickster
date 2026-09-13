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

package redirect

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/stretchr/testify/require"
)

// rewritten models what a request rewriter leaves on the request URL
func rewritten(host, path string, edit func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = host
	if edit != nil {
		edit(r)
	}
	return r
}

func TestLocation(t *testing.T) {
	tests := []struct {
		name string
		r    *http.Request
		want string
	}{
		{
			"nothing set keeps the request", rewritten("shop.example.com:8080", "/a?b=1", nil),
			"http://shop.example.com:8080/a?b=1",
		},
		{"tls request keeps https", rewritten("shop.example.com", "/a", func(r *http.Request) {
			r.TLS = &tls.ConnectionState{}
		}), "https://shop.example.com/a"},
		{
			"explicit scheme drops the request port", rewritten("shop.example.com:8080", "/a",
				func(r *http.Request) { r.URL.Scheme = "https" }),
			"https://shop.example.com/a",
		},
		{
			"explicit scheme and port", rewritten("shop.example.com", "/a",
				func(r *http.Request) { r.URL.Scheme = "https"; r.URL.Host = ":8443" }),
			"https://shop.example.com:8443/a",
		},
		{
			"well-known port is omitted", rewritten("shop.example.com", "/a",
				func(r *http.Request) { r.URL.Scheme = "https"; r.URL.Host = ":443" }),
			"https://shop.example.com/a",
		},
		{
			"hostname keeps the request port", rewritten("shop.example.com:8080", "/a",
				func(r *http.Request) { r.URL.Host = "other.example.com" }),
			"http://other.example.com:8080/a",
		},
		{
			"hostname and port", rewritten("shop.example.com", "/a",
				func(r *http.Request) { r.URL.Host = "other.example.com:9000" }),
			"http://other.example.com:9000/a",
		},
		{
			"rewritten path", rewritten("shop.example.com", "/a/b?q=1",
				func(r *http.Request) { r.URL.Path = "/v2/b" }),
			"http://shop.example.com/v2/b?q=1",
		},
		{"ipv6 request host", rewritten("[::1]:8080", "/a", nil), "http://[::1]:8080/a"},
		{"bare ipv6 request host", rewritten("[::1]", "/a", nil), "http://[::1]/a"},
		{"empty path", rewritten("shop.example.com", "/", func(r *http.Request) {
			r.URL.Path = ""
		}), "http://shop.example.com/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, Location(tt.r).String())
		})
	}
	require.Equal(t, "/", Location(nil).String())
}

func TestHandleRedirect(t *testing.T) {
	HandleRedirect(nil, nil)

	// no path configuration: the default redirection status
	r := rewritten("shop.example.com", "/a", nil)
	w := httptest.NewRecorder()
	HandleRedirect(w, r)
	require.Equal(t, DefaultRedirectCode, w.Code)
	require.Equal(t, "http://shop.example.com/a", w.Header().Get("Location"))

	// a redirection response_code is honored, and response_headers applied
	pc := &po.Options{
		ResponseCode:    http.StatusMovedPermanently,
		ResponseHeaders: map[string]string{"X-Redirected-By": "trickster"},
	}
	r = rewritten("shop.example.com", "/a", func(r *http.Request) { r.URL.Scheme = "https" })
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, pc, nil, nil, nil, nil)))
	w = httptest.NewRecorder()
	HandleRedirect(w, r)
	require.Equal(t, http.StatusMovedPermanently, w.Code)
	require.Equal(t, "https://shop.example.com/a", w.Header().Get("Location"))
	require.Equal(t, "trickster", w.Header().Get("X-Redirected-By"))

	// the path's request headers shape the Location: a Host update, under
	// any spelling, is the hostname a rewriter did not set
	pc = &po.Options{RequestHeaders: map[string]string{
		"hOsT": "tenant.example.com", "X-Ignored": "1",
	}}
	r = rewritten("shop.example.com:8080", "/a", nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, pc, nil, nil, nil, nil)))
	w = httptest.NewRecorder()
	HandleRedirect(w, r)
	require.Equal(t, "http://tenant.example.com/a", w.Header().Get("Location"),
		"a set Host replaces the request's, port included")
	require.Empty(t, w.Header().Get("X-Ignored"), "request headers are not response headers")

	// a rewriter's explicit hostname outranks a request Host update
	r = rewritten("shop.example.com", "/a", func(r *http.Request) { r.URL.Host = "named.example.com" })
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, pc, nil, nil, nil, nil)))
	w = httptest.NewRecorder()
	HandleRedirect(w, r)
	require.Equal(t, "http://named.example.com/a", w.Header().Get("Location"))

	// a response_code that is not a redirection falls back to the default
	pc = &po.Options{ResponseCode: http.StatusTeapot}
	r = rewritten("shop.example.com", "/a", nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(nil, pc, nil, nil, nil, nil)))
	w = httptest.NewRecorder()
	HandleRedirect(w, r)
	require.Equal(t, DefaultRedirectCode, w.Code)
}
