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

func TestCompleteOuterAggregation(t *testing.T) {
	tests := []struct {
		query string
		agg   aggregation.Operator
		input string
	}{
		{"sum(up)", aggregation.Sum, "up"},
		{"((sum(up)))", aggregation.Sum, "up"},
		{"SUM(up) # total", aggregation.Sum, "up"},
		{"sum by (service) (rate(requests[5m]))", aggregation.Sum, "rate(requests[5m])"},
		{"avg(requests) without (instance)", aggregation.Average, "requests"},
		{`count_values("code", requests)`, aggregation.CountValues, "requests"},
		{"topk(5, (up + down))", aggregation.TopK, "up + down"},
		{"count_values(\"v\", m)", aggregation.CountValues, "m"},
		{"sum(avg(cpu_usage))", aggregation.Sum, "avg(cpu_usage)"},
		{"sum(up) + vector(1)", "", ""},
		{"(sum(up)) + vector(1)", "", ""},
		{"((sum(up) + vector(1)))", "", ""},
		{"rate(sum(up)[5m:])", "", ""},
		{"avg_over_time(cpu[5m])", "", ""},
		{"up", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			agg, input, found := CompleteOuterAggregation(mustParse(t, tt.query))
			if agg != tt.agg || input.String() != tt.input || found != (tt.agg != "") {
				t.Fatalf("got (%q, %q, %v) want (%q, %q)", agg, input.String(), found,
					tt.agg, tt.input)
			}
		})
	}
}

func TestReplaceOuterAggregator(t *testing.T) {
	tests := []struct {
		query       string
		aggregator  aggregation.Operator
		replacement aggregation.Operator
		want        string
	}{
		{"avg(requests)", aggregation.Average, aggregation.Sum, "sum(requests)"},
		{"avg(requests)", aggregation.Average, aggregation.Count, "count(requests)"},
		{"avg by (region) (requests)", aggregation.Average, aggregation.Sum, "sum by (region) (requests)"},
		{"avg without (region) (requests)", aggregation.Average, aggregation.Count, "count without (region) (requests)"},
		{"AVG(requests) by (region)", aggregation.Average, aggregation.Sum, "sum(requests) by (region)"},
		{"((avg(requests)))", aggregation.Average, aggregation.Sum, "sum(requests)"},
		{"  avg(requests) # note", aggregation.Average, aggregation.Sum, "sum(requests)"},
		// non-matching aggregator is returned unchanged
		{"sum(requests)", aggregation.Average, aggregation.Sum, "sum(requests)"},
		{"avg(requests) + 1", aggregation.Average, aggregation.Sum, "avg(requests) + 1"},
	}
	for _, tt := range tests {
		got := ReplaceOuterAggregator(mustParse(t, tt.query), tt.aggregator, tt.replacement)
		if got != tt.want {
			t.Errorf("ReplaceOuterAggregator(%q, %q, %q) = %q, want %q",
				tt.query, tt.aggregator, tt.replacement, got, tt.want)
		}
	}
}

func TestAggregationGrouping(t *testing.T) {
	tests := map[string]AggregationGrouping{
		"sum(up)":                            {},
		"sum by () (up)":                     {},
		"sum without () (up)":                {Without: true},
		"sum by (zone, job, zone) (up)":      {Labels: []string{"job", "zone"}},
		"sum(up) without (pod, instance)":    {Labels: []string{"instance", "pod"}, Without: true},
		`sum by ("service.name", job,) (up)`: {Labels: []string{"job", "service.name"}},
	}
	for query, want := range tests {
		spec, found := parseOuterAggregation(mustParse(t, query), aggregation.Sum)
		if !found || !reflect.DeepEqual(spec.Grouping, want) {
			t.Errorf("grouping for %q = %#v, want %#v", query, spec.Grouping, want)
		}
	}
}

func TestParseZeroFallback(t *testing.T) {
	tests := []struct {
		query           string
		found           bool
		operator        string
		input           string
		grouping        AggregationGrouping
		defaultMatching bool
	}{
		{"count(up) or vector(0)", true, aggregation.Count, "up", AggregationGrouping{}, true},
		{"(count((up))) or (vector((0)))", true, aggregation.Count, "up", AggregationGrouping{}, true},
		{"sum by (job) (up + down) or vector(-0)", true, aggregation.Sum, "up + down",
			AggregationGrouping{Labels: []string{"job"}}, true},
		{"count(up) or ignoring () vector(0)", true, aggregation.Count, "up", AggregationGrouping{}, true},
		{"count(up) or on () vector(0)", true, aggregation.Count, "up", AggregationGrouping{}, false},
		{"max(up) or ignoring (job) vector(0)", true, aggregation.Maximum, "up", AggregationGrouping{}, false},
		{"count(up) or vector(1)", false, "", "", AggregationGrouping{}, false},
		{"count(up) or vector(scalar(up))", false, "", "", AggregationGrouping{}, false},
		{"count(up) or absent(up)", false, "", "", AggregationGrouping{}, false},
		{"count(up) and vector(0)", false, "", "", AggregationGrouping{}, false},
		{"vector(0) or count(up)", false, "", "", AggregationGrouping{}, false},
		{"rate(up[5m]) or vector(0)", false, "", "", AggregationGrouping{}, false},
		{"count(up)", false, "", "", AggregationGrouping{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, found := ParseZeroFallback(mustParse(t, tt.query))
			if found != tt.found || got.Operator != tt.operator || got.Input.String() != tt.input ||
				!reflect.DeepEqual(got.Grouping, tt.grouping) || got.DefaultMatching != tt.defaultMatching {
				t.Fatalf("got (%q, %q, %#v, %v, found=%v)", got.Operator, got.Input.String(),
					got.Grouping, got.DefaultMatching, found)
			}
		})
	}
}
