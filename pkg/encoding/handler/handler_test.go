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

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func emptyHandler(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte("trickster"))
}

func TestHandleCompression(t *testing.T) {
	f := HandleCompression(http.HandlerFunc(emptyHandler),
		sets.New([]string{headers.ValueTextPlain}))
	r, _ := http.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil)
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Body.String() != "trickster" {
		t.Error("writer data mismatch")
	}
	r.Header.Add(headers.NameCacheControl, "no-transform")
	w = httptest.NewRecorder()
	f.ServeHTTP(w, r)
	if w.Body.String() != "trickster" {
		t.Error("writer data mismatch")
	}
}

func TestHandleCompressionSupportsHijacker(t *testing.T) {
	var supportsHijacker bool
	f := HandleCompression(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, supportsHijacker = w.(http.Hijacker)
	}), nil)
	r := httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil)
	f.ServeHTTP(httptest.NewRecorder(), r)

	if !supportsHijacker {
		t.Error("compression response writer does not implement http.Hijacker")
	}
}

func TestHandleCompressionHonorsWeights(t *testing.T) {
	var upstream string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// what an origin would be asked for, were this request proxied to one
		if ep := profile.FromContext(r.Context()); ep != nil {
			upstream = ep.SupportedHeaderVal
		}
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Write([]byte(strings.Repeat("trickster ", 100)))
	})
	h := HandleCompression(next, sets.New([]string{headers.ValueTextPlain}))
	tests := []struct {
		accept, encoding, upstream string
	}{
		// unweighted, Trickster's own preference applies
		{"gzip, deflate, br, zstd", "zstd", "zstd, br, gzip, deflate"},
		{"gzip;q=1.0, zstd;q=0.5", "gzip", "gzip, zstd;q=0.5"},
		{"zstd;q=0, BR;q=0.3, deflate;q=0.4", "deflate", "deflate;q=0.4, br;q=0.3"},
		{"gzip;q=0, identity", "", ""},
		{"*", "zstd", "zstd, br, gzip, deflate"},
		{"gzip;q=0, *;q=0.5", "zstd", "zstd;q=0.5, br;q=0.5, deflate;q=0.5"},
		{"identity;q=1, gzip;q=0.5", "", ""},
		{"identity;q=0, deflate;q=0.1", "deflate", "deflate;q=0.1"},
		// nothing acceptable is left, and identity is sent regardless, as other servers do
		{"identity;q=0", "", ""},
		{"", "", ""},
	}
	for _, test := range tests {
		upstream = ""
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		if test.accept != "" {
			r.Header.Set(headers.NameAcceptEncoding, test.accept)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get(headers.NameContentEncoding); got != test.encoding {
			t.Errorf("Accept-Encoding %q: expected %q got %q", test.accept, test.encoding, got)
		}
		if upstream != test.upstream {
			t.Errorf("Accept-Encoding %q: expected upstream %q got %q", test.accept, test.upstream, upstream)
		}
	}
}
