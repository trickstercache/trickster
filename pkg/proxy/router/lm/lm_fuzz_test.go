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

package lm

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
)

// fuzzRoute is one entry in the mixed route table shared by the router under
// test and the naive reference implementation
type fuzzRoute struct {
	matchType matching.PathMatchType
	path      string
	marker    string
	re        *regexp.Regexp
}

var fuzzRoutes = []*fuzzRoute{
	{matching.PathMatchTypeExact, "/api/query", "exact-query", nil},
	{matching.PathMatchTypeExact, "/foo", "exact-foo", nil},
	{matching.PathMatchTypePrefix, "/api/", "prefix-api", nil},
	{matching.PathMatchTypePrefix, "/foo/bar", "prefix-foobar", nil},
	{matching.PathMatchTypeRegex, "^/api/[0-9]+", "regex-api-num", nil},
	{matching.PathMatchTypeRegex, "^/[a-z]+/query", "regex-query", nil},
	{matching.PathMatchTypeRegex, "^/foo/[0-9]+$", "regex-foo-num", nil},
	{matching.PathMatchTypeRegex, "^/z.*", "regex-z", nil},
}

// refMatch is a naive reference implementation of the router's selection
// semantics for GET requests on the global host: exact, then longest prefix,
// then regex longest-pattern-first with config order breaking ties
func refMatch(path string) string {
	for _, fr := range fuzzRoutes {
		if fr.matchType == matching.PathMatchTypeExact && fr.path == path {
			return fr.marker
		}
	}
	var prefixBest *fuzzRoute
	for _, fr := range fuzzRoutes {
		if fr.matchType == matching.PathMatchTypePrefix &&
			strings.HasPrefix(path, fr.path) &&
			(prefixBest == nil || len(fr.path) > len(prefixBest.path)) {
			prefixBest = fr
		}
	}
	if prefixBest != nil {
		return prefixBest.marker
	}
	regexes := make([]*fuzzRoute, 0, len(fuzzRoutes))
	for _, fr := range fuzzRoutes {
		if fr.matchType == matching.PathMatchTypeRegex {
			regexes = append(regexes, fr)
		}
	}
	sort.SliceStable(regexes, func(i, j int) bool {
		return len(regexes[i].path) > len(regexes[j].path)
	})
	for _, fr := range regexes {
		if fr.re.MatchString(path) {
			return fr.marker
		}
	}
	return ""
}

func FuzzHandler(f *testing.F) {
	r := NewRouter().(*lmRouter)
	for _, fr := range fuzzRoutes {
		if fr.matchType == matching.PathMatchTypeRegex {
			fr.re = regexp.MustCompile(fr.path)
		}
		marker := fr.marker
		if err := r.RegisterRoute(fr.path, nil, []string{http.MethodGet},
			fr.matchType, http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, marker, http.StatusOK)
				})); err != nil {
			f.Fatal(err)
		}
	}

	f.Add("/api/query")
	f.Add("/api/42")
	f.Add("/api/42/detail")
	f.Add("/foo")
	f.Add("/foo/bar/baz")
	f.Add("/foo/99")
	f.Add("/zebra")
	f.Add("/abc/query")
	f.Add("/no/match/here")
	f.Add("")
	f.Add("/")
	f.Add("^/api/[0-9]+")
	f.Add("/foo\x00bar")
	f.Add(strings.Repeat("/foo", 512))

	f.Fuzz(func(t *testing.T, path string) {
		req := &http.Request{
			Method: http.MethodGet,
			URL:    &url.URL{Path: path},
		}
		w := httptest.NewRecorder()
		// must never panic
		r.Handler(req).ServeHTTP(w, req)

		expected := refMatch(path)
		if expected == "" {
			if w.Code != http.StatusNotFound {
				t.Fatalf("path %q: expected 404, got %d (%q)",
					path, w.Code, strings.TrimSpace(w.Body.String()))
			}
			return
		}
		if w.Code != http.StatusOK {
			t.Fatalf("path %q: expected 200 (%s), got %d",
				path, expected, w.Code)
		}
		if body := strings.TrimSpace(w.Body.String()); body != expected {
			t.Fatalf("path %q: expected handler %q, got %q",
				path, expected, body)
		}
	})
}
