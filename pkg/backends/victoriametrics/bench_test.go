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

package victoriametrics

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const benchQuery = `histogram_quantile(0.9, sum by (le, borough) (rate(trips_distance_miles_bucket{cab_type=~"yellow|green"}[5m])))`

func BenchmarkClassify(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		classify(benchQuery)
	}
}

func BenchmarkAnalyzeRemembered(b *testing.B) {
	a := newAnalyzer()
	a.analyze(benchQuery)
	b.ReportAllocs()
	for b.Loop() {
		a.analyze(benchQuery)
	}
}

func BenchmarkPrepareRangeRequest(b *testing.B) {
	o := url.Values{"query": {benchQuery}, "start": {"1700000017"}, "end": {"1700021617"}, "step": {"60"}}
	c := &Client{analyzer: newAnalyzer()}
	c.analyzer.analyze(benchQuery)
	rawQuery := o.Encode()
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/query_range?"+rawQuery, nil)
		if _, ok := c.prepare(r, routeRange); !ok {
			b.Fatal("refused")
		}
	}
}

func BenchmarkPreparePostRangeRequest(b *testing.B) {
	o := url.Values{"query": {benchQuery}, "start": {"1700000000"}, "end": {"1700021600"}, "step": {"60"}}
	c := &Client{analyzer: newAnalyzer()}
	body := o.Encode()
	b.ReportAllocs()
	for b.Loop() {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/query_range", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, ok := c.prepare(r, routeRange); !ok {
			b.Fatal("refused")
		}
	}
}
