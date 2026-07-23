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

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func TestPlanTSMMergeStrategies(t *testing.T) {
	const (
		standard = merge.TSMReductionStandard
		weighted = merge.TSMReductionWeightedAverage
		pooled   = merge.TSMReductionPooledVariance
	)
	tests := []struct {
		query          string
		wantStrategy   int
		wantReduction  merge.TSMReductionKind
		wantWarnSubstr string
	}{
		// Queries without an outer aggregation use deduplication.
		{"up", int(merge.StrategyDedup), standard, ""},
		{"rate(http_requests_total[5m])", int(merge.StrategyDedup), standard, ""},
		// Sum, count, and count_values accumulate shard values.
		{"sum(up)", int(merge.StrategySum), standard, ""},
		{"sum by (job) (up)", int(merge.StrategySum), standard, ""},
		{"count(up)", int(merge.StrategySum), standard, ""},
		{"count_values(\"code\", http_requests_total)", int(merge.StrategySum), standard, ""},
		// Average uses paired sum and count variants.
		{"avg(up)", int(merge.StrategySum), weighted, ""},
		{"avg by (region) (up)", int(merge.StrategySum), weighted, ""},
		{"min(up)", int(merge.StrategyMin), standard, ""},
		{"max(up)", int(merge.StrategyMax), standard, ""},
		// Rank aggregations are rewritten and finalized after merge.
		{"topk(5, up)", int(merge.StrategyDedup), standard, ""},
		{"topk(5, sum by (service) (up))", int(merge.StrategySum), standard, ""},
		{"topk(5, avg by (service) (up))", int(merge.StrategySum), weighted, ""},
		{"sort_desc(topk(5, max(up)))", int(merge.StrategyMax), standard, ""},
		{"bottomk(5, up)", int(merge.StrategyDedup), standard, ""},
		{"sort_desc(topk(5, up))", int(merge.StrategyDedup), standard, ""},
		// Literal limit_ratio uses a shard-local fast path or globally merges a
		// compatible inner aggregation before applying the ratio.
		{"limit_ratio(0.5, up)", int(merge.StrategyDedup), standard, ""},
		{"limit_ratio by (job) (-0.5, rate(requests[5m]))", int(merge.StrategyDedup), standard, ""},
		{"limit_ratio(0.5, count by (service) (requests))", int(merge.StrategySum), standard, ""},
		{"limit_ratio(0.5, min(requests))", int(merge.StrategyMin), standard, ""},
		{"limit_ratio(0.5, max(requests))", int(merge.StrategyMax), standard, ""},
		{"limit_ratio(0.5, group by (service) (requests))", int(merge.StrategyDedup), standard, ""},
		{"limit_ratio(0.5, sum by (service) (requests))", int(merge.StrategySum), standard, ""},
		{"limit_ratio(0.5, avg by (service) (requests))", int(merge.StrategySum), weighted, ""},
		{"sort_desc(limit_ratio(0.5, sum(requests)))", int(merge.StrategySum), standard, ""},
		// Sort wrappers preserve the inner aggregation strategy.
		{"sort(sum(up))", int(merge.StrategySum), standard, ""},
		{"sort_desc(count by (service) (up))", int(merge.StrategySum), standard, ""},
		{"sort(avg by (service) (up))", int(merge.StrategySum), weighted, ""},
		{"sort(min(up))", int(merge.StrategyMin), standard, ""},
		{"sort_desc(max(up))", int(merge.StrategyMax), standard, ""},
		{"sort(up)", int(merge.StrategyDedup), standard, ""},
		// Float-only stddev/stdvar use paired count, mean, and variance inputs.
		{"stddev(up)", int(merge.StrategyDedup), pooled, ""},
		{"stdvar without (instance) (rate(requests[5m]))", int(merge.StrategyDedup), pooled, ""},
		{"sort_desc(stddev by (job) (up))", int(merge.StrategyDedup), pooled, ""},
		// Numeric-compatible inner aggregations are completed globally before
		// the outer variance aggregation is finalized.
		{"stddev(count by (service) (requests))", int(merge.StrategySum), standard, ""},
		{"stddev(min(requests))", int(merge.StrategyMin), standard, ""},
		{"stdvar(max(requests))", int(merge.StrategyMax), standard, ""},
		{"stddev(group by (service) (requests))", int(merge.StrategyDedup), standard, ""},
		// Unsupported aggregations fall back to deduplication with a warning.
		{"stddev(sum by (service) (requests))", int(merge.StrategySum), standard, ""},
		{"stdvar(avg by (service) (requests))", int(merge.StrategySum), weighted, ""},
		{"stddev(up + down)", int(merge.StrategyDedup), standard, aggregation.StdDev},
		{"stdvar(rate(sum(up)[5m:]))", int(merge.StrategyDedup), standard, aggregation.StdVar},
		{"stddev(stdvar(up))", int(merge.StrategyDedup), standard, aggregation.StdDev},
		{"quantile(0.9, up)", int(merge.StrategyDedup), standard, aggregation.Quantile},
		{"topk(5, stddev(up))", int(merge.StrategyDedup), standard, aggregation.StdDev},
		{"topk(k, up)", int(merge.StrategyDedup), standard, aggregation.TopK},
		{"limitk(5, up)", int(merge.StrategyDedup), standard, aggregation.LimitK},
		{"limit_ratio(0.5, stddev(up))", int(merge.StrategyDedup), standard, aggregation.StdDev},
		{"sort_desc(limit_ratio(0.5, stddev(up)))", int(merge.StrategyDedup), standard, aggregation.StdDev},
		{"limit_ratio(0.5, rate(sum(up)[5m:]))", int(merge.StrategyDedup), standard, "nested"},
		{"limit_ratio(0.5, sum(sum(up)))", int(merge.StrategyDedup), standard, "nested"},
		{"limit_ratio(0.5, sum(up + down))", int(merge.StrategyDedup), standard, "binary"},
		{"limit_ratio(0.5, up + on (job) group_left down)", int(merge.StrategyDedup), standard, "binary"},
		{"limit_ratio(0.5, absent(up))", int(merge.StrategyDedup), standard, "absent"},
		{"limit_ratio(0.5, sum(absent(up)))", int(merge.StrategyDedup), standard, "absent"},
		{"limit_ratio(scalar(ratio), up)", int(merge.StrategyDedup), standard, aggregation.LimitRatio},
		{"group(up)", int(merge.StrategyDedup), standard, ""},
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
			switch test.wantReduction {
			case weighted:
				wantVariants = 2
			case pooled:
				wantVariants = 3
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

func mustTSMMergePlan(t *testing.T, r *http.Request, query string) *merge.TSMMergePlan {
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
		if len(plan.Variants) != 1 || plan.Variants[0].Name != merge.TSMVariantPrimary {
			t.Fatalf("variants: %#v", plan.Variants)
		}
		if plan.Variants[0].Request != r {
			t.Fatal("unchanged primary request was cloned")
		}
		if !plan.Variants[0].ResponseAuthority {
			t.Fatal("primary is not response authority")
		}
		if plan.Reduction.Kind != merge.TSMReductionStandard ||
			len(plan.Reduction.InputVariants) != 1 ||
			plan.Reduction.InputVariants[0] != merge.TSMVariantPrimary {
			t.Fatalf("reduction: %#v", plan.Reduction)
		}
		if plan.Completeness != merge.TSMCompletenessResponseAuthority {
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
			plan.Variants[0].Name != merge.TSMVariantWeightedAverageSum ||
			plan.Variants[1].Name != merge.TSMVariantWeightedAverageCount {
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
				merge.TSMVariantWeightedAverageSum)
		}
		if plan.Reduction.Kind != merge.TSMReductionWeightedAverage ||
			strings.Join(plan.Reduction.InputVariants, ",") !=
				strings.Join(merge.TSMReductionWeightedAverageVariants(), ",") {
			t.Fatalf("reduction: %#v", plan.Reduction)
		}
		if plan.Completeness != merge.TSMCompletenessAllVariants {
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
		const query = "stddev(up + down)"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		if plan.Variants[0].MergeStrategy != int(merge.StrategyDedup) {
			t.Fatalf("strategy: %d", plan.Variants[0].MergeStrategy)
		}
		if !strings.Contains(plan.UnsupportedWarning, aggregation.StdDev) {
			t.Fatalf("warning: %q", plan.UnsupportedWarning)
		}
		if plan.AllowSingleMemberBypass {
			t.Fatal("unsupported fallback allowed a single-member bypass")
		}
	})
}

func TestPlanTSMMergeVarianceContents(t *testing.T) {
	t.Run("pooled float-only variants", func(t *testing.T) {
		const query = "sort_desc(stddev without (instance) (rate(requests[5m])))"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)

		if plan.Reduction.Kind != merge.TSMReductionPooledVariance || len(plan.Variants) != 3 {
			t.Fatalf("pooled plan: %#v", plan)
		}
		spec, found := promql.ParseVarianceAggregation(query)
		if !found {
			t.Fatal("expected variance aggregation")
		}
		wantNames := merge.TSMReductionPooledVarianceVariants()
		wantQueries := []string{
			promql.VarianceVariantQuery(spec, aggregation.Count),
			promql.VarianceVariantQuery(spec, aggregation.Average),
			promql.VarianceVariantQuery(spec, aggregation.StdVar),
		}
		for i, variant := range plan.Variants {
			values, _, _ := params.GetRequestValues(variant.Request)
			if variant.Name != wantNames[i] || values.Get(promQueryParam) != wantQueries[i] {
				t.Fatalf("variant %d: name=%q query=%q", i, variant.Name,
					values.Get(promQueryParam))
			}
			if variant.MergeStrategy != int(merge.StrategyDedup) {
				t.Fatalf("variant %d strategy: %d", i, variant.MergeStrategy)
			}
			if variant.ResponseAuthority != (i == 0) {
				t.Fatalf("variant %d authority: %v", i, variant.ResponseAuthority)
			}
		}
		if !plan.Finalizer.Enabled || plan.Finalizer.Query != query ||
			plan.Completeness != merge.TSMCompletenessAllVariants ||
			!plan.StripInjectedLabels || plan.AllowSingleMemberBypass {
			t.Fatalf("pooled metadata: %#v", plan)
		}
	})

	t.Run("metric name grouping survives float filter", func(t *testing.T) {
		const query = "stdvar by (__name__, job) (up)"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
		got := values.Get(promQueryParam)
		for _, required := range []string{
			"clamp(", "__trickster_tsm_name__", "label_replace(", "sum by (__name__, job)",
		} {
			if !strings.Contains(got, required) {
				t.Fatalf("query %q does not contain %q", got, required)
			}
		}
	})

	t.Run("globally merged inner count", func(t *testing.T) {
		const (
			query = "stddev by (region) (count by (service) (requests))"
			inner = "count by (service) (requests)"
		)
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
		if got := values.Get(promQueryParam); got != inner {
			t.Fatalf("fanout query got %q want %q", got, inner)
		}
		if plan.Reduction.Kind != merge.TSMReductionStandard ||
			plan.Variants[0].MergeStrategy != int(merge.StrategySum) ||
			!plan.Finalizer.Enabled || !plan.StripInjectedLabels {
			t.Fatalf("inner count plan: %#v", plan)
		}
	})

	t.Run("histogram-capable inner aggregations use exact plans", func(t *testing.T) {
		tests := []struct {
			query       string
			variants    int
			reduction   merge.TSMReductionKind
			wantQueries []string
		}{
			{
				query:       "stddev(sum by (service) (requests))",
				variants:    1,
				reduction:   merge.TSMReductionStandard,
				wantQueries: []string{"sum by (service) (requests)"},
			},
			{
				query:       "stdvar(avg by (service) (requests))",
				variants:    2,
				reduction:   merge.TSMReductionWeightedAverage,
				wantQueries: []string{"sum by (service) (requests)", "count by (service) (requests)"},
			},
		}
		for _, test := range tests {
			t.Run(test.query, func(t *testing.T) {
				r, _ := http.NewRequest(http.MethodGet,
					"http://example.com/api/v1/query?query="+url.QueryEscape(test.query), nil)
				plan := mustTSMMergePlan(t, r, test.query)
				if len(plan.Variants) != test.variants ||
					plan.Reduction.Kind != test.reduction ||
					!plan.Finalizer.Enabled || plan.UnsupportedWarning != "" {
					t.Fatalf("exact plan: %#v", plan)
				}
				for i, wantQuery := range test.wantQueries {
					values, _, _ := params.GetRequestValues(plan.Variants[i].Request)
					if got := values.Get(promQueryParam); got != wantQuery {
						t.Fatalf("variant %d query got %q want %q", i, got, wantQuery)
					}
					if plan.Variants[i].MergeStrategy != int(merge.StrategySum) {
						t.Fatalf("variant %d strategy: %d", i, plan.Variants[i].MergeStrategy)
					}
				}
			})
		}
	})

	t.Run("unsupported sorted binary expression keeps fallback", func(t *testing.T) {
		const query = "sort(stddev(up + down))"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
		if got := values.Get(promQueryParam); got != "stddev(up + down)" {
			t.Fatalf("fallback query got %q", got)
		}
		if plan.Reduction.Kind != merge.TSMReductionStandard ||
			!strings.Contains(plan.UnsupportedWarning, aggregation.StdDev) ||
			!plan.Finalizer.Enabled {
			t.Fatalf("fallback plan: %#v", plan)
		}
	})
}

func TestPlanTSMMergeVariancePOST(t *testing.T) {
	const (
		query    = "stddev by (job) (rate(requests[5m]))"
		origBody = "query=stddev+by+%28job%29+%28rate%28requests%5B5m%5D%29%29&time=1000"
	)
	r, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v1/query",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	r = request.SetResources(r, &request.Resources{RequestBody: []byte(origBody)})

	plan := mustTSMMergePlan(t, r, query)
	want := []string{
		"count by (job) (clamp(rate(requests[5m]), -Inf, +Inf))",
		"avg by (job) (clamp(rate(requests[5m]), -Inf, +Inf))",
		"stdvar by (job) (clamp(rate(requests[5m]), -Inf, +Inf))",
	}
	for i, variant := range plan.Variants {
		values, _, _ := params.GetRequestValues(variant.Request)
		if got := values.Get(promQueryParam); got != want[i] {
			t.Fatalf("variant %d values query got %q want %q", i, got, want[i])
		}
		body, err := io.ReadAll(variant.Request.Body)
		if err != nil {
			t.Fatalf("variant %d read body: %v", i, err)
		}
		bodyValues, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("variant %d parse body: %v", i, err)
		}
		if got := bodyValues.Get(promQueryParam); got != want[i] {
			t.Fatalf("variant %d body query got %q want %q", i, got, want[i])
		}
	}
}

func TestPlanTSMMergeLimitRatioContents(t *testing.T) {
	t.Run("shard-local fast path", func(t *testing.T) {
		const query = "limit_ratio(0.5, rate(requests[5m]))"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)

		if plan.Variants[0].Request != r {
			t.Fatal("fast path cloned the original request")
		}
		if plan.Finalizer.Enabled {
			t.Fatalf("fast path enabled finalizer: %#v", plan.Finalizer)
		}
		if !plan.StripInjectedLabels {
			t.Fatal("fast path did not request injected-label stripping")
		}
		if !plan.AllowSingleMemberBypass {
			t.Fatal("fast path did not allow a safe single-member bypass")
		}
	})

	t.Run("globally merged inner count", func(t *testing.T) {
		const (
			query = "limit_ratio(0.5, count by (service) (requests))"
			inner = "count by (service) (requests)"
		)
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		values, _, _ := params.GetRequestValues(plan.Variants[0].Request)

		if got := values.Get(promQueryParam); got != inner {
			t.Fatalf("fanout query got %q want %q", got, inner)
		}
		if plan.Variants[0].MergeStrategy != int(merge.StrategySum) {
			t.Fatalf("strategy got %d want sum", plan.Variants[0].MergeStrategy)
		}
		if !plan.Finalizer.Enabled || plan.Finalizer.Query != query {
			t.Fatalf("finalizer: %#v", plan.Finalizer)
		}
		if !plan.StripInjectedLabels {
			t.Fatal("global plan did not request injected-label stripping")
		}
	})

	t.Run("sort wrapper moves sampling and ordering global", func(t *testing.T) {
		const (
			query = "sort_desc(limit_ratio(0.5, up))"
			inner = "up"
		)
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		values, _, _ := params.GetRequestValues(plan.Variants[0].Request)

		if got := values.Get(promQueryParam); got != inner {
			t.Fatalf("fanout query got %q want %q", got, inner)
		}
		if !plan.Finalizer.Enabled || plan.Finalizer.Query != query {
			t.Fatalf("finalizer: %#v", plan.Finalizer)
		}
	})

	t.Run("histogram-capable inner aggregations use exact plans", func(t *testing.T) {
		tests := []struct {
			operator  string
			reduction merge.TSMReductionKind
			variants  int
		}{
			{aggregation.Sum, merge.TSMReductionStandard, 1},
			{aggregation.Average, merge.TSMReductionWeightedAverage, 2},
		}
		for _, test := range tests {
			query := "limit_ratio(-0.5, " + test.operator + " by (service) (requests))"
			r, _ := http.NewRequest(http.MethodGet,
				"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
			plan := mustTSMMergePlan(t, r, query)

			if len(plan.Variants) != test.variants ||
				plan.Variants[0].MergeStrategy != int(merge.StrategySum) ||
				plan.Reduction.Kind != test.reduction || !plan.Finalizer.Enabled {
				t.Fatalf("%s exact plan: %#v", test.operator, plan)
			}
			if plan.UnsupportedWarning != "" {
				t.Fatalf("%s warning: %q", test.operator, plan.UnsupportedWarning)
			}
		}
	})

	t.Run("nested unsupported aggregation falls back with warning", func(t *testing.T) {
		const query = "limit_ratio(0.5, rate(sum(up)[5m:]))"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)

		if plan.Variants[0].Request != r {
			t.Fatal("unsupported fallback rewrote the request")
		}
		if !strings.Contains(plan.UnsupportedWarning, "nested") {
			t.Fatalf("warning got %q", plan.UnsupportedWarning)
		}
		if plan.Finalizer.Enabled {
			t.Fatalf("unsupported fallback enabled finalizer: %#v", plan.Finalizer)
		}
	})

	t.Run("cross-shard binary expression falls back with warning", func(t *testing.T) {
		const query = "limit_ratio(0.5, up + on (job) group_left down)"
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)

		if plan.Variants[0].Request != r {
			t.Fatal("binary fallback rewrote the request")
		}
		if !strings.Contains(plan.UnsupportedWarning, "cross-shard") {
			t.Fatalf("warning got %q", plan.UnsupportedWarning)
		}
		if plan.Finalizer.Enabled {
			t.Fatalf("binary fallback enabled finalizer: %#v", plan.Finalizer)
		}
	})

	t.Run("sort wrapper samples unsupported inner expression once", func(t *testing.T) {
		const (
			query = "sort_desc(limit_ratio(0.5, stddev(up)))"
			inner = "stddev(up)"
		)
		r, _ := http.NewRequest(http.MethodGet,
			"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
		plan := mustTSMMergePlan(t, r, query)
		values, _, _ := params.GetRequestValues(plan.Variants[0].Request)

		if got := values.Get(promQueryParam); got != inner {
			t.Fatalf("fanout query got %q want %q", got, inner)
		}
		if !plan.Finalizer.Enabled || plan.Finalizer.Query != query {
			t.Fatalf("finalizer: %#v", plan.Finalizer)
		}
		if !strings.Contains(plan.UnsupportedWarning, aggregation.StdDev) {
			t.Fatalf("warning got %q", plan.UnsupportedWarning)
		}
	})
}

func TestPlanTSMMergeLimitRatioPOST(t *testing.T) {
	const (
		query    = "limit_ratio(0.5, count by (service) (requests))"
		wantQ    = "count by (service) (requests)"
		origBody = "query=limit_ratio%280.5%2C+count+by+%28service%29+%28requests%29%29&start=1000&end=2000&step=15"
	)
	r, _ := http.NewRequest(http.MethodPost,
		"http://example.com/api/v1/query_range",
		io.NopCloser(bytes.NewBufferString(origBody)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	r = request.SetResources(r, &request.Resources{RequestBody: []byte(origBody)})

	plan := mustTSMMergePlan(t, r, query)
	rewritten := plan.Variants[0].Request
	values, _, _ := params.GetRequestValues(rewritten)
	if got := values.Get(promQueryParam); got != wantQ {
		t.Fatalf("rewritten query got %q want %q", got, wantQ)
	}
	body, err := io.ReadAll(rewritten.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	bodyValues, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatalf("parse body: %v", err)
	}
	if got := bodyValues.Get(promQueryParam); got != wantQ {
		t.Fatalf("body query got %q want %q", got, wantQ)
	}
	rsc := request.GetResources(rewritten)
	if rsc == nil {
		t.Fatal("rewritten request has no resources")
	}
	cachedValues, err := url.ParseQuery(string(rsc.RequestBody))
	if err != nil {
		t.Fatalf("parse cached body: %v", err)
	}
	if got := cachedValues.Get(promQueryParam); got != wantQ {
		t.Fatalf("cached body query got %q want %q", got, wantQ)
	}
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
		{merge.Sum, sumReq, "sum(up)"},
		{merge.Count, countReq, "count(up)"},
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
		{merge.Sum, sumReq, "sum(up)"},
		{merge.Count, countReq, "count(up)"},
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
		{merge.Sum, sumReq, "sum by (service) (requests)"},
		{merge.Count, countReq, "count by (service) (requests)"},
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
		{merge.Sum, sumReq, "sum by (service) (requests)"},
		{merge.Count, countReq, "count by (service) (requests)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := params.GetRequestValues(tc.req)
			if q := got.Get("query"); q != tc.wantQ {
				t.Fatalf("query got %q want %q", q, tc.wantQ)
			}
		})
	}
}
