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
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestPlanTSMMergeStrategies(t *testing.T) {
	const (
		standard = backends.TSMReductionStandard
		weighted = backends.TSMReductionWeightedAverage
	)
	tests := []struct {
		query          string
		wantStrategy   int
		wantReduction  backends.TSMReductionKind
		wantWarnSubstr string
	}{
		// Queries without an outer aggregation use deduplication.
		{"up", int(dataset.MergeStrategyDedup), standard, ""},
		{"rate(http_requests_total[5m])", int(dataset.MergeStrategyDedup), standard, ""},
		// Sum, count, and count_values accumulate shard values.
		{"sum(up)", int(dataset.MergeStrategySum), standard, ""},
		{"sum by (job) (up)", int(dataset.MergeStrategySum), standard, ""},
		{"count(up)", int(dataset.MergeStrategySum), standard, ""},
		{"count_values(\"code\", http_requests_total)", int(dataset.MergeStrategySum), standard, ""},
		// Average uses paired sum and count variants.
		{"avg(up)", int(dataset.MergeStrategySum), weighted, ""},
		{"avg by (region) (up)", int(dataset.MergeStrategySum), weighted, ""},
		{"min(up)", int(dataset.MergeStrategyMin), standard, ""},
		{"max(up)", int(dataset.MergeStrategyMax), standard, ""},
		// Rank aggregations are rewritten and finalized after merge.
		{"topk(5, up)", int(dataset.MergeStrategyDedup), standard, ""},
		{"topk(5, sum by (service) (up))", int(dataset.MergeStrategySum), standard, ""},
		{"topk(5, avg by (service) (up))", int(dataset.MergeStrategySum), weighted, ""},
		{"sort_desc(topk(5, max(up)))", int(dataset.MergeStrategyMax), standard, ""},
		{"bottomk(5, up)", int(dataset.MergeStrategyDedup), standard, ""},
		{"sort_desc(topk(5, up))", int(dataset.MergeStrategyDedup), standard, ""},
		// Sort wrappers preserve the inner aggregation strategy.
		{"sort(sum(up))", int(dataset.MergeStrategySum), standard, ""},
		{"sort_desc(count by (service) (up))", int(dataset.MergeStrategySum), standard, ""},
		{"sort(avg by (service) (up))", int(dataset.MergeStrategySum), weighted, ""},
		{"sort(min(up))", int(dataset.MergeStrategyMin), standard, ""},
		{"sort_desc(max(up))", int(dataset.MergeStrategyMax), standard, ""},
		{"sort(up)", int(dataset.MergeStrategyDedup), standard, ""},
		// Unsupported aggregations fall back to deduplication with a warning.
		{"stddev(up)", int(dataset.MergeStrategyDedup), standard, "stddev"},
		{"stdvar(up)", int(dataset.MergeStrategyDedup), standard, "stdvar"},
		{"quantile(0.9, up)", int(dataset.MergeStrategyDedup), standard, "quantile"},
		{"topk(5, stddev(up))", int(dataset.MergeStrategyDedup), standard, "stddev"},
		{"sort_desc(stddev(up))", int(dataset.MergeStrategyDedup), standard, "stddev"},
		{"topk(k, up)", int(dataset.MergeStrategyDedup), standard, "topk"},
		{"limitk(5, up)", int(dataset.MergeStrategyDedup), standard, "limitk"},
		{"limit_ratio(0.5, up)", int(dataset.MergeStrategyDedup), standard, "limit_ratio"},
		{"group(up)", int(dataset.MergeStrategyDedup), standard, ""},
	}

	c := &Client{}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet,
				"http://example.com/api/v1/query?query="+url.QueryEscape(test.query), nil)
			plan, err := c.PlanTSMMerge(r, test.query)
			if err != nil {
				t.Fatalf("PlanTSMMerge: %v", err)
			}
			if err := plan.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			wantVariants := 1
			if test.wantReduction == weighted {
				wantVariants = 2
			}
			if len(plan.Variants) != wantVariants {
				t.Fatalf("variant count: got %d want %d", len(plan.Variants), wantVariants)
			}
			for i, variant := range plan.Variants {
				if variant.MergeStrategy != test.wantStrategy {
					t.Errorf("variant %d strategy: got %d want %d",
						i, variant.MergeStrategy, test.wantStrategy)
				}
			}
			if plan.Reduction.Kind != test.wantReduction {
				t.Errorf("reduction: got %d want %d", plan.Reduction.Kind, test.wantReduction)
			}
			if test.wantWarnSubstr != "" {
				if !strings.Contains(plan.UnsupportedWarning, test.wantWarnSubstr) {
					t.Errorf("warning %q does not contain %q",
						plan.UnsupportedWarning, test.wantWarnSubstr)
				}
			} else if plan.UnsupportedWarning != "" {
				t.Errorf("expected no warning, got %q", plan.UnsupportedWarning)
			}
		})
	}
}

func mustTSMMergePlan(t *testing.T, r *http.Request, query string) *backends.TSMMergePlan {
	t.Helper()
	plan, err := (&Client{}).PlanTSMMerge(r, query)
	if err != nil {
		t.Fatalf("PlanTSMMerge: %v", err)
	}
	return plan
}

func TestPlanTSMMergeContents(t *testing.T) {
	t.Run("unchanged primary", func(t *testing.T) {
		const query = "sum by (job) (up)"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		if len(plan.Variants) != 1 || plan.Variants[0].Name != "primary" {
			t.Fatalf("variants: %#v", plan.Variants)
		}
		if plan.Variants[0].Request != r {
			t.Fatal("unchanged primary request was cloned")
		}
		if !plan.Variants[0].ResponseAuthority {
			t.Fatal("primary is not response authority")
		}
		if plan.Reduction.Kind != backends.TSMReductionStandard ||
			len(plan.Reduction.InputVariants) != 1 || plan.Reduction.InputVariants[0] != "primary" {
			t.Fatalf("reduction: %#v", plan.Reduction)
		}
		if plan.Completeness != backends.TSMCompletenessResponseAuthority {
			t.Fatalf("completeness: %d", plan.Completeness)
		}
		if !plan.AllowSingleMemberBypass {
			t.Fatal("safe unchanged primary did not allow a single-member bypass")
		}
	})

	t.Run("weighted average under rank finalizer", func(t *testing.T) {
		const query = "topk(5, avg by (service) (requests))"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		if plan.OriginalQuery != query {
			t.Fatalf("original query: got %q", plan.OriginalQuery)
		}
		if len(plan.Variants) != 2 ||
			plan.Variants[0].Name != backends.TSMVariantWeightedAverageSum ||
			plan.Variants[1].Name != backends.TSMVariantWeightedAverageCount {
			t.Fatalf("variants: %#v", plan.Variants)
		}
		if plan.Variants[0].Request == plan.Variants[1].Request ||
			plan.Variants[0].Request == r || plan.Variants[1].Request == r {
			t.Fatal("weighted-average variant requests are not independent clones")
		}
		for i, want := range []string{
			"sum by (service) (requests)",
			"count by (service) (requests)",
		} {
			values, _, _ := params.GetRequestValues(plan.Variants[i].Request)
			if got := values.Get(promQueryParam); got != want {
				t.Fatalf("variant %d query: got %q want %q", i, got, want)
			}
		}
		if !plan.Variants[0].ResponseAuthority || plan.Variants[1].ResponseAuthority {
			t.Fatalf("%s must be the sole response authority",
				backends.TSMVariantWeightedAverageSum)
		}
		if plan.Reduction.Kind != backends.TSMReductionWeightedAverage ||
			strings.Join(plan.Reduction.InputVariants, ",") !=
				strings.Join(backends.TSMReductionWeightedAverageVariants(), ",") {
			t.Fatalf("reduction: %#v", plan.Reduction)
		}
		if plan.Completeness != backends.TSMCompletenessAllVariants {
			t.Fatalf("completeness: %d", plan.Completeness)
		}
		if !plan.Finalizer.Enabled || plan.Finalizer.Query != query {
			t.Fatalf("finalizer: %#v", plan.Finalizer)
		}
		if plan.AllowSingleMemberBypass {
			t.Fatal("weighted-average plan allowed a single-member bypass")
		}
	})

	t.Run("unsupported fallback", func(t *testing.T) {
		const query = "stddev(up)"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		if plan.Variants[0].MergeStrategy != int(dataset.MergeStrategyDedup) {
			t.Fatalf("strategy: %d", plan.Variants[0].MergeStrategy)
		}
		if !strings.Contains(plan.UnsupportedWarning, "stddev") {
			t.Fatalf("warning: %q", plan.UnsupportedWarning)
		}
		if plan.AllowSingleMemberBypass {
			t.Fatal("unsupported fallback allowed a single-member bypass")
		}
	})
}

type failingTSMRequestBody struct{}

func (failingTSMRequestBody) Read([]byte) (int, error) { return 0, errors.New("read failure") }
func (failingTSMRequestBody) Close() error             { return nil }

func TestPlanTSMMergeRejectsUncloneableRequest(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range?query=avg%28up%29",
		failingTSMRequestBody{})
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	if _, err := (&Client{}).PlanTSMMerge(r, "avg(up)"); err == nil {
		t.Fatal("uncloneable request unexpectedly produced a plan")
	}
	if _, err := (&Client{}).PlanTSMMerge(nil, "avg(up)"); err == nil {
		t.Fatal("nil request unexpectedly produced a plan")
	}
}

// TestPlanTSMMergeWeightedAveragePOST verifies that the sum and count requests
// produced for a POST query carry the rewritten query in both the body and
// rsc.RequestBody, not the stale original avg body.
func TestPlanTSMMergeWeightedAveragePOST(t *testing.T) {
	const origBody = "query=avg%28up%29&start=1000&end=2000&step=15"
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)

	// Attach resources with body pre-cached, as ServeHTTP would have done.
	rsc := &request.Resources{RequestBody: []byte(origBody)}
	r = request.SetResources(r, rsc)

	plan := mustTSMMergePlan(t, r, "avg(up)")
	sumReq, countReq := plan.Variants[0].Request, plan.Variants[1].Request

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

// TestPlanTSMMergeWeightedAverageGET verifies that for a GET request the rewritten
// query lands in r.URL.RawQuery and that no body is written.
func TestPlanTSMMergeWeightedAverageGET(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query_range?query=avg%28up%29&start=1000&end=2000&step=15",
		nil)

	plan := mustTSMMergePlan(t, r, "avg(up)")
	sumReq, countReq := plan.Variants[0].Request, plan.Variants[1].Request

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

func TestPlanTSMMergeRankAggregationPOST(t *testing.T) {
	const (
		query    = "topk(5, sum(up))"
		wantQ    = "sum(up)"
		origBody = "query=topk%285%2C+sum%28up%29%29&start=1000&end=2000&step=15"
	)
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(ctx)
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	r = request.SetResources(r, &request.Resources{RequestBody: []byte(origBody)})

	plan := mustTSMMergePlan(t, r, query)
	rewritten := plan.Variants[0].Request
	rewrittenValues, _, _ := params.GetRequestValues(rewritten)
	rewrittenQuery := rewrittenValues.Get(promQueryParam)
	if rewrittenQuery != wantQ {
		t.Fatalf("rewritten query got %q want %q", rewrittenQuery, wantQ)
	}

	gotURL, err := url.ParseQuery(rewritten.URL.RawQuery)
	if err != nil {
		t.Fatalf("parse URL RawQuery: %v", err)
	}
	if q := gotURL.Get("query"); q != wantQ {
		t.Errorf("URL query param: got %q, want %q", q, wantQ)
	}

	bodyBytes, err := io.ReadAll(rewritten.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	gotBody, err := url.ParseQuery(string(bodyBytes))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if q := gotBody.Get("query"); q != wantQ {
		t.Errorf("body query param: got %q, want %q", q, wantQ)
	}

	gotRsc := request.GetResources(rewritten)
	if gotRsc == nil {
		t.Fatal("expected non-nil resources")
	}
	gotCache, err := url.ParseQuery(string(gotRsc.RequestBody))
	if err != nil {
		t.Fatalf("parse rsc.RequestBody: %v", err)
	}
	if q := gotCache.Get("query"); q != wantQ {
		t.Errorf("rsc.RequestBody query param: got %q, want %q", q, wantQ)
	}

	cancel()
	select {
	case <-rewritten.Context().Done():
		if err := rewritten.Context().Err(); err != context.Canceled {
			t.Fatalf("rewritten context error got %v want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("rewritten request did not observe source cancellation")
	}
}

func TestPlanTSMMergeSortWrapperPOST(t *testing.T) {
	const (
		query    = "sort_desc(sum by (service) (requests))"
		wantQ    = "sum by (service) (requests)"
		origBody = "query=sort_desc%28sum+by+%28service%29+%28requests%29%29&start=1000&end=2000&step=15"
	)
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	r = request.SetResources(r, &request.Resources{RequestBody: []byte(origBody)})

	plan := mustTSMMergePlan(t, r, query)
	rewritten := plan.Variants[0].Request
	rewrittenValues, _, _ := params.GetRequestValues(rewritten)
	rewrittenQuery := rewrittenValues.Get(promQueryParam)
	if rewrittenQuery != wantQ {
		t.Fatalf("rewritten query got %q want %q", rewrittenQuery, wantQ)
	}

	gotValues, _, _ := params.GetRequestValues(rewritten)
	if q := gotValues.Get("query"); q != wantQ {
		t.Fatalf("rewritten request query got %q want %q", q, wantQ)
	}
	gotRsc := request.GetResources(rewritten)
	if gotRsc == nil {
		t.Fatal("expected non-nil resources")
	}
	gotBody, err := url.ParseQuery(string(gotRsc.RequestBody))
	if err != nil {
		t.Fatalf("parse rsc.RequestBody: %v", err)
	}
	if q := gotBody.Get("query"); q != wantQ {
		t.Fatalf("rsc.RequestBody query got %q want %q", q, wantQ)
	}
}

func TestPlanTSMMergePreservesNonAggregationSort(t *testing.T) {
	const query = "sort(up)"
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)

	plan := mustTSMMergePlan(t, r, query)
	rewritten := plan.Variants[0].Request
	rewrittenValues, _, _ := params.GetRequestValues(rewritten)
	rewrittenQuery := rewrittenValues.Get(promQueryParam)
	if rewritten != r {
		t.Fatal("non-aggregation sort unexpectedly cloned the request")
	}
	if rewrittenQuery != query {
		t.Fatalf("rewritten query got %q want %q", rewrittenQuery, query)
	}
}

func TestPlanTSMMergeWeightedAverageRankAggregationGET(t *testing.T) {
	const query = "topk(5, avg by (service) (requests))"
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query_range?query="+url.QueryEscape(query)+"&start=1000&end=2000&step=15",
		nil)

	plan := mustTSMMergePlan(t, r, query)
	sumReq, countReq := plan.Variants[0].Request, plan.Variants[1].Request
	for _, tc := range []struct {
		name  string
		req   *http.Request
		wantQ string
	}{
		{"sum", sumReq, "sum by (service) (requests)"},
		{"count", countReq, "count by (service) (requests)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, err := url.ParseQuery(tc.req.URL.RawQuery)
			if err != nil {
				t.Fatalf("parse URL RawQuery: %v", err)
			}
			if q := gotURL.Get("query"); q != tc.wantQ {
				t.Errorf("URL query param: got %q, want %q", q, tc.wantQ)
			}
		})
	}
}

func TestPlanTSMMergeWeightedAverageSortWrapperGET(t *testing.T) {
	const query = "sort_desc(avg by (service) (requests))"
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)

	plan := mustTSMMergePlan(t, r, query)
	sumReq, countReq := plan.Variants[0].Request, plan.Variants[1].Request
	for _, tc := range []struct {
		name  string
		req   *http.Request
		wantQ string
	}{
		{"sum", sumReq, "sum by (service) (requests)"},
		{"count", countReq, "count by (service) (requests)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := params.GetRequestValues(tc.req)
			if q := got.Get("query"); q != tc.wantQ {
				t.Fatalf("query got %q want %q", q, tc.wantQ)
			}
		})
	}
}
