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
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/route"
	"github.com/trickstercache/trickster/v2/pkg/testutil/writer"

	"github.com/stretchr/testify/require"
)

const notFoundText = "404 page not found"

func predicates(t *testing.T, hdrs, queries [][3]string) *reqmatching.Predicates {
	t.Helper()
	p := &reqmatching.Predicates{}
	for _, h := range hdrs {
		hp, err := reqmatching.NewHeaderPredicate(h[0], h[1], h[2] == "regex")
		require.NoError(t, err)
		p.Headers = append(p.Headers, hp)
	}
	for _, q := range queries {
		qp, err := reqmatching.NewQueryPredicate(q[0], q[1], q[2] == "regex")
		require.NoError(t, err)
		p.Queries = append(p.Queries, qp)
	}
	return p
}

func header(name, value string) [][3]string { return [][3]string{{name, value, ""}} }

func serveWith(t *testing.T, r *lmRouter, method, host, path string, hdr http.Header) (string, int) {
	t.Helper()
	req, err := http.NewRequest(method, path, nil)
	require.NoError(t, err)
	req.Host = host
	maps.Copy(req.Header, hdr)
	w := writer.NewWriter().(*writer.TestResponseWriter)
	r.Handler(req).ServeHTTP(w, req)
	return strings.TrimSpace(string(w.Bytes)), w.StatusCode
}

func TestPredicatedExactRoutes(t *testing.T) {
	r := NewRouter().(*lmRouter)
	// conditioned routes are registered before the unconditioned one, in
	// the order they should be tried
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse1Handler,
	}))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, [][3]string{{"X-Tenant", "^b.*$", "regex"}}, [][3]string{{"v", "2", ""}}), Handler: testResponse2Handler,
	}))
	require.NoError(t, r.RegisterRoute(testPathExact1, nil, nil, matching.PathMatchTypeExact,
		testResponse3Handler))

	got, _ := serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse1Text, got, "exact header value")
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact1+"?v=2", http.Header{"X-Tenant": {"bee"}})
	require.Equal(t, testResponse2Text, got, "regex header and query")
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact1+"?v=3", http.Header{"X-Tenant": {"bee"}})
	require.Equal(t, testResponse3Text, got, "query miss falls to the unconditioned route")
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact1, nil)
	require.Equal(t, testResponse3Text, got, "no headers selects the unconditioned route")
	// HEAD follows GET, candidates included
	got, _ = serveWith(t, r, http.MethodHead, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse1Text, got)
	// the path exists but the method does not: 405, not a fallback
	_, code := serveWith(t, r, http.MethodPost, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, http.StatusMethodNotAllowed, code)
}

func TestPredicateMissContinuesToLessSpecificTiers(t *testing.T) {
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, Hosts: []string{testHostExact}, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse1Handler,
	}))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathPrefix1, Hosts: []string{testHostExact}, Methods: nil, MatchType: matching.PathMatchTypePrefix,
		Predicates: predicates(t, header("X-Tenant", "b"), nil), Handler: testResponse2Handler,
	}))
	require.NoError(t, r.RegisterRoute("/", []string{testHostWildcard}, nil,
		matching.PathMatchTypePrefix, testResponse3Handler))

	// exact candidate misses, prefix candidate misses, wildcard host catches it
	got, _ := serveWith(t, r, http.MethodGet, testHostExact, testPathExact1, nil)
	require.Equal(t, testResponse3Text, got)
	got, _ = serveWith(t, r, http.MethodGet, testHostExact, testPathPrefix1+"/x", http.Header{"X-Tenant": {"b"}})
	require.Equal(t, testResponse2Text, got)
	got, _ = serveWith(t, r, http.MethodGet, testHostExact, testPathPrefix1+"/x", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse3Text, got)
	// with nothing below, an all-candidate miss is a 404
	r2 := NewRouter().(*lmRouter)
	require.NoError(t, r2.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse1Handler,
	}))
	got, code := serveWith(t, r2, http.MethodGet, "", testPathExact1, nil)
	require.Equal(t, http.StatusNotFound, code)
	require.Equal(t, notFoundText, got)
}

func TestPredicatedPrefixAndRegexTiers(t *testing.T) {
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: "/api/v1", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypePrefix,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse1Handler,
	}))
	require.NoError(t, r.RegisterRoute("/api", nil, nil, matching.PathMatchTypePrefix, testResponse2Handler))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: "^/re/[0-9]+", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeRegex,
		Predicates: predicates(t, nil, [][3]string{{"v", "1", ""}}), Handler: testResponse1Handler,
	}))
	require.NoError(t, r.RegisterRoute("^/re/.*", nil, nil, matching.PathMatchTypeRegex, testResponse3Handler))

	got, _ := serveWith(t, r, http.MethodGet, "", "/api/v1/x", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse1Text, got)
	got, _ = serveWith(t, r, http.MethodGet, "", "/api/v1/x", nil)
	require.Equal(t, testResponse2Text, got, "a longer prefix miss falls to the shorter prefix")
	got, _ = serveWith(t, r, http.MethodGet, "", "/re/42?v=1", nil)
	require.Equal(t, testResponse1Text, got)
	got, _ = serveWith(t, r, http.MethodGet, "", "/re/42", nil)
	require.Equal(t, testResponse3Text, got, "a regex candidate miss falls to the next pattern")
}

func TestPredicateRegistrationOrder(t *testing.T) {
	// an unconditioned route registered first always matches, so a
	// conditioned route registered after it is never reached
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRoute(testPathExact1, nil, nil, matching.PathMatchTypeExact, testResponse3Handler))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse1Handler,
	}))
	got, _ := serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse3Text, got)

	// an unconditioned route replaces an unconditioned one, as it always has
	require.NoError(t, r.RegisterRoute(testPathExact2, nil, nil, matching.PathMatchTypeExact, testResponse1Handler))
	require.NoError(t, r.RegisterRoute(testPathExact2, nil, nil, matching.PathMatchTypeExact, testResponse2Handler))
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact2, nil)
	require.Equal(t, testResponse2Text, got)

	// empty predicates register an unconditioned route
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathPrefix1, Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: &reqmatching.Predicates{}, Handler: testResponse1Handler,
	}))
	require.Nil(t, r.routes[""].ExactMatchRoutes[testPathPrefix1][http.MethodGet].Candidates)
	require.Equal(t, errors.ErrInvalidPath, r.RegisterRouteSpec(route.Spec{
		Path: "", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: nil, Handler: testResponse1Handler,
	}))
}

func TestSegmentMatch(t *testing.T) {
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRoute("/foo", nil, nil, matching.PathMatchTypeSegment, testResponse1Handler))
	require.NoError(t, r.RegisterRoute("/bar/", nil, nil, matching.PathMatchTypeSegment, testResponse2Handler))
	require.NoError(t, r.RegisterRoute("/", nil, nil, matching.PathMatchTypePrefix, testResponse3Handler))
	for path, want := range map[string]string{
		"/foo":        testResponse1Text,
		"/foo/":       testResponse1Text,
		"/foo/bar":    testResponse1Text,
		"/foobar":     testResponse3Text,
		"/bar/x":      testResponse2Text,
		"/bar":        testResponse3Text,
		"/barbecue/x": testResponse3Text,
	} {
		got, _ := serveWith(t, r, http.MethodGet, "", path, nil)
		require.Equal(t, want, got, path)
	}
	require.Equal(t, errors.ErrInvalidMatchType, r.RegisterRoute("/x", nil, nil, matching.PathMatchType(99),
		testResponse1Handler))
}

func TestAnyDepthWildcardHosts(t *testing.T) {
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{testHostExact}, nil,
		matching.PathMatchTypeExact, testResponse1Handler))
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{testHostWildcard}, nil,
		matching.PathMatchTypeExact, testResponse2Handler))
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{"**.example.com"}, nil,
		matching.PathMatchTypeExact, testResponse3Handler))
	require.NoError(t, r.RegisterRoute(testPathExact2, []string{"**.com"}, nil,
		matching.PathMatchTypeExact, testResponse2Handler))
	require.NoError(t, r.RegisterRoute(testPathExact2, nil, nil,
		matching.PathMatchTypeExact, testResponse3Handler))

	got := serveHost(t, r, testHostExact, testPathExact1)
	require.Equal(t, testResponse1Text, got, "exact host first")
	got = serveHost(t, r, "www.example.com", testPathExact1)
	require.Equal(t, testResponse2Text, got, "one-label wildcard before any-depth at the same boundary")
	got = serveHost(t, r, "deep.www.example.com", testPathExact1)
	require.Equal(t, testResponse3Text, got, "any-depth wildcard covers nested labels")
	got = serveHost(t, r, "a.b.c.d.example.com", testPathExact1)
	require.Equal(t, testResponse3Text, got)
	got = serveHost(t, r, "example.com", testPathExact1)
	require.Equal(t, notFoundText, got, "a wildcard needs at least one label")
	got = serveHost(t, r, "deep.www.example.com", testPathExact2)
	require.Equal(t, testResponse2Text, got, "an outer boundary wildcard beats the hostless tier")
	got = serveHost(t, r, "other.org", testPathExact2)
	require.Equal(t, testResponse3Text, got, "no wildcard covers, hostless tier")
	got = serveHost(t, r, "localhost", testPathExact2)
	require.Equal(t, testResponse3Text, got)
}

func TestRegisterRouteInvalidAnyDepthHosts(t *testing.T) {
	r := NewRouter().(*lmRouter)
	for _, host := range []string{"**.", "**", "***.example.com", "a.**.example.com", "**.*.example.com"} {
		err := r.RegisterRoute(testPathExact1, []string{host}, nil,
			matching.PathMatchTypeExact, testResponse1Handler)
		require.Equal(t, errors.ErrInvalidHost, err, host)
	}
	require.Empty(t, r.anyDepth)
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{" **.Example.COM. "}, nil,
		matching.PathMatchTypeExact, testResponse1Handler))
	require.Contains(t, r.anyDepth, "example.com")
}

func TestSegmentHostTiers(t *testing.T) {
	// a host with a segment route runs the boundary loop for every prefix
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRoute("/seg", nil, nil, matching.PathMatchTypeSegment, testResponse1Handler))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: "/cond", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeSegment,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse2Handler,
	}))
	require.NoError(t, r.RegisterRoute("/plain/", nil, nil, matching.PathMatchTypePrefix, testResponse3Handler))
	require.True(t, r.routes[""].HasSegments)

	got, _ := serveWith(t, r, http.MethodGet, "", "/cond/x", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse2Text, got, "conditioned segment route")
	got, code := serveWith(t, r, http.MethodGet, "", "/cond/x", nil)
	require.Equal(t, http.StatusNotFound, code, "candidate miss with nothing below")
	require.Equal(t, notFoundText, got)
	got, _ = serveWith(t, r, http.MethodGet, "", "/plain/x", nil)
	require.Equal(t, testResponse3Text, got, "plain prefix on a segment host")
	_, code = serveWith(t, r, http.MethodPost, "", "/seg/x", nil)
	require.Equal(t, http.StatusMethodNotAllowed, code)
	_, code = serveWith(t, r, http.MethodGet, "", "/segment", nil)
	require.Equal(t, http.StatusNotFound, code, "no prefix matches on a segment host")
}

func TestCandidateOrder(t *testing.T) {
	// candidates are tried in ascending order whatever their registration
	// order; the unconditioned route ranks last by its order, not its position
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, MatchType: matching.PathMatchTypeExact,
		Order: 9, Handler: testResponse3Handler,
	}))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Order: 5, Handler: testResponse2Handler,
	}))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, [][3]string{{"X-Tenant", "^z", "regex"}}, nil), Order: 1, Handler: testResponse1Handler,
	}))
	got, _ := serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"zed"}})
	require.Equal(t, testResponse1Text, got, "the lowest order wins")
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse2Text, got)
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"b"}})
	require.Equal(t, testResponse3Text, got, "the unconditioned route still ends the search")
	slot := r.routes[""].ExactMatchRoutes[testPathExact1][http.MethodGet]
	require.Len(t, slot.Candidates, 3)
	require.Equal(t, []int{1, 5, 9}, []int{slot.Candidates[0].Order, slot.Candidates[1].Order, slot.Candidates[2].Order})
	// equal orders keep registration order
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path: testPathExact1, MatchType: matching.PathMatchTypeExact,
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Order: 5, Handler: testResponse3Handler,
	}))
	got, _ = serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse2Text, got, "the earlier registration of an equal order is tried first")
	require.Len(t, slot.Candidates, 4)
}

func TestDeclaredHEADSupersedesImplicitHEAD(t *testing.T) {
	// A registration naming HEAD supersedes the implicit HEAD a GET registration
	// contributes, in either order, conditioned or not
	for _, headFirst := range []bool{false, true} {
		r := NewRouter().(*lmRouter)
		declareHEAD := func() {
			require.NoError(t, r.RegisterRoute(testPathExact1, nil, []string{http.MethodHead},
				matching.PathMatchTypeExact, testResponse3Handler))
		}
		declareGET := func() {
			require.NoError(t, r.RegisterRouteSpec(route.Spec{
				Path:      testPathExact1,
				MatchType: matching.PathMatchTypeExact, Methods: []string{http.MethodGet},
				Predicates: predicates(t, header("X-Tenant", "a"), nil),
				Handler:    testResponse1Handler,
			}))
		}
		if headFirst {
			declareHEAD()
			declareGET()
		} else {
			declareGET()
			declareHEAD()
		}
		got, _ := serveWith(t, r, http.MethodGet, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
		require.Equal(t, testResponse1Text, got, "the conditioned GET still answers GET")
		got, _ = serveWith(t, r, http.MethodHead, "", testPathExact1, http.Header{"X-Tenant": {"a"}})
		require.Equal(t, testResponse3Text, got, "the declared HEAD route answers HEAD")
		slot := r.routes[""].ExactMatchRoutes[testPathExact1][http.MethodHead]
		require.Nil(t, slot.Candidates, "the implicit candidate is gone, not merely outranked")
	}
}

func TestImplicitHEADSurvivesWithoutADeclaredHEAD(t *testing.T) {
	// HEAD still follows GET where nothing declares HEAD, conditioned or not
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRoute(testPathExact1, nil, []string{http.MethodGet},
		matching.PathMatchTypeExact, testResponse1Handler))
	got, _ := serveWith(t, r, http.MethodHead, "", testPathExact1, nil)
	require.Equal(t, testResponse1Text, got)

	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path:      testPathExact2,
		MatchType: matching.PathMatchTypeExact, Methods: []string{http.MethodGet},
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse2Handler,
	}))
	got, _ = serveWith(t, r, http.MethodHead, "", testPathExact2, http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse2Text, got)
	_, code := serveWith(t, r, http.MethodHead, "", testPathExact2, nil)
	require.Equal(t, http.StatusNotFound, code, "the condition gates the implicit HEAD too")
}

func TestSegmentAndPrefixShareOnePath(t *testing.T) {
	// A segment route and a plain prefix on one path keep their own boundary
	// requirements, whichever registers first
	for _, segmentFirst := range []bool{false, true} {
		r := NewRouter().(*lmRouter)
		declarePrefix := func() {
			require.NoError(t, r.RegisterRoute("/foo", nil, []string{http.MethodGet},
				matching.PathMatchTypePrefix, testResponse1Handler))
		}
		declareSegment := func() {
			require.NoError(t, r.RegisterRoute("/foo", nil, []string{http.MethodPost},
				matching.PathMatchTypeSegment, testResponse2Handler))
		}
		if segmentFirst {
			declareSegment()
			declarePrefix()
		} else {
			declarePrefix()
			declareSegment()
		}
		for _, tt := range []struct {
			method, path, want string
		}{
			{http.MethodGet, "/foo", testResponse1Text},
			{http.MethodPost, "/foo", testResponse2Text},
			{http.MethodGet, "/foo/bar", testResponse1Text},
			{http.MethodPost, "/foo/bar", testResponse2Text},
			{http.MethodGet, "/foobar", testResponse1Text},
		} {
			got, _ := serveWith(t, r, tt.method, "", tt.path, nil)
			require.Equal(t, tt.want, got, "%s %s", tt.method, tt.path)
		}
		// /foobar is not on a boundary, so only the plain prefix is eligible
		// there; the path exists and POST is not allowed on it
		_, code := serveWith(t, r, http.MethodPost, "", "/foobar", nil)
		require.Equal(t, http.StatusMethodNotAllowed, code)
	}
}

func TestSegmentAndPrefixShareOnePathWithPredicates(t *testing.T) {
	// Conditioned and plain routes of both boundary kinds resolve on one path
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path:      "/foo",
		MatchType: matching.PathMatchTypeSegment, Methods: []string{http.MethodGet},
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Order: 1,
		Handler: testResponse1Handler,
	}))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path:      "/foo",
		MatchType: matching.PathMatchTypePrefix, Methods: []string{http.MethodGet},
		Order: 2, Handler: testResponse2Handler,
	}))
	require.NoError(t, r.RegisterRouteSpec(route.Spec{
		Path:      "/foo",
		MatchType: matching.PathMatchTypeSegment, Methods: []string{http.MethodPost},
		Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: testResponse3Handler,
	}))

	got, _ := serveWith(t, r, http.MethodGet, "", "/foo/bar", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse1Text, got, "the conditioned segment route ranks first")
	got, _ = serveWith(t, r, http.MethodGet, "", "/foo/bar", nil)
	require.Equal(t, testResponse2Text, got, "a condition miss falls to the plain prefix")
	got, _ = serveWith(t, r, http.MethodGet, "", "/foobar", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse2Text, got, "off a boundary the segment route is not eligible")
	// POST has only a segment candidate, so off a boundary the path exists
	// through the plain GET route and POST is not allowed on it
	_, code := serveWith(t, r, http.MethodPost, "", "/foobar", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, http.StatusMethodNotAllowed, code)
	got, _ = serveWith(t, r, http.MethodPost, "", "/foo/bar", http.Header{"X-Tenant": {"a"}})
	require.Equal(t, testResponse3Text, got)
}

func TestTrailingDotHostnamesMatch(t *testing.T) {
	// A request naming a fully qualified hostname reaches the route registered
	// for it, at every wildcard depth
	r := NewRouter().(*lmRouter)
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{testHostExact}, nil,
		matching.PathMatchTypeExact, testResponse1Handler))
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{testHostWildcard}, nil,
		matching.PathMatchTypeExact, testResponse2Handler))
	require.NoError(t, r.RegisterRoute(testPathExact1, []string{"**.example.com"}, nil,
		matching.PathMatchTypeExact, testResponse3Handler))
	for host, want := range map[string]string{
		"api.example.com":            testResponse1Text,
		"api.example.com.":           testResponse1Text,
		"API.Example.COM.:8480":      testResponse1Text,
		"www.example.com.":           testResponse2Text,
		"deep.www.example.com.":      testResponse3Text,
		"deep.www.example.com.:8080": testResponse3Text,
	} {
		require.Equal(t, want, serveHost(t, r, host, testPathExact1), host)
	}
	// a host configured fully qualified normalizes to the same key
	require.NoError(t, r.RegisterRoute(testPathExact2, []string{" Other.Example.ORG. "}, nil,
		matching.PathMatchTypeExact, testResponse1Handler))
	require.Equal(t, testResponse1Text, serveHost(t, r, "other.example.org.", testPathExact2))
	require.Equal(t, testResponse1Text, serveHost(t, r, "other.example.org", testPathExact2))
}

func TestSegmentAndPrefixShareOnePathAndMethod(t *testing.T) {
	// two unconditioned routes differing only in boundary kind are both kept,
	// and equal ranks resolve in registration order
	slot := func(r *lmRouter) *route.Route {
		return r.routes[""].PrefixMatchRoutesLkp["/foo"].RoutesByMethod[http.MethodGet]
	}
	declare := func(r *lmRouter, mt matching.PathMatchType, h http.Handler) {
		require.NoError(t, r.RegisterRoute("/foo", nil, []string{http.MethodGet}, mt, h))
	}

	// the segment route registered first answers on a boundary, and the plain
	// prefix behind it answers off one
	r := NewRouter().(*lmRouter)
	declare(r, matching.PathMatchTypeSegment, testResponse2Handler)
	declare(r, matching.PathMatchTypePrefix, testResponse1Handler)
	for path, want := range map[string]string{
		"/foo":     testResponse2Text,
		"/foo/":    testResponse2Text,
		"/foo/bar": testResponse2Text,
		"/foobar":  testResponse1Text,
	} {
		got, _ := serveWith(t, r, http.MethodGet, "", path, nil)
		require.Equal(t, want, got, "GET %s", path)
		got, _ = serveWith(t, r, http.MethodHead, "", path, nil)
		require.Equal(t, want, got, "HEAD %s", path)
	}

	// a later registration replaces the route of its own kind in place, and
	// leaves the other alone
	declare(r, matching.PathMatchTypePrefix, testResponse3Handler)
	got, _ := serveWith(t, r, http.MethodGet, "", "/foobar", nil)
	require.Equal(t, testResponse3Text, got, "a plain prefix replaces a plain prefix")
	got, _ = serveWith(t, r, http.MethodGet, "", "/foo/bar", nil)
	require.Equal(t, testResponse2Text, got, "the segment route is untouched")
	declare(r, matching.PathMatchTypeSegment, testResponse1Handler)
	got, _ = serveWith(t, r, http.MethodGet, "", "/foo/bar", nil)
	require.Equal(t, testResponse1Text, got, "a segment route replaces a segment route")
	got, _ = serveWith(t, r, http.MethodGet, "", "/foobar", nil)
	require.Equal(t, testResponse3Text, got, "the plain prefix is untouched")
	require.Len(t, slot(r).Candidates, 2)

	// registered the other way round, the plain prefix matches everything the
	// segment would: the segment is kept but registration order leaves it unreached
	r2 := NewRouter().(*lmRouter)
	declare(r2, matching.PathMatchTypePrefix, testResponse1Handler)
	declare(r2, matching.PathMatchTypeSegment, testResponse2Handler)
	for _, path := range []string{"/foo", "/foo/bar", "/foobar"} {
		got, _ := serveWith(t, r2, http.MethodGet, "", path, nil)
		require.Equal(t, testResponse1Text, got, path)
	}
	require.Len(t, slot(r2).Candidates, 2, "the segment route is kept, not deleted")
}

func TestEqualOrderCandidatesKeepRegistrationOrder(t *testing.T) {
	// Order is the only rank: equal values resolve in registration order
	// whatever boundary kinds the routes sharing a path declare
	for _, prefixFirst := range []bool{true, false} {
		r := NewRouter().(*lmRouter)
		declare := func(mt matching.PathMatchType, order int, h http.Handler) {
			require.NoError(t, r.RegisterRouteSpec(route.Spec{
				Path:      "/foo",
				MatchType: mt, Methods: []string{http.MethodGet}, Order: order,
				Predicates: predicates(t, header("X-Tenant", "a"), nil), Handler: h,
			}))
		}
		want := testResponse1Text
		if prefixFirst {
			declare(matching.PathMatchTypePrefix, 5, testResponse1Handler)
			declare(matching.PathMatchTypeSegment, 5, testResponse2Handler)
		} else {
			declare(matching.PathMatchTypeSegment, 5, testResponse2Handler)
			declare(matching.PathMatchTypePrefix, 5, testResponse1Handler)
			want = testResponse2Text
		}
		hdr := http.Header{"X-Tenant": {"a"}}
		got, _ := serveWith(t, r, http.MethodGet, "", "/foo/bar", hdr)
		require.Equal(t, want, got, "the first registered candidate answers")
		// off a boundary only the plain prefix is eligible, either way round
		got, _ = serveWith(t, r, http.MethodGet, "", "/foobar", hdr)
		require.Equal(t, testResponse1Text, got)
		// a lower Order still outranks both
		declare(matching.PathMatchTypeSegment, 1, testResponse3Handler)
		got, _ = serveWith(t, r, http.MethodGet, "", "/foo/bar", hdr)
		require.Equal(t, testResponse3Text, got)
	}
}
