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

import "testing"

func TestOuterAggregator(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantAgg  string
		wantFind bool
	}{
		// Supportable aggregators
		{name: "sum direct", query: "sum(requests)", wantAgg: "sum", wantFind: true},
		{name: "sum by clause", query: "sum by (region) (requests)", wantAgg: "sum", wantFind: true},
		{name: "sum without clause", query: "sum without (region) (requests)", wantAgg: "sum", wantFind: true},
		{name: "avg direct", query: "avg(cpu_usage)", wantAgg: "avg", wantFind: true},
		{name: "avg by clause", query: "avg by (env) (cpu_usage)", wantAgg: "avg", wantFind: true},
		{name: "count direct", query: "count(up)", wantAgg: "count", wantFind: true},
		{name: "count_values", query: `count_values("version", build_info)`, wantAgg: "count_values", wantFind: true},
		{name: "min", query: "min(latency)", wantAgg: "min", wantFind: true},
		{name: "max", query: "max(latency)", wantAgg: "max", wantFind: true},
		{name: "group", query: "group(metric)", wantAgg: "group", wantFind: true},
		// Unsupported aggregators (OuterAggregator still detects them)
		{name: "stddev", query: "stddev(vals)", wantAgg: "stddev", wantFind: true},
		{name: "stdvar", query: "stdvar(vals)", wantAgg: "stdvar", wantFind: true},
		{name: "quantile", query: "quantile(0.95, latency)", wantAgg: "quantile", wantFind: true},
		{name: "topk", query: "topk(10, requests)", wantAgg: "topk", wantFind: true},
		{name: "bottomk", query: "bottomk(5, requests)", wantAgg: "bottomk", wantFind: true},
		{name: "limitk", query: "limitk(100, series)", wantAgg: "limitk", wantFind: true},
		{name: "limit_ratio", query: "limit_ratio(0.1, series)", wantAgg: "limit_ratio", wantFind: true},
		// Non-aggregator expressions
		{name: "plain metric", query: "http_requests_total", wantAgg: "", wantFind: false},
		{name: "selector with labels", query: `http_requests_total{job="api"}`, wantAgg: "", wantFind: false},
		{name: "rate function", query: "rate(http_requests_total[5m])", wantAgg: "", wantFind: false},
		{name: "avg_over_time is not an aggregator", query: "avg_over_time(cpu[5m])", wantAgg: "", wantFind: false},
		{name: "leading whitespace", query: "  sum(up)", wantAgg: "sum", wantFind: true},
		{name: "uppercase aggregator", query: "SUM(requests)", wantAgg: "sum", wantFind: true},
		{name: "mixed case", query: "Avg(cpu)", wantAgg: "avg", wantFind: true},
		// count_values must match before count
		{name: "count_values not confused with count", query: `count_values("v", m)`, wantAgg: "count_values", wantFind: true},
		// Nested: outer is sum, inner is avg
		{name: "nested sum(avg(...))", query: "sum(avg(cpu_usage))", wantAgg: "sum", wantFind: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAgg, gotFound := OuterAggregator(tt.query)
			if gotFound != tt.wantFind {
				t.Errorf("OuterAggregator(%q) found=%v, want %v", tt.query, gotFound, tt.wantFind)
			}
			if gotAgg != tt.wantAgg {
				t.Errorf("OuterAggregator(%q) = %q, want %q", tt.query, gotAgg, tt.wantAgg)
			}
		})
	}
}

func TestReplaceOuterAggregator(t *testing.T) {
	tests := []struct {
		query       string
		aggregator  string
		replacement string
		want        string
	}{
		{"avg(requests)", "avg", "sum", "sum(requests)"},
		{"avg(requests)", "avg", "count", "count(requests)"},
		{"avg by (region) (requests)", "avg", "sum", "sum by (region) (requests)"},
		{"avg without (region) (requests)", "avg", "count", "count without (region) (requests)"},
		{"  avg(requests)", "avg", "sum", "sum(requests)"},
		// non-matching aggregator is returned unchanged
		{"sum(requests)", "avg", "sum", "sum(requests)"},
	}
	for _, tt := range tests {
		got := ReplaceOuterAggregator(tt.query, tt.aggregator, tt.replacement)
		if got != tt.want {
			t.Errorf("ReplaceOuterAggregator(%q, %q, %q) = %q, want %q",
				tt.query, tt.aggregator, tt.replacement, got, tt.want)
		}
	}
}
