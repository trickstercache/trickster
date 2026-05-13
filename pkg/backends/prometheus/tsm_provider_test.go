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

package prometheus

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestClassifyMerge(t *testing.T) {
	tests := []struct {
		query          string
		wantStrategy   int
		wantDualQuery  bool
		wantWarnSubstr string // non-empty means a warning containing this substring is expected
	}{
		// no outer aggregator → dedup
		{"up", int(dataset.MergeStrategyDedup), false, ""},
		{"rate(http_requests_total[5m])", int(dataset.MergeStrategyDedup), false, ""},
		// sum family → sum, no dual query
		{"sum(up)", int(dataset.MergeStrategySum), false, ""},
		{"sum by (job) (up)", int(dataset.MergeStrategySum), false, ""},
		{"count(up)", int(dataset.MergeStrategySum), false, ""},
		{"count_values(\"code\", http_requests_total)", int(dataset.MergeStrategySum), false, ""},
		// avg → sum strategy + dual query
		{"avg(up)", int(dataset.MergeStrategySum), true, ""},
		{"avg by (region) (up)", int(dataset.MergeStrategySum), true, ""},
		// min / max
		{"min(up)", int(dataset.MergeStrategyMin), false, ""},
		{"max(up)", int(dataset.MergeStrategyMax), false, ""},
		// unsupported aggregators → dedup + warning containing the operator name
		{"stddev(up)", int(dataset.MergeStrategyDedup), false, "stddev"},
		{"stdvar(up)", int(dataset.MergeStrategyDedup), false, "stdvar"},
		{"quantile(0.9, up)", int(dataset.MergeStrategyDedup), false, "quantile"},
		{"topk(5, up)", int(dataset.MergeStrategyDedup), false, "topk"},
		{"bottomk(5, up)", int(dataset.MergeStrategyDedup), false, "bottomk"},
		{"limitk(5, up)", int(dataset.MergeStrategyDedup), false, "limitk"},
		{"limit_ratio(0.5, up)", int(dataset.MergeStrategyDedup), false, "limit_ratio"},
		// group → default → dedup, no warning
		{"group(up)", int(dataset.MergeStrategyDedup), false, ""},
	}

	c := &Client{}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			strategy, dualQuery, warning := c.ClassifyMerge(tt.query)
			if strategy != tt.wantStrategy {
				t.Errorf("strategy: got %d, want %d", strategy, tt.wantStrategy)
			}
			if dualQuery != tt.wantDualQuery {
				t.Errorf("needsDualQuery: got %v, want %v", dualQuery, tt.wantDualQuery)
			}
			if tt.wantWarnSubstr != "" {
				if !strings.Contains(warning, tt.wantWarnSubstr) {
					t.Errorf("warning %q does not contain %q", warning, tt.wantWarnSubstr)
				}
			} else if warning != "" {
				t.Errorf("expected no warning, got %q", warning)
			}
		})
	}
}

// TestRewriteForWeightedAvgPOST verifies that the sum and count requests
// produced for a POST query carry the rewritten query in both the body and
// rsc.RequestBody — not the stale original avg body.
func TestRewriteForWeightedAvgPOST(t *testing.T) {
	const origBody = "query=avg%28up%29&start=1000&end=2000&step=15"
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	// Attach resources with body pre-cached, as ServeHTTP would have done.
	rsc := &request.Resources{RequestBody: []byte(origBody)}
	r = request.SetResources(r, rsc)

	c := &Client{}
	sumReq, countReq := c.RewriteForWeightedAvg(r, "avg(up)")

	for _, tc := range []struct {
		name  string
		req   *http.Request
		wantQ string
	}{
		{"sum", sumReq, "sum(up)"},
		{"count", countReq, "count(up)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Verify the URL query string.
			gotURL, err := url.ParseQuery(tc.req.URL.RawQuery)
			if err != nil {
				t.Fatalf("parse URL RawQuery: %v", err)
			}
			if q := gotURL.Get("query"); q != tc.wantQ {
				t.Errorf("URL query param: got %q, want %q", q, tc.wantQ)
			}
			if s := gotURL.Get("start"); s != "1000" {
				t.Errorf("URL start param: got %q, want %q", s, "1000")
			}

			// Verify the request body.
			bodyBytes, err := io.ReadAll(tc.req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			gotBody, err := url.ParseQuery(string(bodyBytes))
			if err != nil {
				t.Fatalf("parse body: %v", err)
			}
			if q := gotBody.Get("query"); q != tc.wantQ {
				t.Errorf("body query param: got %q, want %q", q, tc.wantQ)
			}
			if s := gotBody.Get("start"); s != "1000" {
				t.Errorf("body start param: got %q, want %q", s, "1000")
			}

			// Verify rsc.RequestBody is also in sync (critical for
			// CloneWithoutResources in scatter goroutines).
			gotRsc := request.GetResources(tc.req)
			if gotRsc == nil {
				t.Fatal("expected non-nil resources")
			}
			gotCache, err := url.ParseQuery(string(gotRsc.RequestBody))
			if err != nil {
				t.Fatalf("parse rsc.RequestBody: %v", err)
			}
			if q := gotCache.Get("query"); q != tc.wantQ {
				t.Errorf("rsc.RequestBody query param: got %q, want %q", q, tc.wantQ)
			}
			if s := gotCache.Get("start"); s != "1000" {
				t.Errorf("rsc.RequestBody start param: got %q, want %q", s, "1000")
			}

			// Verify subsequent provider parsing sees the rewritten body, not a
			// stale PostForm cache populated when the original avg body was read.
			gotParsed, _, _ := params.GetRequestValues(tc.req)
			if q := gotParsed.Get("query"); q != tc.wantQ {
				t.Errorf("reparsed query param: got %q, want %q", q, tc.wantQ)
			}
		})
	}
}

// TestRewriteForWeightedAvg_GET verifies that for a GET request the rewritten
// query lands in r.URL.RawQuery and that no body is written.
func TestRewriteForWeightedAvgGET(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query_range?query=avg%28up%29&start=1000&end=2000&step=15",
		nil)

	c := &Client{}
	sumReq, countReq := c.RewriteForWeightedAvg(r, "avg(up)")

	for _, tc := range []struct {
		name  string
		req   *http.Request
		wantQ string
	}{
		{"sum", sumReq, "sum(up)"},
		{"count", countReq, "count(up)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, err := url.ParseQuery(tc.req.URL.RawQuery)
			if err != nil {
				t.Fatalf("parse URL RawQuery: %v", err)
			}
			if q := gotURL.Get("query"); q != tc.wantQ {
				t.Errorf("URL query param: got %q, want %q", q, tc.wantQ)
			}
			if s := gotURL.Get("start"); s != "1000" {
				t.Errorf("URL start param: got %q, want %q", s, "1000")
			}

			// GET requests must have no body.
			if tc.req.Body != nil {
				b, _ := io.ReadAll(tc.req.Body)
				if len(b) > 0 {
					t.Errorf("expected empty body for GET, got %q", string(b))
				}
			}

			// rsc.RequestBody should not be set for GET.
			if gotRsc := request.GetResources(tc.req); gotRsc != nil {
				if len(gotRsc.RequestBody) > 0 {
					t.Errorf("expected empty rsc.RequestBody for GET, got %q",
						string(gotRsc.RequestBody))
				}
			}
		})
	}
}
