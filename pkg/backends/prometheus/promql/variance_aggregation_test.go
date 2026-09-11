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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

func TestParseVarianceAggregation(t *testing.T) {
	tests := []struct {
		query          string
		operator       string
		inner          string
		grouping       AggregationGrouping
		sortSet        bool
		sortDescending bool
	}{
		{"stddev(up)", aggregation.StdDev, "up", AggregationGrouping{}, false, false},
		{"STDVAR by (zone, job) (rate(x[5m]))", aggregation.StdVar, "rate(x[5m])",
			AggregationGrouping{Labels: []string{"job", "zone"}}, false, false},
		{"stddev(sum(x)) without (instance)", aggregation.StdDev, "sum(x)",
			AggregationGrouping{Labels: []string{"instance"}, Without: true}, false, false},
		{"sort(stdvar without () ({__name__=~\".+\"}))", aggregation.StdVar,
			"{__name__=~\".+\"}", AggregationGrouping{Without: true}, true, false},
		{"sort_desc(stddev by (__name__) (up))", aggregation.StdDev, "up",
			AggregationGrouping{Labels: []string{"__name__"}}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			spec, found := ParseVarianceAggregation(tt.query)
			if !found {
				t.Fatal("expected variance aggregation")
			}
			if spec.Operator != tt.operator || spec.InnerQuery != tt.inner ||
				spec.SortSet != tt.sortSet || spec.SortDescending != tt.sortDescending ||
				!reflect.DeepEqual(spec.Grouping, tt.grouping) {
				t.Fatalf("unexpected parse: %#v", spec)
			}
		})
	}
}

func TestParseVarianceAggregationRejectsOtherShapes(t *testing.T) {
	queries := []string{
		"stddev_over_time(up[5m])",
		"stddev(up) + vector(1)",
		"stdvar()",
		"stdvar(up, down)",
		"stddev by job (up)",
		"sort(sum(up))",
		"sort_desc(stddev(up), down)",
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			if _, found := ParseVarianceAggregation(query); found {
				t.Fatal("unexpected variance aggregation")
			}
		})
	}
}

func TestVarianceVariantQuery(t *testing.T) {
	tests := []struct {
		query    string
		operator string
		want     string
	}{
		{
			query:    "stddev(rate(requests_total[5m]))",
			operator: aggregation.Count,
			want:     "count(clamp(rate(requests_total[5m]), -Inf, +Inf))",
		},
		{
			query:    "stdvar without (instance, pod) (up)",
			operator: aggregation.Average,
			want: "sum without (instance, pod, __trickster_tsm_type__, __trickster_tsm_unit__) " +
				"(label_replace(label_replace(label_replace(avg without (instance, pod) " +
				"(clamp(label_replace(label_replace(up, \"__trickster_tsm_type__\", \"$1\", " +
				"\"__type__\", \"(.*)\"), \"__trickster_tsm_unit__\", \"$1\", \"__unit__\", " +
				"\"(.*)\"), -Inf, +Inf)), \"__type__\", \"$1\", \"__trickster_tsm_type__\", " +
				"\"(.*)\"), \"__unit__\", \"$1\", \"__trickster_tsm_unit__\", \"(.*)\"), " +
				"\"__name__\", \"__trickster_tsm_variance__\", \"__name__\", \".*\"))",
		},
		{
			query:    "stddev(up) by (zone)",
			operator: aggregation.StdVar,
			want:     "stdvar(clamp(up, -Inf, +Inf)) by (zone)",
		},
		{
			query:    "stddev by (__name__, job) (rate(x[5m]))",
			operator: aggregation.Count,
			want: "sum by (__name__, job) (label_replace(count by (__trickster_tsm_name__, job) " +
				"(clamp(label_replace(rate(x[5m]), \"__trickster_tsm_name__\", \"$1\", \"__name__\", \"(.*)\"), " +
				"-Inf, +Inf)), \"__name__\", \"$1\", \"__trickster_tsm_name__\", \"(.*)\"))",
		},
		{
			query:    "stdvar by (__type__, __unit__) (x)",
			operator: aggregation.StdVar,
			want: "sum by (__type__, __unit__) (label_replace(label_replace(label_replace(stdvar by " +
				"(__trickster_tsm_type__, __trickster_tsm_unit__) (clamp(label_replace(label_replace(x, " +
				"\"__trickster_tsm_type__\", \"$1\", \"__type__\", \"(.*)\"), \"__trickster_tsm_unit__\", " +
				"\"$1\", \"__unit__\", \"(.*)\"), -Inf, +Inf)), \"__type__\", \"$1\", " +
				"\"__trickster_tsm_type__\", \"(.*)\"), \"__unit__\", \"$1\", " +
				"\"__trickster_tsm_unit__\", \"(.*)\"), \"__name__\", " +
				"\"__trickster_tsm_variance__\", \"__name__\", \".*\"))",
		},
	}
	for _, tt := range tests {
		t.Run(tt.query+"/"+tt.operator, func(t *testing.T) {
			spec, found := ParseVarianceAggregation(tt.query)
			if !found {
				t.Fatal("expected variance aggregation")
			}
			if got := VarianceVariantQuery(spec, tt.operator); got != tt.want {
				t.Fatalf("unexpected query\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}

func TestVarianceVariantQueryAvoidsTemporaryLabelCollision(t *testing.T) {
	const query = "stdvar by (__name__, __trickster_tsm_name__) (x)"
	spec, found := ParseVarianceAggregation(query)
	if !found {
		t.Fatal("expected variance aggregation")
	}
	want := "sum by (__name__, __trickster_tsm_name__) (label_replace(count by " +
		"(__trickster_tsm_name___, __trickster_tsm_name__) (clamp(label_replace(x, " +
		"\"__trickster_tsm_name___\", \"$1\", \"__name__\", \"(.*)\"), -Inf, +Inf)), " +
		"\"__name__\", \"$1\", \"__trickster_tsm_name___\", \"(.*)\"))"
	if got := VarianceVariantQuery(spec, aggregation.Count); got != want {
		t.Fatalf("unexpected query\n got: %s\nwant: %s", got, want)
	}
}

func TestVarianceVariantQueryWithoutPreservesNonExcludedMetadata(t *testing.T) {
	tests := []struct {
		query         string
		wantTemporary string
		reject        string
	}{
		{
			query:         "stdvar without (__type__, instance) (x)",
			wantTemporary: "__trickster_tsm_unit__",
			reject:        "__trickster_tsm_type__",
		},
		{
			query:         "stdvar without (__unit__, instance) (x)",
			wantTemporary: "__trickster_tsm_type__",
			reject:        "__trickster_tsm_unit__",
		},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			spec, found := ParseVarianceAggregation(tt.query)
			if !found {
				t.Fatal("expected variance aggregation")
			}
			got := VarianceVariantQuery(spec, aggregation.Count)
			if !strings.Contains(got, tt.wantTemporary) || strings.Contains(got, tt.reject) {
				t.Fatalf("unexpected metadata rewrite: %s", got)
			}
		})
	}
}
