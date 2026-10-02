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

package normalize

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize/options"
)

const refused = "<400>"

func opts(dots string, merge bool, slashes string) *options.Options {
	return &options.Options{DotSegments: dots, MergeSlashes: merge, EscapedSlashes: slashes}
}

func apply(t *testing.T, n *Normalizer, target string) (string, *url.URL) {
	t.Helper()
	// parses target as a server parses a request line, then returns the escaped
	// path an origin would receive, or refused
	u, err := url.ParseRequestURI(target)
	if err != nil {
		t.Fatal(err)
	}
	if !n.Normalize(u) {
		return refused, u
	}
	return u.EscapedPath(), u
}

func TestNormalize(t *testing.T) {
	defaults := (*options.Options)(nil)
	tests := []struct {
		name   string
		o      *options.Options
		target string
		want   string
	}{
		// the three bypass probes, under the defaults and under reject
		{"probe literal", defaults, "/public/../admin/x", "/admin/x"},
		{"probe encoded dots", defaults, "/public/%2e%2e/admin/x", "/admin/x"},
		{"probe encoded dots upper", defaults, "/public/%2E%2e/admin/x", "/admin/x"},
		{"probe mixed dots", defaults, "/public/.%2e/admin/x", "/admin/x"},
		{"probe hidden by keep", defaults, "/public//..%2fadmin/x", refused},
		{"probe hidden trailing", defaults, "/public/a%2F../admin", refused},
		{"probe literal reject", opts("reject", false, ""), "/public/../admin/x", refused},
		{"probe encoded reject", opts("reject", false, ""), "/public/%2e%2e/admin/x", refused},
		{"probe hidden reject", opts("reject", false, ""), "/public//..%2fadmin/x", refused},
		{"probe hidden unescape", opts("", false, "unescape"), "/public//..%2fadmin/x", "/public/admin/x"},
		{"probe hidden unescape merge", opts("", true, "unescape"), "/public//..%2fadmin/x", "/admin/x"},
		{"probe hidden reject slashes", opts("", false, "reject"), "/public//..%2fadmin/x", refused},
		{"probe off", opts("off", false, ""), "/public/../admin/x", "/public/../admin/x"},
		// RFC 3986 section 5.2.4 behavior
		{"single dot", defaults, "/a/./b", "/a/b"},
		{"trailing dot", defaults, "/a/b/.", "/a/b/"},
		{"trailing dotdot", defaults, "/a/b/..", "/a/"},
		{"dotdot trailing slash", defaults, "/a/b/../", "/a/"},
		{"above root", defaults, "/../../x", "/x"},
		{"only dotdot", defaults, "/..", "/"},
		{"empty segment kept", defaults, "/a//../b", "/a/b"},
		{"doubled slash kept", defaults, "/a//b/./c", "/a//b/c"},
		{"dotted names untouched", defaults, "/a/.well-known/..b/b../...", "/a/.well-known/..b/b../..."},
		{"query untouched", defaults, "/a/../b?x=/../y", "/b"},
		// merge_slashes
		{"merge", opts("", true, ""), "//a///b//", "/a/b/"},
		{"merge then dots", opts("", true, ""), "/a//../b", "/b"},
		{"merge root", opts("", true, ""), "//", "/"},
		{"merge dots off", opts("off", true, ""), "/a//./b", "/a/./b"},
		// escaped_slashes
		{"keep encoded slash", defaults, "/a/b%2Fc/d", "/a/b%2Fc/d"},
		{"keep with dots", defaults, "/a/x/../b%2Fc", "/a/b%2Fc"},
		{"keep percent preserved", defaults, "/a/./b%2Bc", "/a/b%2Bc"},
		{"keep no merge across escape", opts("", true, ""), "/a/%2F%2Fb//c", "/a/%2F%2Fb/c"},
		{"reject encoded slash", opts("", false, "reject"), "/a/b%2fc", refused},
		{"reject other escapes", opts("", false, "reject"), "/a/b%2Bc", "/a/b%2Bc"},
		{"unescape encoded slash", opts("", false, "unescape"), "/a/b%2Fc", "/a/b/c"},
		{"unescape then merge", opts("", true, "unescape"), "/a/b%2F%2Fc", "/a/b/c"},
		// requests outside path normalization's reach
		{"asterisk", defaults, "*", "*"},
		{"root", defaults, "/", "/"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, u := apply(t, New(test.o), test.target)
			if got != test.want {
				t.Fatalf("%s: got %q; want %q", test.target, got, test.want)
			}
			if got == refused {
				return
			}
			// routing reads Path, so it must decode from what the origin receives
			if dec, err := url.PathUnescape(got); err != nil || dec != u.Path {
				t.Fatalf("%s: Path %q disagrees with forwarded %q", test.target, u.Path, got)
			}
		})
	}
}

func TestNormalizeNoAllocation(t *testing.T) {
	n := New(opts("", true, "reject"))
	for _, target := range []string{"/api/v1/query_range", "/a/b.c/.well-known/x/", "/a/b%2Bc/d"} {
		u, err := url.ParseRequestURI(target)
		if err != nil {
			t.Fatal(err)
		}
		if allocs := testing.AllocsPerRun(100, func() { n.Normalize(u) }); allocs != 0 {
			t.Errorf("%s: %v allocations; want 0", target, allocs)
		}
	}
}

func TestMiddleware(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	h := Middleware(nil, next)
	for target, want := range map[string]int{
		"/public/../admin/x":    http.StatusNoContent,
		"/public//..%2fadmin/x": http.StatusBadRequest,
	} {
		seen = ""
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != want {
			t.Errorf("%s: status %d; want %d", target, w.Code, want)
		}
		if want == http.StatusNoContent && seen != "/admin/x" {
			t.Errorf("%s: routed as %q", target, seen)
		}
		if want == http.StatusBadRequest && seen != "" {
			t.Errorf("%s: refused request reached next", target)
		}
	}
	// with every option off there is nothing to do, so next is used directly
	if h := Middleware(opts("off", false, "keep"), next); h == nil || New(opts("off", false, "keep")).Enabled() {
		t.Error("disabled normalization should return next unwrapped")
	}
	if Middleware(nil, nil) != nil {
		t.Error("a nil next should stay nil")
	}
}

func BenchmarkNormalizeClean(b *testing.B) {
	n := New(nil)
	u := &url.URL{Path: "/api/v1/query_range"}
	b.ReportAllocs()
	for b.Loop() {
		n.Normalize(u)
	}
}

func BenchmarkMiddlewareClean(b *testing.B) {
	h := Middleware(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/query_range?query=up", nil)
	w := httptest.NewRecorder()
	b.ReportAllocs()
	for b.Loop() {
		h.ServeHTTP(w, r)
	}
}

func BenchmarkNormalizeDotSegments(b *testing.B) {
	n := New(nil)
	b.ReportAllocs()
	for b.Loop() {
		u := url.URL{Path: "/public/../admin/x"}
		n.Normalize(&u)
	}
}
