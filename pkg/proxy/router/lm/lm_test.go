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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/route"
	"github.com/trickstercache/trickster/v2/pkg/testutil/writer"

	"github.com/stretchr/testify/require"
)

const (
	testPathExact1  = "/path/exact"
	testPathExact2  = "/path/exact/2"
	testPathPrefix1 = "/path/prefix"
	testPathPrefix2 = "/path/prefix/2"
)

func TestRegisterRoute(t *testing.T) {
	const testPathExact1 = "/path1/exact"

	r := NewRouter().(*lmRouter)
	r.RegisterRoute(testPathExact1, nil, nil, matching.PathMatchTypeExact, notFoundHandler)

	hrs, ok := r.routes[""]
	if !ok || hrs == nil {
		t.Fatal("expected non-nil route set")
	}
	rll, ok := hrs.ExactMatchRoutes[testPathExact1]
	if !ok || rll == nil {
		t.Fatal("expected non-nil route lookup")
	}

	err := r.RegisterRoute("", nil, nil, matching.PathMatchTypeExact, notFoundHandler)
	if err != errors.ErrInvalidPath {
		t.Fatal("expected error for invalid path")
	}

	err = r.RegisterRoute(testPathPrefix1, nil, []string{"invalidMethod"},
		matching.PathMatchTypeExact, notFoundHandler)
	if err != errors.ErrInvalidMethod {
		t.Fatal("expected error for invalid method")
	}

	err = r.RegisterRoute(testPathPrefix1, nil, []string{http.MethodGet},
		matching.PathMatchTypePrefix, notFoundHandler)
	if err != nil {
		t.Fatal(err)
	}
}

func TestHandler(t *testing.T) {
	r := NewRouter().(*lmRouter)
	r.RegisterRoute(testPathExact1, nil, nil, matching.PathMatchTypeExact, testResponse1Handler)
	r.RegisterRoute(testPathPrefix2, []string{"example.com"}, nil, matching.PathMatchTypePrefix,
		testResponse2Handler)
	r.RegisterRoute(testPathPrefix1, []string{"example.com"}, nil, matching.PathMatchTypePrefix,
		testResponse1Handler)

	req, _ := http.NewRequest(http.MethodGet, testPathExact1, nil)
	req.Host = "example.com:8080"
	h := r.Handler(req)
	w := writer.NewWriter().(*writer.TestResponseWriter)
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok := serveAndVerifyTestResponse1(h, w, req)
	if !ok {
		t.Fatal("expected test response 1 handler")
	}

	// POST request should fail with Method Not Allowed
	req, _ = http.NewRequest(http.MethodPost, testPathExact1, nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyMethodNotAllowed(h, w, req)
	if !ok {
		t.Fatal("expected method not allowed handler")
	}

	// request should fail with 404 Not Found
	req, _ = http.NewRequest(http.MethodPost, testPathExact2, nil)
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyNotFound(h, w, req)
	if !ok {
		t.Fatal("expected 404 not found handler")
	}

	// Prefix Route 1 should pass with test response 1
	req, _ = http.NewRequest(http.MethodGet, testPathPrefix1+"/more/path", nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = serveAndVerifyTestResponse1(h, w, req)
	if !ok {
		t.Fatal("expected test response 1 handler")
	}

	// POST on Prefix Route 1 should fail with Method Not Allowed
	req, _ = http.NewRequest(http.MethodPost, testPathPrefix1+"/more/path", nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyMethodNotAllowed(h, w, req)
	if !ok {
		t.Fatal("expected method not allowed handler")
	}

	r.RegisterRoute(testPathExact2, []string{"example.com"}, nil, matching.PathMatchTypeExact,
		testResponse2Handler)
	req, _ = http.NewRequest(http.MethodGet, testPathExact2, nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyTestResponse2(h, w, req)
	if !ok {
		t.Fatal("expected test response 2 handler")
	}

	r.SetMatchingScheme(0)
	req, _ = http.NewRequest(http.MethodConnect, testPathPrefix1, nil)
	req.Host = "example.com:8080"
	h = r.Handler(req)
	w.Reset()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
	ok = verifyNotFound(h, w, req)
	if !ok {
		t.Fatal("expected 404 not found handler")
	}
}

func TestServeHTTP(t *testing.T) {
	r := NewRouter().(*lmRouter)
	r.RegisterRoute("/", nil, nil, matching.PathMatchTypePrefix, testResponse1Handler)
	w := writer.NewWriter().(*writer.TestResponseWriter)
	req, _ := http.NewRequest(http.MethodGet, testPathPrefix1, nil)
	req.RequestURI = "*"
	r.ServeHTTP(w, req)
	ok := verifyBadRequest(w)
	if !ok {
		t.Fatal("expected 400 bad request handler")
	}
	req, _ = http.NewRequest(http.MethodGet, testPathPrefix1, nil)
	w.Reset()
	r.ServeHTTP(w, req)
	ok = verifyTestResponse1(w)
	if !ok {
		t.Fatal("expected test response 1 handler")
	}
}

func verifyNotFound(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request,
) bool {
	h.ServeHTTP(w, r)
	return w.StatusCode == http.StatusNotFound
}

func verifyMethodNotAllowed(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request,
) bool {
	h.ServeHTTP(w, r)
	return w.StatusCode == http.StatusMethodNotAllowed
}

const (
	testResponse1Text = "test response 1"
	testResponse2Text = "test response 2"
)

func testResponse1(w http.ResponseWriter, r *http.Request) {
	http.Error(w, testResponse1Text, http.StatusOK)
}

var testResponse1Handler = http.HandlerFunc(testResponse1)

func serveAndVerifyTestResponse1(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request,
) bool {
	h.ServeHTTP(w, r)
	return verifyTestResponse1(w)
}

func verifyTestResponse1(w *writer.TestResponseWriter) bool {
	return w.StatusCode == http.StatusOK &&
		strings.TrimSpace(string(w.Bytes)) == testResponse1Text
}

func testResponse2(w http.ResponseWriter, r *http.Request) {
	http.Error(w, testResponse2Text, http.StatusOK)
}

var testResponse2Handler = http.HandlerFunc(testResponse2)

func verifyTestResponse2(h http.Handler, w *writer.TestResponseWriter,
	r *http.Request,
) bool {
	h.ServeHTTP(w, r)
	return w.StatusCode == http.StatusOK &&
		strings.TrimSpace(string(w.Bytes)) == testResponse2Text
}

func verifyBadRequest(w *writer.TestResponseWriter) bool {
	return w.StatusCode == http.StatusBadRequest
}

func TestRegisterRouteRegex(t *testing.T) {
	r := NewRouter().(*lmRouter)

	// an invalid pattern is a registration error
	err := r.RegisterRoute("^/bad/(unclosed", nil, nil,
		matching.PathMatchTypeRegex, testResponse1Handler)
	if err == nil {
		t.Fatal("expected error for invalid regex pattern")
	}

	// an unknown match type is a registration error
	err = r.RegisterRoute("/path", nil, nil, matching.PathMatchType(27),
		testResponse1Handler)
	if err != errors.ErrInvalidMatchType {
		t.Fatal("expected error for invalid match type")
	}

	err = r.RegisterRoute("^/results/[0-9]+", nil, nil,
		matching.PathMatchTypeRegex, testResponse1Handler)
	if err != nil {
		t.Fatal(err)
	}
	hrs := r.routes[""]
	require.NotNil(t, hrs)
	require.Equal(t, 1, len(hrs.RegexMatchRoutes))
	rrs := hrs.RegexMatchRoutes[0]
	require.Equal(t, "^/results/[0-9]+", rrs.Pattern)
	require.Equal(t, len(rrs.Pattern), rrs.PatternLen)
	require.NotNil(t, rrs.Regexp)
	// implicit HEAD-for-GET
	require.NotNil(t, rrs.RoutesByMethod[http.MethodGet])
	require.NotNil(t, rrs.RoutesByMethod[http.MethodHead])

	// re-registering the same pattern reuses the set rather than appending
	err = r.RegisterRoute("^/results/[0-9]+", nil, []string{http.MethodPost},
		matching.PathMatchTypeRegex, testResponse2Handler)
	if err != nil {
		t.Fatal(err)
	}
	require.Equal(t, 1, len(hrs.RegexMatchRoutes))
	require.NotNil(t, rrs.RoutesByMethod[http.MethodPost])
}

func TestHandlerRegex(t *testing.T) {
	r := NewRouter().(*lmRouter)
	// all three tiers on one host, plus a host-specific regex
	r.RegisterRoute("/api/exact", nil, nil, matching.PathMatchTypeExact,
		testResponse1Handler)
	r.RegisterRoute("/api/prefix", nil, nil, matching.PathMatchTypePrefix,
		testResponse1Handler)
	r.RegisterRoute("^/api/[0-9]+", nil, nil, matching.PathMatchTypeRegex,
		testResponse1Handler)
	r.RegisterRoute("^/api/[0-9]+/detail", nil, nil, matching.PathMatchTypeRegex,
		testResponse2Handler)
	r.RegisterRoute("^/hosted/[0-9]+", []string{"example.com"}, nil,
		matching.PathMatchTypeRegex, testResponse2Handler)

	// regex sets are sorted longest pattern first
	prs := r.routes[""].RegexMatchRoutes
	require.Equal(t, 2, len(prs))
	require.Equal(t, "^/api/[0-9]+/detail", prs[0].Pattern)

	w := writer.NewWriter().(*writer.TestResponseWriter)

	// classic tiers still win over regex
	req, _ := http.NewRequest(http.MethodGet, "/api/exact", nil)
	if !serveAndVerifyTestResponse1(r.Handler(req), w, req) {
		t.Fatal("expected exact-tier test response 1 handler")
	}
	req, _ = http.NewRequest(http.MethodGet, "/api/prefix/42", nil)
	w.Reset()
	if !serveAndVerifyTestResponse1(r.Handler(req), w, req) {
		t.Fatal("expected prefix-tier test response 1 handler")
	}

	// longest pattern evaluated first
	req, _ = http.NewRequest(http.MethodGet, "/api/42/detail", nil)
	w.Reset()
	if !verifyTestResponse2(r.Handler(req), w, req) {
		t.Fatal("expected longest-pattern test response 2 handler")
	}
	req, _ = http.NewRequest(http.MethodGet, "/api/42", nil)
	w.Reset()
	if !serveAndVerifyTestResponse1(r.Handler(req), w, req) {
		t.Fatal("expected test response 1 handler")
	}

	// implicit HEAD, and 405 for an unregistered method
	req, _ = http.NewRequest(http.MethodHead, "/api/42", nil)
	w.Reset()
	r.Handler(req).ServeHTTP(w, req)
	if w.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for implicit HEAD, got %d", w.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, "/api/42", nil)
	w.Reset()
	if !verifyMethodNotAllowed(r.Handler(req), w, req) {
		t.Fatal("expected method not allowed handler")
	}

	// host-specific regex is matched in the host pass, with global fallback
	req, _ = http.NewRequest(http.MethodGet, "/hosted/1", nil)
	req.Host = "example.com:8080"
	w.Reset()
	if !verifyTestResponse2(r.Handler(req), w, req) {
		t.Fatal("expected host-specific test response 2 handler")
	}
	req, _ = http.NewRequest(http.MethodGet, "/api/42", nil)
	req.Host = "example.com:8080"
	w.Reset()
	if !serveAndVerifyTestResponse1(r.Handler(req), w, req) {
		t.Fatal("expected global regex fallback test response 1 handler")
	}

	// a scheme without MatchPathRegex never evaluates the regex tier
	r.SetMatchingScheme(router.MatchHostname | router.MatchPathPrefix)
	req, _ = http.NewRequest(http.MethodGet, "/api/42", nil)
	w.Reset()
	if !verifyNotFound(r.Handler(req), w, req) {
		t.Fatal("expected 404 with regex matching disabled")
	}
}

// TestGatewayMixedTierPrecedence verifies the routing semantics the Kubernetes
// Ingress/Gateway controller relies upon: a kube-generated backend maps
// HTTPRoute Exact -> exact, Prefix -> prefix and RegularExpression -> regex
// paths on the same backend, and the router's classic-first evaluation order
// implements Gateway API precedence (Exact > Prefix > RegularExpression)
func TestGatewayMixedTierPrecedence(t *testing.T) {
	mkHandler := func(body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, body, http.StatusOK)
		})
	}
	serve := func(r *lmRouter, path string) string {
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		w := writer.NewWriter().(*writer.TestResponseWriter)
		r.Handler(req).ServeHTTP(w, req)
		return strings.TrimSpace(string(w.Bytes))
	}

	r := NewRouter().(*lmRouter)
	r.RegisterRoute("/api/query", nil, nil, matching.PathMatchTypeExact,
		mkHandler("exact"))
	r.RegisterRoute("/api/", nil, nil, matching.PathMatchTypePrefix,
		mkHandler("prefix"))
	r.RegisterRoute("^/[a-z]+/query", nil, nil, matching.PathMatchTypeRegex,
		mkHandler("regex"))

	if body := serve(r, "/api/query"); body != "exact" {
		t.Fatalf("expected exact to beat prefix and regex, got %q", body)
	}
	if body := serve(r, "/api/other"); body != "prefix" {
		t.Fatalf("expected prefix to beat regex, got %q", body)
	}
	if body := serve(r, "/other/query"); body != "regex" {
		t.Fatalf("expected regex after classic tiers miss, got %q", body)
	}

	// regex precedence is deterministic: longest pattern first, and equal
	// length patterns evaluate in registration (config) order, giving the
	// controller a stable lever over relative regex priority
	r2 := NewRouter().(*lmRouter)
	r2.RegisterRoute("^/tie/[ab]+", nil, nil, matching.PathMatchTypeRegex,
		mkHandler("first"))
	r2.RegisterRoute("^/tie/[ba]+", nil, nil, matching.PathMatchTypeRegex,
		mkHandler("second"))
	if body := serve(r2, "/tie/ab"); body != "first" {
		t.Fatalf("expected registration order to break the tie, got %q", body)
	}

	r3 := NewRouter().(*lmRouter)
	r3.RegisterRoute("^/tie/[ba]+", nil, nil, matching.PathMatchTypeRegex,
		mkHandler("second"))
	r3.RegisterRoute("^/tie/[ab]+", nil, nil, matching.PathMatchTypeRegex,
		mkHandler("first"))
	if body := serve(r3, "/tie/ab"); body != "second" {
		t.Fatalf("expected reversed registration order to flip the winner, got %q",
			body)
	}
}

func Test_lmRouter(t *testing.T) {
	l := lmRouter{
		routes: map[string]*route.HostRouteSet{
			"foo": {
				PrefixMatchRoutes: []*route.PrefixRouteSet{
					{Path: "/baz", PathLen: 3},
					{Path: "/quxx", PathLen: 4},
					{Path: "/ab", PathLen: 2},
				},
				RegexMatchRoutes: []*route.RegexRouteSet{
					{Pattern: "^/[ab]", PatternLen: 5},
					{Pattern: "^/long/.*", PatternLen: 9},
					{Pattern: "^/[ba]", PatternLen: 5},
				},
			},
		},
	}
	// sort the routes (by path length)
	l.sort()
	route := l.routes["foo"]
	require.Equal(t, 3, len(route.PrefixMatchRoutes))
	prefixes := route.PrefixMatchRoutes
	// check the order of sorted routes
	require.Equal(t, "/quxx", prefixes[0].Path)
	require.Equal(t, 4, prefixes[0].PathLen)
	require.Equal(t, "/baz", prefixes[1].Path)
	require.Equal(t, 3, prefixes[1].PathLen)
	require.Equal(t, "/ab", prefixes[2].Path)
	require.Equal(t, 2, prefixes[2].PathLen)

	// regex sets sort longest pattern first, with registration order
	// preserved for equal-length patterns (stable)
	regexes := route.RegexMatchRoutes
	require.Equal(t, 3, len(regexes))
	require.Equal(t, "^/long/.*", regexes[0].Pattern)
	require.Equal(t, "^/[ab]", regexes[1].Pattern)
	require.Equal(t, "^/[ba]", regexes[2].Pattern)
}
