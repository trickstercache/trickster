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
	"slices"
	"testing"
)

func TestParseLimitRatioAggregation(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantFound   bool
		wantRatio   float64
		wantInner   string
		wantQuery   string
		wantLabels  []string
		wantWithout bool
		wantSortSet bool
		wantSortDir bool
	}{
		{
			name:      "direct",
			query:     "limit_ratio(0.25, up)",
			wantFound: true,
			wantRatio: 0.25,
			wantInner: "up",
			wantQuery: "limit_ratio(0.25, up)",
		},
		{
			name:       "negative exponent and prefix grouping",
			query:      "limit_ratio by (region, job) (-2.5e-1, rate(requests[5m]))",
			wantFound:  true,
			wantRatio:  -0.25,
			wantInner:  "rate(requests[5m])",
			wantQuery:  "limit_ratio by (region, job) (-2.5e-1, rate(requests[5m]))",
			wantLabels: []string{"job", "region"},
		},
		{
			name:        "postfix without grouping",
			query:       "limit_ratio(.5, up) without (pod, instance)",
			wantFound:   true,
			wantRatio:   0.5,
			wantInner:   "up",
			wantQuery:   "limit_ratio(.5, up) without (pod, instance)",
			wantLabels:  []string{"instance", "pod"},
			wantWithout: true,
		},
		{
			name:        "sort desc wrapper",
			query:       "sort_desc(limit_ratio(1, sum by (service) (requests)))",
			wantFound:   true,
			wantRatio:   1,
			wantInner:   "sum by (service) (requests)",
			wantQuery:   "limit_ratio(1, sum by (service) (requests))",
			wantSortSet: true,
			wantSortDir: true,
		},
		{
			name:        "nested sort wrappers use outer direction",
			query:       "sort(sort_desc(limit_ratio(-1, up)))",
			wantFound:   true,
			wantRatio:   -1,
			wantInner:   "up",
			wantQuery:   "limit_ratio(-1, up)",
			wantSortSet: true,
		},
		{
			name:      "inner expression contains commas",
			query:     `limit_ratio(0.5, label_replace(up, "dst", "$1", "src", "(.*)"))`,
			wantFound: true,
			wantRatio: 0.5,
			wantInner: `label_replace(up, "dst", "$1", "src", "(.*)")`,
			wantQuery: `limit_ratio(0.5, label_replace(up, "dst", "$1", "src", "(.*)"))`,
		},
		{name: "lower boundary", query: "limit_ratio(-1, up)", wantFound: true, wantRatio: -1, wantInner: "up", wantQuery: "limit_ratio(-1, up)"},
		{name: "zero", query: "limit_ratio(0, up)", wantFound: true, wantInner: "up", wantQuery: "limit_ratio(0, up)"},
		{name: "upper boundary", query: "limit_ratio(1, up)", wantFound: true, wantRatio: 1, wantInner: "up", wantQuery: "limit_ratio(1, up)"},
		{name: "scalar parameter expression", query: "limit_ratio(scalar(ratio), up)"},
		{name: "positive out of range", query: "limit_ratio(1.01, up)"},
		{name: "negative out of range", query: "limit_ratio(-1.01, up)"},
		{name: "nan", query: "limit_ratio(NaN, up)"},
		{name: "infinity", query: "limit_ratio(+Inf, up)"},
		{name: "missing comma", query: "limit_ratio(0.5 up)"},
		{name: "missing vector", query: "limit_ratio(0.5, )"},
		{name: "extra argument", query: "limit_ratio(0.5, up, down)"},
		{name: "unclosed", query: "limit_ratio(0.5, up"},
		{name: "trailing expression", query: "limit_ratio(0.5, up) + down"},
		{name: "nested below another operator", query: "sum(limit_ratio(0.5, up))"},
		{name: "operator prefix collision", query: "limit_ratio_total(0.5, up)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ParseLimitRatioAggregation(tt.query)
			if found != tt.wantFound {
				t.Fatalf("found got %v want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if got.Ratio != tt.wantRatio {
				t.Errorf("ratio got %v want %v", got.Ratio, tt.wantRatio)
			}
			if got.InnerQuery != tt.wantInner {
				t.Errorf("inner query got %q want %q", got.InnerQuery, tt.wantInner)
			}
			if got.AggregationQuery != tt.wantQuery {
				t.Errorf("aggregation query got %q want %q", got.AggregationQuery, tt.wantQuery)
			}
			if !slices.Equal(got.Grouping.Labels, tt.wantLabels) {
				t.Errorf("labels got %v want %v", got.Grouping.Labels, tt.wantLabels)
			}
			if got.Grouping.Without != tt.wantWithout {
				t.Errorf("without got %v want %v", got.Grouping.Without, tt.wantWithout)
			}
			if got.SortSet != tt.wantSortSet {
				t.Errorf("sort set got %v want %v", got.SortSet, tt.wantSortSet)
			}
			if got.SortDescending != tt.wantSortDir {
				t.Errorf("sort direction got %v want %v", got.SortDescending, tt.wantSortDir)
			}
		})
	}
}
