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
