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

package promql

import (
	"math"
	"testing"
)

func TestParseQuantileAggregation(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantFound  bool
		wantPhi    float64
		wantInner  string
		wantQuery  string
		wantGroup  AggregationGrouping
		wantSort   bool
		wantDesc   bool
		wantPhiNaN bool
	}{
		{name: "direct", query: "quantile(0.9, up)", wantFound: true, wantPhi: 0.9,
			wantInner: "up", wantQuery: "quantile(0.9, up)"},
		{name: "prefix grouping", query: "quantile by (region, job) (.5, rate(x[5m]))",
			wantFound: true, wantPhi: 0.5, wantInner: "rate(x[5m])",
			wantQuery: "quantile by (region, job) (.5, rate(x[5m]))",
			wantGroup: AggregationGrouping{Labels: []string{"job", "region"}}},
		{name: "postfix grouping", query: "quantile(-0.1, up) without (pod, instance)",
			wantFound: true, wantPhi: -0.1, wantInner: "up",
			wantQuery: "quantile(-0.1, up) without (pod, instance)",
			wantGroup: AggregationGrouping{Labels: []string{"instance", "pod"}, Without: true}},
		{name: "sort wrapper", query: "sort_desc(quantile(1.1, sum by (service) (x)))",
			wantFound: true, wantPhi: 1.1, wantInner: "sum by (service) (x)",
			wantQuery: "quantile(1.1, sum by (service) (x))", wantSort: true, wantDesc: true},
		{name: "nested wrappers", query: "sort(sort_desc(quantile(0, up)))",
			wantFound: true, wantInner: "up", wantQuery: "quantile(0, up)", wantSort: true},
		{name: "nan", query: "quantile(NaN, up)", wantFound: true, wantPhiNaN: true,
			wantInner: "up", wantQuery: "quantile(NaN, up)"},
		{name: "lowercase nan", query: "quantile(nan, up)", wantFound: true, wantPhiNaN: true,
			wantInner: "up", wantQuery: "quantile(nan, up)"},
		{name: "negative infinity", query: "quantile(-Inf, up)", wantFound: true,
			wantPhi: math.Inf(-1), wantInner: "up", wantQuery: "quantile(-Inf, up)"},
		{name: "positive infinity", query: "quantile(+Inf, up)", wantFound: true,
			wantPhi: math.Inf(1), wantInner: "up", wantQuery: "quantile(+Inf, up)"},
		{name: "duration literal", query: "quantile(500ms, up)", wantFound: true,
			wantPhi: 0.5, wantInner: "up", wantQuery: "quantile(500ms, up)"},
		{name: "composite duration", query: "quantile(1m30s, up)", wantFound: true,
			wantPhi: 90, wantInner: "up", wantQuery: "quantile(1m30s, up)"},
		{name: "negative duration", query: "quantile(-1m, up)", wantFound: true,
			wantPhi: -60, wantInner: "up", wantQuery: "quantile(-1m, up)"},
		{name: "underscored numeric", query: "quantile(1_000e-3, up)", wantFound: true,
			wantPhi: 1, wantInner: "up", wantQuery: "quantile(1_000e-3, up)"},
		{name: "hexadecimal integer", query: "quantile(0x1, up)", wantFound: true,
			wantPhi: 1, wantInner: "up", wantQuery: "quantile(0x1, up)"},
		{name: "octal integer", query: "quantile(01, up)", wantFound: true,
			wantPhi: 1, wantInner: "up", wantQuery: "quantile(01, up)"},
		{name: "invalid underscore", query: "quantile(1__0, up)"},
		{name: "invalid underscored duration", query: "quantile(1_0m, up)"},
		{name: "invalid infinity spelling", query: "quantile(Infinity, up)"},
		{name: "invalid hexadecimal float", query: "quantile(0x1p-1, up)"},
		{name: "scalar parameter", query: "quantile(scalar(phi), up)"},
		{name: "missing comma", query: "quantile(0.5 up)"},
		{name: "missing vector", query: "quantile(0.5, )"},
		{name: "extra argument", query: "quantile(0.5, up, down)"},
		{name: "unclosed", query: "quantile(0.5, up"},
		{name: "trailing expression", query: "quantile(0.5, up) + down"},
		{name: "nested", query: "sum(quantile(0.5, up))"},
		{name: "prefix collision", query: "quantile_total(0.5, up)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ParseQuantileAggregation(tt.query)
			if found != tt.wantFound {
				t.Fatalf("found got %t want %t: %#v", found, tt.wantFound, got)
			}
			if !found {
				return
			}
			if tt.wantPhiNaN {
				if !math.IsNaN(got.Phi) {
					t.Fatalf("phi got %v want NaN", got.Phi)
				}
			} else if got.Phi != tt.wantPhi {
				t.Errorf("phi got %v want %v", got.Phi, tt.wantPhi)
			}
			if got.InnerQuery != tt.wantInner || got.AggregationQuery != tt.wantQuery {
				t.Errorf("queries got inner=%q aggregation=%q", got.InnerQuery, got.AggregationQuery)
			}
			if got.Grouping.Without != tt.wantGroup.Without ||
				!slicesEqual(got.Grouping.Labels, tt.wantGroup.Labels) {
				t.Errorf("grouping got %#v want %#v", got.Grouping, tt.wantGroup)
			}
			if got.SortSet != tt.wantSort || got.SortDescending != tt.wantDesc {
				t.Errorf("sort got set=%t desc=%t", got.SortSet, got.SortDescending)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
