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

	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/promql/parser/posrange"
)

func mustParse(t *testing.T, query string) Expr {
	t.Helper()
	e, err := Parse(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}
	return e
}

func parseOrZero(query string) Expr {
	e, _ := Parse(query)
	return e
}

func TestParse(t *testing.T) {
	for _, query := range []string{"", "   ", "sum(", "sum by service (up)", "topk(k, up)"} {
		t.Run(query, func(t *testing.T) {
			if _, err := Parse(query); err == nil {
				t.Fatal("expected parse error")
			}
		})
	}
	for _, query := range []string{"limitk(2, up)", "limit_ratio(0.5, up)", `sort_by_label(up, "job")`} {
		t.Run(query, func(t *testing.T) {
			if _, err := Parse(query); err != nil {
				t.Fatalf("experimental syntax rejected: %v", err)
			}
		})
	}
}

func TestExprString(t *testing.T) {
	tests := map[string]string{
		"sum(up)":                   "sum(up)",
		"  ((sum(up)))  ":           "sum(up)",
		"sum(up) # trailing":        "sum(up)",
		"# leading\n(sum(up))":      "sum(up)",
		"SUM(up) BY (job)":          "SUM(up) BY (job)",
		"(count(up)) or vector(0)":  "(count(up)) or vector(0)",
		"up offset 1h @ 100 # note": "up offset 1h @ 100",
	}
	for query, want := range tests {
		if got := mustParse(t, query).String(); got != want {
			t.Errorf("String(%q) = %q, want %q", query, got, want)
		}
	}
	if got := (Expr{}).String(); got != "" {
		t.Errorf("empty expression String() = %q", got)
	}
	outOfRange := Expr{
		node:   &parser.NumberLiteral{PosRange: posrange.PositionRange{Start: 2, End: 9}},
		source: "1",
	}
	if got := outOfRange.String(); got != "" {
		t.Errorf("out-of-range expression String() = %q", got)
	}
}

func TestIsScalar(t *testing.T) {
	tests := map[string]bool{
		"scalar(count(up))":           true,
		" scalar (sum(up)) ":          true,
		"SCALAR(sum(up))":             false,
		`scalar(count({label="("}))`:  true,
		"((scalar(count(up))))":       true,
		"scalar(count(up)) + 1":       true,
		"1 + scalar(count(up)) * 2":   true,
		"-scalar(count(up))":          true,
		"1e-3 + scalar(count(up))":    true,
		"scalar(count(up)) + 0XAf":    true,
		"scalar(count(up)) + 1h30m":   true,
		"scalar(up) == bool 1":        true,
		"time()":                      true,
		"pi()":                        true,
		"42":                          true,
		"scalar(up) + up":             false,
		"vector(1)":                   false,
		"sum(up)":                     false,
		"rate(requests_total[5m])":    false,
		"scalar(up) and scalar(down)": false,
	}
	for query, want := range tests {
		if got := parseOrZero(query).IsScalar(); got != want {
			t.Errorf("IsScalar(%q) = %v, want %v", query, got, want)
		}
	}
}

func TestContainsAggregation(t *testing.T) {
	tests := map[string]bool{
		"sum(up)":                                true,
		"rate(sum by (service) (requests)[5m:])": true,
		"(topk(2, up))":                          true,
		"abs(sum(up))":                           true,
		"rate(up[5m])":                           false,
		`label_replace(up, "note", "sum(up)", "src", ".*")`: false,
		`sum_total{operation="sum"}`:                        false,
	}
	for query, want := range tests {
		if got := mustParse(t, query).ContainsAggregation(); got != want {
			t.Errorf("ContainsAggregation(%q) = %v, want %v", query, got, want)
		}
	}
	if (Expr{}).ContainsAggregation() {
		t.Error("empty expression contains an aggregation")
	}
}

func TestContainsBinaryExpression(t *testing.T) {
	tests := map[string]bool{
		"up + on (job) group_left down":         true,
		"up-2":                                  true,
		"metric1e-2":                            true,
		"rate(requests[5m]) / rate(errors[5m])": true,
		"up and on (job) ready":                 true,
		"up unless down":                        true,
		"up == bool down":                       true,
		"up atan2 down":                         true,
		"rate(up[5m])":                          false,
		"rate(up[5m] offset -1h)":               false,
		"clamp_min(up, -1e-3)":                  false,
		"1e-3":                                  false,
		`label_replace(up{method=~"GET|POST"}, "dst", "a+b", "src", ".*")`: false,
	}
	for query, want := range tests {
		if got := mustParse(t, query).ContainsBinaryExpression(); got != want {
			t.Errorf("ContainsBinaryExpression(%q) = %v, want %v", query, got, want)
		}
	}
}

func TestNonShardLocalFunction(t *testing.T) {
	tests := map[string]string{
		"absent(up)":                       "absent",
		"sum(absent_over_time(up[5m]))":    "absent_over_time",
		"histogram_quantile(0.9, buckets)": "histogram_quantile",
		"scalar(up)":                       "scalar",
		"sort_desc(rate(up[5m]))":          "sort_desc",
		`sort_by_label(up, "job")`:         "sort_by_label",
		"absent_total":                     "",
		"rate(up[5m])":                     "",
		`label_replace(up, "n", "absent(up)", "src", ".*")`: "",
	}
	for query, want := range tests {
		got, found := mustParse(t, query).NonShardLocalFunction()
		if got != want || found != (want != "") {
			t.Errorf("NonShardLocalFunction(%q) = (%q, %v), want %q", query, got, found, want)
		}
	}
}

func TestScalarLiteral(t *testing.T) {
	tests := []struct {
		query string
		want  float64
		valid bool
	}{
		{"topk(5, up)", 5, true},
		{"topk((5), up)", 5, true},
		{"topk(-(5), up)", -5, true},
		{"topk(+(0x10), up)", 16, true},
		{"limitk(010, up)", 8, true},
		{"limitk(1500ms, up)", 1.5, true},
		{"topk(scalar(k), up)", 0, false},
		{"topk(-scalar(k), up)", 0, false},
		{"topk(1 + 1, up)", 0, false},
	}
	for _, tt := range tests {
		agg, ok := mustParse(t, tt.query).node.(*parser.AggregateExpr)
		if !ok {
			t.Fatalf("%q is not an aggregation", tt.query)
		}
		got, valid := scalarLiteral(agg.Param)
		if valid != tt.valid || valid && got != tt.want {
			t.Errorf("scalarLiteral(%q) = (%v, %v), want (%v, %v)",
				tt.query, got, valid, tt.want, tt.valid)
		}
	}
	if got, valid := scalarLiteral(&parser.NumberLiteral{Val: math.Inf(-1)}); !valid || !math.IsInf(got, -1) {
		t.Errorf("scalarLiteral(-Inf) = (%v, %v)", got, valid)
	}
}
