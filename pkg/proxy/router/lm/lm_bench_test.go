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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/route"
)

var benchHandler = http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})

// newBenchRouter registers nClassic exact and nClassic prefix routes, and
// nRegex regex routes, all on the global host
func newBenchRouter(b *testing.B, nClassic, nRegex int) *lmRouter {
	r := NewRouter().(*lmRouter)
	for i := range nClassic {
		if err := r.RegisterRoute(fmt.Sprintf("/exact/path/%03d", i), nil, nil,
			matching.PathMatchTypeExact, benchHandler); err != nil {
			b.Fatal(err)
		}
		if err := r.RegisterRoute(fmt.Sprintf("/prefix/path/%03d", i), nil, nil,
			matching.PathMatchTypePrefix, benchHandler); err != nil {
			b.Fatal(err)
		}
	}
	for i := range nRegex {
		if err := r.RegisterRoute(fmt.Sprintf("^/svc%03d/[a-z-]+/[0-9]+", i),
			nil, nil, matching.PathMatchTypeRegex, benchHandler); err != nil {
			b.Fatal(err)
		}
	}
	return r
}

func benchRequest(b *testing.B, r *lmRouter, path string, expectMatch bool) {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.Handler(req).ServeHTTP(w, req)
	if expectMatch && w.Code != http.StatusOK {
		b.Fatalf("expected a route match for %s, got status %d", path, w.Code)
	}
	if !expectMatch && w.Code != http.StatusNotFound {
		b.Fatalf("expected no route match for %s, got status %d", path, w.Code)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r.Handler(req)
	}
}

// classic exact matching with no regex routes registered (baseline)
func BenchmarkClassicExact_RegexRoutes0(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 0), "/exact/path/005", true)
}

// classic exact matching with 100 regex routes registered; must be
// zero-delta vs the baseline because the exact tier matches first
func BenchmarkClassicExact_RegexRoutes100(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 100), "/exact/path/005", true)
}

// classic prefix matching with no regex routes registered (baseline)
func BenchmarkClassicPrefix_RegexRoutes0(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 0), "/prefix/path/005/extra", true)
}

// classic prefix matching with 100 regex routes registered; must be
// zero-delta vs the baseline because the prefix tier matches first
func BenchmarkClassicPrefix_RegexRoutes100(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 100), "/prefix/path/005/extra", true)
}

// all-miss with classic routes only (baseline for the empty-regex fast path)
func BenchmarkAllMiss_RegexRoutes0(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 0), "/no/such/route", false)
}

// worst-case regex-tier hit: the request matches only the last-evaluated
// pattern (equal-length patterns evaluate in registration order, so the
// highest-numbered service pattern is checked last), meaning every registered
// pattern is evaluated
func BenchmarkRegexWorstCaseHit_RegexRoutes1(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 1), "/svc000/detail/42", true)
}

func BenchmarkRegexWorstCaseHit_RegexRoutes10(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 10), "/svc009/detail/42", true)
}

func BenchmarkRegexWorstCaseHit_RegexRoutes100(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 100), "/svc099/detail/42", true)
}

// worst-case all-miss: both classic tiers and every regex pattern evaluated
func BenchmarkRegexAllMiss_RegexRoutes1(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 1), "/no/such/route", false)
}

func BenchmarkRegexAllMiss_RegexRoutes10(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 10), "/no/such/route", false)
}

func BenchmarkRegexAllMiss_RegexRoutes100(b *testing.B) {
	benchRequest(b, newBenchRouter(b, 10, 100), "/no/such/route", false)
}

const benchHost = "svc005.example.com"

// newBenchHostRouter registers nHosts exact-host routes and nWildcards
// wildcard-host routes on top of the classic global routes
func newBenchHostRouter(b *testing.B, nHosts, nWildcards int) *lmRouter {
	r := newBenchRouter(b, 10, 0)
	for i := range nHosts {
		if err := r.RegisterRoute("/host/path", []string{fmt.Sprintf("svc%03d.example.com", i)},
			nil, matching.PathMatchTypeExact, benchHandler); err != nil {
			b.Fatal(err)
		}
	}
	for i := range nWildcards {
		if err := r.RegisterRoute("/wild/path", []string{fmt.Sprintf("*.zone%03d.example.net", i)},
			nil, matching.PathMatchTypeExact, benchHandler); err != nil {
			b.Fatal(err)
		}
	}
	return r
}

func benchHostRequest(b *testing.B, r *lmRouter, host, path string, expectMatch bool) {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	r.Handler(req).ServeHTTP(w, req)
	if expectMatch && w.Code != http.StatusOK {
		b.Fatalf("expected a route match for %s%s, got status %d", host, path, w.Code)
	}
	if !expectMatch && w.Code != http.StatusNotFound {
		b.Fatalf("expected no route match for %s%s, got status %d", host, path, w.Code)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r.Handler(req)
	}
}

// exact-host hit with no wildcards registered (baseline)
func BenchmarkHostExact_Wildcards0(b *testing.B) {
	benchHostRequest(b, newBenchHostRouter(b, 10, 0), benchHost, "/host/path", true)
}

// exact-host hit with 100 wildcards registered; must be zero-delta vs the
// baseline because the exact host map is consulted first
func BenchmarkHostExact_Wildcards100(b *testing.B) {
	benchHostRequest(b, newBenchHostRouter(b, 10, 100), benchHost, "/host/path", true)
}

// global-route fallback for an unknown host with no wildcards (baseline)
func BenchmarkHostGlobalFallback_Wildcards0(b *testing.B) {
	benchHostRequest(b, newBenchHostRouter(b, 10, 0), "unknown.example.org", "/exact/path/005", true)
}

// global-route fallback for an unknown host with 100 wildcards; costs one
// extra map lookup on the host suffix
func BenchmarkHostGlobalFallback_Wildcards100(b *testing.B) {
	benchHostRequest(b, newBenchHostRouter(b, 10, 100), "unknown.example.org", "/exact/path/005", true)
}

// wildcard-host hit
func BenchmarkHostWildcardHit_Wildcards100(b *testing.B) {
	benchHostRequest(b, newBenchHostRouter(b, 10, 100), "api.zone050.example.net", "/wild/path", true)
}

// benchPredicates builds n conditioned routes' worth of predicates on the
// given header and query names; header values are exact unless regex is set
func benchPredicates(b *testing.B, hdrs, queries map[string]string, regex bool) *reqmatching.Predicates {
	b.Helper()
	p := &reqmatching.Predicates{}
	for name, value := range hdrs {
		hp, err := reqmatching.NewHeaderPredicate(name, value, regex)
		if err != nil {
			b.Fatal(err)
		}
		p.Headers = append(p.Headers, hp)
	}
	for name, value := range queries {
		qp, err := reqmatching.NewQueryPredicate(name, value, regex)
		if err != nil {
			b.Fatal(err)
		}
		p.Queries = append(p.Queries, qp)
	}
	return p
}

// newBenchPredicatedRouter adds n conditioned exact routes on other paths to
// the classic baseline router
func newBenchPredicatedRouter(b *testing.B, n int) *lmRouter {
	r := newBenchRouter(b, 10, 0)
	for i := range n {
		if err := r.RegisterRouteSpec(route.Spec{
			Path: fmt.Sprintf("/cond/path/%03d", i), MatchType: matching.PathMatchTypeExact,
			Predicates: benchPredicates(b, map[string]string{"X-Tenant": fmt.Sprintf("t%03d", i)}, nil, false),
			Handler:    benchHandler,
		}); err != nil {
			b.Fatal(err)
		}
	}
	return r
}

func benchHeaderRequest(b *testing.B, r *lmRouter, path string, hdr map[string]string, expectMatch bool) {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.Handler(req).ServeHTTP(w, req)
	if expectMatch && w.Code != http.StatusOK {
		b.Fatalf("expected a route match for %s, got status %d", path, w.Code)
	}
	if !expectMatch && w.Code != http.StatusNotFound {
		b.Fatalf("expected no route match for %s, got status %d", path, w.Code)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r.Handler(req)
	}
}

// classic exact and prefix hits with 100 conditioned routes registered on
// other paths; must be zero-delta vs the baselines
func BenchmarkClassicExact_Predicated100(b *testing.B) {
	benchRequest(b, newBenchPredicatedRouter(b, 100), "/exact/path/005", true)
}

func BenchmarkClassicPrefix_Predicated100(b *testing.B) {
	benchRequest(b, newBenchPredicatedRouter(b, 100), "/prefix/path/005/extra", true)
}

// one exact-header candidate hit
func BenchmarkPredicateHeaderExact(b *testing.B) {
	benchHeaderRequest(b, newBenchPredicatedRouter(b, 100), "/cond/path/050",
		map[string]string{"X-Tenant": "t050"}, true)
}

// one regex-header candidate hit
func BenchmarkPredicateHeaderRegex(b *testing.B) {
	r := newBenchRouter(b, 10, 0)
	if err := r.RegisterRouteSpec(route.Spec{
		Path: "/cond/re", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: benchPredicates(b, map[string]string{"X-Tenant": "^t[0-9]+$"}, nil, true), Handler: benchHandler,
	}); err != nil {
		b.Fatal(err)
	}
	benchHeaderRequest(b, r, "/cond/re", map[string]string{"X-Tenant": "t050"}, true)
}

// one query candidate hit: the one allocation is the parsed query
func BenchmarkPredicateQuery(b *testing.B) {
	r := newBenchRouter(b, 10, 0)
	if err := r.RegisterRouteSpec(route.Spec{
		Path: "/cond/q", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: benchPredicates(b, nil, map[string]string{"v": "1"}, false), Handler: benchHandler,
	}); err != nil {
		b.Fatal(err)
	}
	benchHeaderRequest(b, r, "/cond/q?v=1", nil, true)
}

// header and query predicates on one candidate
func BenchmarkPredicateMixed(b *testing.B) {
	r := newBenchRouter(b, 10, 0)
	if err := r.RegisterRouteSpec(route.Spec{
		Path: "/cond/m", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
		Predicates: benchPredicates(b, map[string]string{"X-Tenant": "t"}, map[string]string{"v": "1"}, false), Handler: benchHandler,
	}); err != nil {
		b.Fatal(err)
	}
	benchHeaderRequest(b, r, "/cond/m?v=1", map[string]string{"X-Tenant": "t"}, true)
}

// newBenchCandidateRouter registers n conditioned candidates on one exact
// path, each on a different header value, and no unconditioned route
func newBenchCandidateRouter(b *testing.B, n int) *lmRouter {
	r := newBenchRouter(b, 10, 0)
	for i := range n {
		if err := r.RegisterRouteSpec(route.Spec{
			Path: "/cond/slot", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
			Predicates: benchPredicates(b, map[string]string{"X-Tenant": fmt.Sprintf("t%03d", i)}, nil, false), Handler: benchHandler,
		}); err != nil {
			b.Fatal(err)
		}
	}
	return r
}

// the request satisfies only the last of 10 candidates
func BenchmarkPredicateLastCandidateHit10(b *testing.B) {
	benchHeaderRequest(b, newBenchCandidateRouter(b, 10), "/cond/slot",
		map[string]string{"X-Tenant": "t009"}, true)
}

// the request satisfies none of 10 candidates and nothing else matches
func BenchmarkPredicateAllCandidatesMiss10(b *testing.B) {
	benchHeaderRequest(b, newBenchCandidateRouter(b, 10), "/cond/slot",
		map[string]string{"X-Tenant": "none"}, false)
}

// a failed exact candidate falls to a covering prefix route
func BenchmarkPredicateFallbackExactToPrefix(b *testing.B) {
	r := newBenchCandidateRouter(b, 1)
	if err := r.RegisterRoute("/cond/", nil, nil, matching.PathMatchTypePrefix, benchHandler); err != nil {
		b.Fatal(err)
	}
	benchHeaderRequest(b, r, "/cond/slot", map[string]string{"X-Tenant": "none"}, true)
}

// a failed prefix candidate falls to the next shorter prefix
func BenchmarkPredicateFallbackPrefixToPrefix(b *testing.B) {
	r := newBenchRouter(b, 10, 0)
	if err := r.RegisterRouteSpec(route.Spec{
		Path: "/cond/deep/", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypePrefix,
		Predicates: benchPredicates(b, map[string]string{"X-Tenant": "t"}, nil, false), Handler: benchHandler,
	}); err != nil {
		b.Fatal(err)
	}
	if err := r.RegisterRoute("/cond/", nil, nil, matching.PathMatchTypePrefix, benchHandler); err != nil {
		b.Fatal(err)
	}
	benchHeaderRequest(b, r, "/cond/deep/x", map[string]string{"X-Tenant": "none"}, true)
}

// ten candidates each with a query predicate, the last one hit: allocations
// must equal the single-candidate query case, proving one parse per attempt
func BenchmarkPredicateManyQueryOneParse(b *testing.B) {
	r := newBenchRouter(b, 10, 0)
	for i := range 10 {
		if err := r.RegisterRouteSpec(route.Spec{
			Path: "/cond/q", Hosts: nil, Methods: nil, MatchType: matching.PathMatchTypeExact,
			Predicates: benchPredicates(b, nil, map[string]string{"v": fmt.Sprintf("%d", i)}, false), Handler: benchHandler,
		}); err != nil {
			b.Fatal(err)
		}
	}
	benchHeaderRequest(b, r, "/cond/q?v=9", nil, true)
}

// newBenchSegmentRouter adds n segment routes on other paths to the classic baseline
func newBenchSegmentRouter(b *testing.B, n int) *lmRouter {
	r := newBenchRouter(b, 10, 0)
	for i := range n {
		if err := r.RegisterRoute(fmt.Sprintf("/seg/path/%03d", i), nil, nil,
			matching.PathMatchTypeSegment, benchHandler); err != nil {
			b.Fatal(err)
		}
	}
	return r
}

// classic prefix hit with 100 segment routes registered; the segment flag is
// consulted only after a prefix matches
func BenchmarkClassicPrefix_Segment100(b *testing.B) {
	benchRequest(b, newBenchSegmentRouter(b, 100), "/prefix/path/005/extra", true)
}

func BenchmarkSegmentHit_Segment100(b *testing.B) {
	benchRequest(b, newBenchSegmentRouter(b, 100), "/seg/path/050/extra", true)
}

// newBenchAnyDepthRouter adds n any-depth wildcard hosts to the host router
func newBenchAnyDepthRouter(b *testing.B, nWildcards, nAnyDepth int) *lmRouter {
	r := newBenchHostRouter(b, 10, nWildcards)
	for i := range nAnyDepth {
		if err := r.RegisterRoute("/deep/path", []string{fmt.Sprintf("**.zone%03d.example.io", i)},
			nil, matching.PathMatchTypeExact, benchHandler); err != nil {
			b.Fatal(err)
		}
	}
	return r
}

// classic global hit on the host router with no wildcards: the baseline for
// the any-depth variant below, since the host map is larger than the classic
// router's
func BenchmarkClassicExact_Hosts10(b *testing.B) {
	benchRequest(b, newBenchHostRouter(b, 10, 0), "/exact/path/005", true)
}

// classic global hit with 100 any-depth wildcards registered elsewhere; a
// request naming no host skips the wildcard walk
func BenchmarkClassicExact_AnyDepth100(b *testing.B) {
	benchRequest(b, newBenchAnyDepthRouter(b, 0, 100), "/exact/path/005", true)
}

// exact-host hit with 100 any-depth wildcards registered
func BenchmarkHostExact_AnyDepth100(b *testing.B) {
	benchHostRequest(b, newBenchAnyDepthRouter(b, 0, 100), benchHost, "/host/path", true)
}

// global fallback for an unknown three-label host: one any-depth lookup per boundary
func BenchmarkHostGlobalFallback_AnyDepth100(b *testing.B) {
	benchHostRequest(b, newBenchAnyDepthRouter(b, 0, 100), "unknown.example.org", "/exact/path/005", true)
}

// one-label wildcard hit two labels deep (api.zoneNNN.example.net)
func BenchmarkHostWildcardHit2Labels_Wildcards100(b *testing.B) {
	benchHostRequest(b, newBenchAnyDepthRouter(b, 100, 0), "api.zone050.example.net", "/wild/path", true)
}

// any-depth wildcard hits two and four labels deep
func BenchmarkHostAnyDepthHit2Labels_AnyDepth100(b *testing.B) {
	benchHostRequest(b, newBenchAnyDepthRouter(b, 100, 100), "api.zone050.example.io", "/deep/path", true)
}

func BenchmarkHostAnyDepthHit4Labels_AnyDepth100(b *testing.B) {
	benchHostRequest(b, newBenchAnyDepthRouter(b, 100, 100), "a.b.api.zone050.example.io", "/deep/path", true)
}
