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
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

func TestParsePresenceAggregation(t *testing.T) {
	const (
		count       = aggregation.Count
		group       = aggregation.Group
		countPods   = "count by (pod) (up)"
		countUp     = "count(up)"
		nsLabel     = "ns"
		withoutBody = `count(up{job="a"}) without (instance)`
	)
	// wantQuery defaults to the query, and wantInner is empty when nothing matches
	tests := []struct {
		name      string
		query     string
		wantOp    string
		wantInner string
		wantQuery string
		wantGroup AggregationGrouping
		wantSort  bool
		wantDesc  bool
	}{
		{name: "reported", query: "count(count by (pod_name) (X))", wantOp: count, wantInner: "count by (pod_name) (X)"},
		{name: "group inner", query: "count(group by (pod) (up))", wantOp: count, wantInner: "group by (pod) (up)"},
		{name: "group outer", query: "group(" + countPods + ")", wantOp: group, wantInner: countPods},
		{
			name: "outer grouping", query: "count by (ns) (count by (pod, ns) (up))", wantOp: count,
			wantInner: "count by (pod, ns) (up)", wantGroup: AggregationGrouping{Labels: []string{nsLabel}},
		},
		{
			name: "without", query: "count(" + withoutBody + ") without (pod)", wantOp: count,
			wantInner: withoutBody, wantGroup: AggregationGrouping{Labels: []string{"pod"}, Without: true},
		},
		{name: "ungrouped inner", query: "count(" + countUp + ")", wantOp: count, wantInner: countUp},
		{
			name: "parentheses and comment", query: "count( (count by (pod) ((up))) ) # pods", wantOp: count,
			wantInner: "count by (pod) ((up))", wantQuery: "count( (count by (pod) ((up))) )",
		},
		{
			name: "offset and at", query: "count(count by (pod) (up offset 5m @ 300))", wantOp: count,
			wantInner: "count by (pod) (up offset 5m @ 300)",
		},
		{
			name: "sort wrapper", query: "sort_desc(count by (ns) (count by (ns, pod) (up)))", wantOp: count,
			wantInner: "count by (ns, pod) (up)", wantQuery: "count by (ns) (count by (ns, pod) (up))",
			wantGroup: AggregationGrouping{Labels: []string{nsLabel}}, wantSort: true, wantDesc: true,
		},
		{name: "function input", query: "count(count by (pod) (rate(x[5m])))"},
		{name: "filter input", query: "count(count by (pod) (up == 1))"},
		{name: "value inner", query: "count(sum by (pod) (up))"},
		{name: "count_values inner", query: `count(count_values("v", up))`},
		{name: "value outer", query: "sum(" + countPods + ")"},
		{name: "count_values outer", query: `count_values("v", ` + countPods + ")"},
		{name: "single aggregation", query: countUp},
		{name: "triple nesting", query: "count(count by (pod) (count by (pod, c) (up)))"},
		{name: "binary wrapper", query: "count(" + countPods + ") > 1"},
		{name: "not an aggregation", query: "up"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expr, err := Parse(test.query)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.query, err)
			}
			got, found := ParsePresenceAggregation(expr)
			if wantFound := test.wantInner != ""; found != wantFound {
				t.Fatalf("found: got %v want %v", found, wantFound)
			}
			if !found {
				return
			}
			wantQuery := test.wantQuery
			if wantQuery == "" {
				wantQuery = test.query
			}
			if got.Operator != test.wantOp || got.Inner.String() != test.wantInner ||
				got.AggregationQuery != wantQuery || got.SortSet != test.wantSort ||
				got.SortDescending != test.wantDesc {
				t.Fatalf("got %+v (inner %q)", got, got.Inner.String())
			}
			if !reflect.DeepEqual(got.Grouping, test.wantGroup) {
				t.Fatalf("grouping: got %+v want %+v", got.Grouping, test.wantGroup)
			}
		})
	}
}
