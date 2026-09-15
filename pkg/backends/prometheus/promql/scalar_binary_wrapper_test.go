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
	"reflect"
	"testing"
)

type scalarBinaryHistogramOperations struct {
	operators []string
	scalars   []float64
}

func (o *scalarBinaryHistogramOperations) ApplyScalarBinary(value any,
	operator string, scalar float64, _ bool,
) (any, bool) {
	o.operators = append(o.operators, operator)
	o.scalars = append(o.scalars, scalar)
	return value, operator != "+"
}

func TestParseScalarBinaryWrapper(t *testing.T) {
	tests := map[string]string{
		"sum(up) * 100":                 "sum(up)",
		"100 / ((count(up) + 2))":       "count(up)",
		"sum(up) > bool 0":              "sum(up)",
		"(count(up) or vector(0)) * 10": "count(up) or vector(0)",
		"-sum(up)":                      "sum(up)",
		"time() - max(timestamp(up))":   "max(timestamp(up))",
		"sum(up) * -time()":             "sum(up)",
		"-2 * sum(up)":                  "sum(up)",
	}
	for query, wantInner := range tests {
		wrapper, found := ParseScalarBinaryWrapper(mustParse(t, query))
		if !found || wrapper.Inner.String() != wantInner {
			t.Errorf("ParseScalarBinaryWrapper(%q) = (%q, %v), want %q",
				query, wrapper.Inner.String(), found, wantInner)
		}
	}
	for _, query := range []string{
		"up * 2",
		"sum(up) * scalar(foo)",
		"sum(up) + vector(1)",
		"abs(sum(up))",
		"-time()",
	} {
		if _, found := ParseScalarBinaryWrapper(mustParse(t, query)); found {
			t.Errorf("ParseScalarBinaryWrapper(%q) unexpectedly matched", query)
		}
	}
}

func TestScalarBinaryWrapperApplyFloat(t *testing.T) {
	tests := []struct {
		query string
		value float64
		time  float64
		want  float64
		keep  bool
	}{
		{"sum(up) * 100 + 2", 3, 0, 302, true},
		{"100 / sum(up)", 4, 0, 25, true},
		{"10 - sum(up)", 3, 0, 7, true},
		{"sum(up) ^ 2", 3, 0, 9, true},
		{"sum(up) % 2", 3, 0, 1, true},
		{"sum(up) atan2 2", 3, 0, math.Atan2(3, 2), true},
		{"sum(up) == 3", 3, 0, 3, true},
		{"sum(up) != 3", 2, 0, 2, true},
		{"sum(up) < 3", 2, 0, 2, true},
		{"sum(up) >= 3", 3, 0, 3, true},
		{"sum(up) <= 3", 3, 0, 3, true},
		{"sum(up) > 0", 2, 0, 2, true},
		{"sum(up) > 0", -2, 0, 0, false},
		{"sum(up) > bool 0", -2, 0, 0, true},
		{"0 < bool sum(up)", 2, 0, 1, true},
		{"-sum(up)", 3, 0, -3, true},
		{"time() - max(timestamp(up))", 90, 100, 10, true},
		{"sum(up) * -time()", 2, 3, -6, true},
	}
	for _, test := range tests {
		wrapper, found := ParseScalarBinaryWrapper(mustParse(t, test.query))
		if !found {
			t.Fatalf("%q did not parse as a scalar binary wrapper", test.query)
		}
		got, keep := wrapper.ApplyFloat(test.value, test.time)
		if got != test.want || keep != test.keep {
			t.Errorf("ApplyFloat(%q, %v) = (%v, %v), want (%v, %v)",
				test.query, test.value, got, keep, test.want, test.keep)
		}
	}
}

func TestScalarBinaryWrapperApplyHistogram(t *testing.T) {
	wrapper, found := ParseScalarBinaryWrapper(mustParse(t, "sum(up) * 2 / 4"))
	if !found {
		t.Fatal("histogram wrapper not found")
	}
	if _, keep := wrapper.ApplyHistogram("histogram", nil, 0); keep {
		t.Fatal("nil histogram operations unexpectedly handled the value")
	}
	operations := &scalarBinaryHistogramOperations{}
	got, keep := wrapper.ApplyHistogram("histogram", operations, 0)
	if got != "histogram" || !keep ||
		!reflect.DeepEqual(operations.operators, []string{"*", "/"}) {
		t.Fatalf("got (%v, %v, %v)", got, keep, operations.operators)
	}

	wrapper, found = ParseScalarBinaryWrapper(mustParse(t, "sum(up) * 2 + 1"))
	if !found {
		t.Fatal("rejecting histogram wrapper not found")
	}
	if _, keep := wrapper.ApplyHistogram("histogram", operations, 0); keep {
		t.Fatal("unsupported histogram operation unexpectedly kept the value")
	}

	wrapper, found = ParseScalarBinaryWrapper(mustParse(t, "time() * sum(up)"))
	if !found {
		t.Fatal("evaluation-time histogram wrapper not found")
	}
	operations = &scalarBinaryHistogramOperations{}
	if _, keep := wrapper.ApplyHistogram("histogram", operations, 123.5); !keep ||
		!reflect.DeepEqual(operations.scalars, []float64{123.5}) {
		t.Fatalf("evaluation-time scalars got %v", operations.scalars)
	}
}

func TestScalarBinaryWrapperDropsMetricName(t *testing.T) {
	tests := map[string]bool{
		"sum(up) > 0":      false,
		"sum(up) > bool 0": true,
		"sum(up) * 2":      true,
		"-sum(up)":         true,
	}
	for query, want := range tests {
		wrapper, found := ParseScalarBinaryWrapper(mustParse(t, query))
		if !found || wrapper.DropsMetricName() != want {
			t.Errorf("DropsMetricName(%q) = %v, want %v", query,
				wrapper.DropsMetricName(), want)
		}
	}
}

func TestScalarBinaryWrapperUsesEvaluationTime(t *testing.T) {
	for query, want := range map[string]bool{
		"sum(up) * 2":                 false,
		"time() - max(timestamp(up))": true,
	} {
		wrapper, found := ParseScalarBinaryWrapper(mustParse(t, query))
		if !found || wrapper.UsesEvaluationTime() != want {
			t.Errorf("UsesEvaluationTime(%q) = %v, want %v", query,
				wrapper.UsesEvaluationTime(), want)
		}
	}
}
