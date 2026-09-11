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
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/promql/parser"
)

type tsmVariantShape struct {
	Name      string
	Strategy  int
	Authority bool
	Rewritten bool
}

type tsmPlanShape struct {
	Variants            []tsmVariantShape
	Reduction           merge.TSMReductionKind
	InputVariants       []string
	Finalizer           bool
	Completeness        merge.TSMCompletenessPolicy
	Warning             string
	StripInjectedLabels bool
	AllowBypass         bool
}

func planShape(t *testing.T, query string) (tsmPlanShape, []string) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
	plan := mustTSMMergePlan(t, r, query)
	shape := tsmPlanShape{
		Reduction:           plan.Reduction.Kind,
		InputVariants:       plan.Reduction.InputVariants,
		Finalizer:           plan.Finalizer.Enabled,
		Completeness:        plan.Completeness,
		Warning:             plan.UnsupportedWarning,
		StripInjectedLabels: plan.StripInjectedLabels,
		AllowBypass:         plan.AllowSingleMemberBypass,
	}
	var rewritten []string
	for _, variant := range plan.Variants {
		shape.Variants = append(shape.Variants, tsmVariantShape{
			Name:      variant.Name,
			Strategy:  variant.MergeStrategy,
			Authority: variant.ResponseAuthority,
			Rewritten: variant.Request != r,
		})
		if variant.Request != r {
			values, _, _ := params.GetRequestValues(variant.Request)
			rewritten = append(rewritten, values.Get(promQueryParam))
		}
	}
	return shape, rewritten
}

func canonicalPromQL(t *testing.T, query string) string {
	t.Helper()
	expr, err := parser.NewParser(parser.Options{EnableExperimentalFunctions: true}).ParseExpr(query)
	if err != nil {
		t.Fatalf("rewritten query %q is not valid PromQL: %v", query, err)
	}
	return expr.String()
}

func assertEquivalentPlans(t *testing.T, base, variant string, exactRewrites bool) {
	t.Helper()
	baseShape, baseRewrites := planShape(t, base)
	variantShape, variantRewrites := planShape(t, variant)
	if !reflect.DeepEqual(baseShape, variantShape) {
		t.Fatalf("plan shape differs\nbase    %q: %+v\nvariant %q: %+v",
			base, baseShape, variant, variantShape)
	}
	for i := range baseRewrites {
		want, got := baseRewrites[i], variantRewrites[i]
		if !exactRewrites {
			want, got = canonicalPromQL(t, want), canonicalPromQL(t, got)
		}
		if want != got {
			t.Fatalf("variant %d query differs\nbase    %q: %q\nvariant %q: %q",
				i, base, want, variant, got)
		}
	}
}

var tsmEquivalenceCorpus = []string{
	"up",
	"rate(requests[5m])",
	"scalar(count(up))",
	"sum(up)",
	"count by (job) (up)",
	"avg by (job) (up)",
	"min(up)",
	"max(up)",
	"group(up)",
	"topk(5, up)",
	"topk(5, sum by (job) (up))",
	"bottomk(3, avg by (job) (up))",
	"topk(5, stddev(up))",
	"limit_ratio(0.5, up)",
	"limit_ratio(0.5, count by (job) (up))",
	"sort_desc(limit_ratio(0.5, stddev(up)))",
	"limitk(5, sum by (job) (up))",
	"limitk(5, up + down)",
	"quantile(0.9, max by (job) (up))",
	"quantile(scalar(phi), up)",
	"stddev(up)",
	"stdvar by (job) (rate(requests[5m]))",
	"stddev(count by (job) (up))",
	"stddev(up + down)",
	"sort(sum(up))",
	"sort_desc(avg by (job) (up))",
	"sort(up)",
	"count(up) or vector(0)",
	"sum(up + down) or vector(0)",
	"sum by (job) (up) or on () vector(0)",
	"sum(up) + vector(1)",
	"abs(sum(up))",
	"sum by service (up)",
}

func TestPlanTSMMergeFormattingEquivalence(t *testing.T) {
	wrappers := map[string]func(string) string{
		"parentheses":        func(q string) string { return "(" + q + ")" },
		"nested parentheses": func(q string) string { return "((" + q + "))" },
		"whitespace":         func(q string) string { return " \n\t" + q + " \n" },
		"trailing comment":   func(q string) string { return q + " # trailing comment" },
		"leading comment":    func(q string) string { return "# leading comment\n(" + q + ")" },
	}
	for _, base := range tsmEquivalenceCorpus {
		for name, wrap := range wrappers {
			variant := wrap(base)
			t.Run(base+"/"+name, func(t *testing.T) {
				assertEquivalentPlans(t, base, variant, true)
			})
		}
	}
}

func TestPlanTSMMergeEquivalentSpellings(t *testing.T) {
	tests := [][2]string{
		{"sort(sum(up))", "sort((sum(up)))"},
		{"sort(sum(up))", "(sort((sort_desc(sum(up)))))"},
		{"sum by (job) (up)", "SUM(up) BY (job)"},
		{"topk(5, up)", "topk((5.0), (up))"},
		{"topk(5, up)", "topk(0x5, up)"},
		{"topk(5, sum by (job) (up))", "TOPK(+5, (sum(up) by (job)))"},
		{"quantile(0.9, max by (job) (up))", "quantile((9e-1), max(up) by (job))"},
		{"limitk(5, sum by (job) (up))", "LIMITK(5, sum(up) by (job,))"},
		{"limit_ratio(0.5, count by (job) (up))", "limit_ratio(500ms, (count(up) by (job)))"},
		{"stddev by (job) (rate(requests[5m]))", "stddev(rate(requests[5m])) by (job)"},
		{"count(up) or vector(0)", "(count(up)) or (vector((0)))"},
		{"count(up) or vector(0)", "count(up) or ignoring () vector(-0)"},
	}
	for _, tt := range tests {
		t.Run(tt[0]+" == "+tt[1], func(t *testing.T) {
			assertEquivalentPlans(t, tt[0], tt[1], false)
		})
	}
}

func finalizedSnapshot(t *testing.T, query string) string {
	t.Helper()
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
	plan := mustTSMMergePlan(t, r, query)
	values, _, _ := params.GetRequestValues(plan.Variants[0].Request)
	ds := rankDataSet(
		rankSeriesWithTags("a", dataset.Tags{"job": "x", "instance": "a"}, "1", 100),
		rankSeriesWithTags("c", dataset.Tags{"job": "x", "instance": "c"}, "3", 100),
		rankSeriesWithTags("b", dataset.Tags{"job": "y", "instance": "b"}, "4", 100),
		rankSeriesWithTags("d", dataset.Tags{"job": "y", "instance": "d"}, "2", 100),
	)
	for _, series := range ds.Results[0].SeriesList {
		series.Header.QueryStatement = values.Get(promQueryParam)
	}
	if plan.Finalizer.Enabled {
		(&Client{}).FinalizeTSMMerge(plan.Finalizer.Query, ds)
	}
	var out strings.Builder
	for _, series := range ds.Results[0].SeriesList {
		out.WriteString(series.Header.Name + series.Header.Tags.JSON())
		for _, point := range series.Points {
			for _, value := range point.Values {
				out.WriteString(" " + value.(string))
			}
		}
		out.WriteString(";")
	}
	return out.String()
}

func TestFinalizeTSMMergeFormattingEquivalence(t *testing.T) {
	bases := []string{
		"topk(2, up)",
		"sort_desc(topk by (job) (1, up))",
		"sort(sum(up))",
		"sort_desc(count by (job) (up))",
		"limitk(1, sum by (instance) (up))",
		"quantile(0.5, max by (instance) (up))",
		"limit_ratio(1, count by (instance) (up))",
		"stddev(count by (instance) (up))",
	}
	for _, base := range bases {
		want := finalizedSnapshot(t, base)
		for _, variant := range []string{
			"(" + base + ")",
			"((" + base + ")) # trailing comment",
			"\n  " + base + "\t",
		} {
			t.Run(variant, func(t *testing.T) {
				if got := finalizedSnapshot(t, variant); got != want {
					t.Fatalf("finalized output differs\nbase    %q: %s\nvariant %q: %s",
						base, want, variant, got)
				}
			})
		}
	}
}

func TestPlanTSMMergeZeroFallbackContents(t *testing.T) {
	const query = "(count(up)) or vector(0)"
	r, _ := http.NewRequest(http.MethodGet,
		"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
	plan := mustTSMMergePlan(t, r, query)
	if plan.Variants[0].Request != r || plan.Variants[0].MergeStrategy != int(merge.StrategySum) ||
		plan.Finalizer.Enabled || plan.UnsupportedWarning != "" || !plan.AllowSingleMemberBypass {
		t.Fatalf("zero fallback plan: %#v", plan)
	}
}

func TestPlanTSMMergeUnparsableQuery(t *testing.T) {
	for query, wantWarning := range map[string]string{
		"sum(":  tsmUnparsableWarning,
		"":      "",
		"  \n ": "",
	} {
		t.Run(query, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet,
				"http://example.com/api/v1/query?query="+url.QueryEscape(query), nil)
			plan := mustTSMMergePlan(t, r, query)
			if plan.Variants[0].Request != r || plan.Finalizer.Enabled ||
				plan.Variants[0].MergeStrategy != int(merge.StrategyDedup) ||
				plan.UnsupportedWarning != wantWarning ||
				plan.AllowSingleMemberBypass != (wantWarning == "") {
				t.Fatalf("plan: %#v", plan)
			}
		})
	}
}

func TestFinalizeTSMMergeIgnoresUnparsableQuery(t *testing.T) {
	ds := rankDataSet(rankSeries("a", "1", 100), rankSeries("b", "2", 100), rankSeries("c", "3", 100))
	(&Client{}).FinalizeTSMMerge("topk(1, up", ds)
	if got := seriesNames(ds); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Fatalf("series got %v", got)
	}
}
