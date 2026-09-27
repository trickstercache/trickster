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

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
)

func mustList(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	list, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("next"))
	})
	deny := mustList(t, ipacl.Options{Allow: []string{"10.0.0.0/8"}, Status: http.StatusTooManyRequests})
	allow := mustList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.RemoteAddr = "192.0.2.9:1"
	w := httptest.NewRecorder()
	Middleware(nil, "/trickster/ready", next).ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "next" {
		t.Fatalf("nil list = %d %q", w.Code, w.Body.String())
	}
	if Middleware(deny, "", nil) != nil {
		t.Fatal("nil next must pass through")
	}

	t.Run("deny", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		Middleware(deny, "", next).ServeHTTP(w, req)
		if w.Code != http.StatusTooManyRequests || w.Body.Len() != 0 {
			t.Fatalf("deny = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("allow", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		Middleware(allow, "", next).ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "next" {
			t.Fatalf("allow = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("ready path", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/custom-ready", nil)
		req.RemoteAddr = "192.0.2.9:1"
		w := httptest.NewRecorder()
		Middleware(deny, "/custom-ready", next).ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Body.String() != "next" {
			t.Fatalf("ready = %d %q", w.Code, w.Body.String())
		}
	})

	t.Run("invalid address", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "not-an-address"
		w := httptest.NewRecorder()
		Middleware(allow, "", next).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("invalid = %d", w.Code)
		}
	})

	t.Run("client ip", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "10.1.1.1:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "192.0.2.9"))
		w := httptest.NewRecorder()
		Middleware(allow, "", next).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("client ip = %d", w.Code)
		}
	})

	t.Run("peer", func(t *testing.T) {
		peer := mustList(t, ipacl.Options{Allow: []string{"10.1.1.1"}, Source: "peer"})
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "10.1.1.1:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "192.0.2.9"))
		w := httptest.NewRecorder()
		Middleware(peer, "", next).ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("peer allow = %d", w.Code)
		}
		req = httptest.NewRequest(http.MethodGet, "/api", nil)
		req.RemoteAddr = "198.51.100.8:9"
		req = req.WithContext(tctx.WithClientIP(req.Context(), "10.1.1.1"))
		w = httptest.NewRecorder()
		Middleware(peer, "", next).ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("peer deny = %d; client ip must not be used", w.Code)
		}
	})
}
