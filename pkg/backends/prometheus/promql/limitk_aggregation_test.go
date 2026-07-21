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

func TestParseLimitKAggregation(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantFound bool
		wantK     int64
		wantInner string
		wantQuery string
		wantGroup AggregationGrouping
		wantSort  bool
		wantDesc  bool
	}{
		{name: "direct", query: "limitk(2, up)", wantFound: true, wantK: 2,
			wantInner: "up", wantQuery: "limitk(2, up)"},
		{name: "fraction truncates", query: "limitk(1.9, up)", wantFound: true, wantK: 1,
			wantInner: "up", wantQuery: "limitk(1.9, up)"},
		{name: "fraction below one", query: "limitk(0.9, up)", wantFound: true,
			wantInner: "up", wantQuery: "limitk(0.9, up)"},
		{name: "duration truncates", query: "limitk(1500ms, up)", wantFound: true, wantK: 1,
			wantInner: "up", wantQuery: "limitk(1500ms, up)"},
		{name: "hexadecimal integer", query: "limitk(0x2, up)", wantFound: true, wantK: 2,
			wantInner: "up", wantQuery: "limitk(0x2, up)"},
		{name: "octal integer", query: "limitk(010, up)", wantFound: true, wantK: 8,
			wantInner: "up", wantQuery: "limitk(010, up)"},
		{name: "prefix grouping", query: "limitk by (region, job) (3, rate(x[5m]))",
			wantFound: true, wantK: 3, wantInner: "rate(x[5m])",
			wantQuery: "limitk by (region, job) (3, rate(x[5m]))",
			wantGroup: AggregationGrouping{Labels: []string{"job", "region"}}},
		{name: "postfix grouping", query: "limitk(4, up) without (pod, instance)",
			wantFound: true, wantK: 4, wantInner: "up",
			wantQuery: "limitk(4, up) without (pod, instance)",
			wantGroup: AggregationGrouping{Labels: []string{"instance", "pod"}, Without: true}},
		{name: "sort wrapper", query: "sort_desc(limitk(5, sum by (service) (x)))",
			wantFound: true, wantK: 5, wantInner: "sum by (service) (x)",
			wantQuery: "limitk(5, sum by (service) (x))", wantSort: true, wantDesc: true},
		{name: "nested wrappers", query: "sort(sort_desc(limitk(1, up)))",
			wantFound: true, wantK: 1, wantInner: "up", wantQuery: "limitk(1, up)", wantSort: true},
		{name: "largest accepted float", query: "limitk(9223372036854773760, up)",
			wantFound: true, wantK: 9223372036854773760, wantInner: "up",
			wantQuery: "limitk(9223372036854773760, up)"},
		{name: "negative", query: "limitk(-1, up)"},
		{name: "nan", query: "limitk(NaN, up)"},
		{name: "infinity", query: "limitk(+Inf, up)"},
		{name: "binary prefix", query: "limitk(0b10, up)"},
		{name: "hexadecimal float", query: "limitk(0x1p2, up)"},
		{name: "underscored duration", query: "limitk(1_0s, up)"},
		{name: "overflow boundary", query: "limitk(9223372036854774784, up)"},
		{name: "rounded overflow", query: "limitk(9223372036854775807, up)"},
		{name: "scalar expression", query: "limitk(scalar(k), up)"},
		{name: "missing comma", query: "limitk(2 up)"},
		{name: "missing vector", query: "limitk(2, )"},
		{name: "extra argument", query: "limitk(2, up, down)"},
		{name: "unclosed", query: "limitk(2, up"},
		{name: "trailing expression", query: "limitk(2, up) + down"},
		{name: "nested", query: "sum(limitk(2, up))"},
		{name: "prefix collision", query: "limitk_total(2, up)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := ParseLimitKAggregation(tt.query)
			if found != tt.wantFound {
				t.Fatalf("found got %t want %t: %#v", found, tt.wantFound, got)
			}
			if !found {
				return
			}
			if got.K != tt.wantK || got.InnerQuery != tt.wantInner ||
				got.AggregationQuery != tt.wantQuery {
				t.Errorf("got k=%d inner=%q aggregation=%q", got.K, got.InnerQuery, got.AggregationQuery)
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
