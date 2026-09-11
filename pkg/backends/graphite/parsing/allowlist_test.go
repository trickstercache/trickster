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

package parsing

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		in       string
		step     StepEffect
		fixed    time.Duration
		shift    time.Duration
		accel    bool
		reason   string
		offender string
		leaves   int
	}{
		{"a.b", StepInherit, 0, 0, true, "", "", 1},
		{"aliasByNode(dev.fast.requests.*.count, 3)", StepInherit, 0, 0, true, "", "", 1},
		{"alias(sumSeries(dev.fast.requests.*.count), 'total')", StepInherit, 0, 0, true, "", "", 1},
		{`aliasSub(aliasByNode(a.*.p99, 3), "(^.*$)", "\1 A")`, StepInherit, 0, 0, true, "", "", 1},
		{"sumSeries(a.*, b.*)", StepInherit, 0, 0, true, "", "", 2},
		{"divideSeries(sumSeries(a.*), sumSeries(b.*))", StepInherit, 0, 0, true, "", "", 2},
		{"scale(offset(a.b, 1), 2) | alias('x')", StepInherit, 0, 0, true, "", "", 1},
		{"removeAboveValue(a.b, 100)", StepInherit, 0, 0, true, "", "", 1},
		{"consolidateBy(a.b, 'max')", StepInherit, 0, 0, true, "", "", 1},
		{"exclude(a.*, 'foo')", StepInherit, 0, 0, true, "", "", 1},
		// step-fixing
		{"summarize(a.b, '1h')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, 1h, sum)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1}, // `1h`, `sum` parse as paths but are not leaves
		{"summarize(a.b, '1h', 'sum', false)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, '1h', 'sum', alignToFrom=false)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, '1h', 'sum', true)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, '1h', 'sum', alignToFrom=true)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, '1h', 'sum', 'true')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, '1h', 'sum', 1)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"summarize(a.b, '1h', 'sum', alignToFrom=a.b)", StepUnknown, 0, 0, false, ReasonParseError, "summarize", 1},
		{"summarize(a.b, '5m')", StepUnknown, 0, 0, false, ReasonParseError, "summarize", 1},
		{"summarize(a.b, 5)", StepUnknown, 0, 0, false, ReasonParseError, "summarize", 1},
		{"summarize(a.b)", StepUnknown, 0, 0, false, ReasonParseError, "summarize", 1},
		{"scale(summarize(a.b, '1h'), 2)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"sumSeries(summarize(a.b, '1h'), summarize(c.d, '1h'))", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 2},
		{"asPercent(a.b, sumSeries(c.*))", StepInherit, 0, 0, true, "", "", 2},
		{"aliasByNode(a.b, 1, 2)", StepInherit, 0, 0, true, "", "", 1},
		{"movingAverage(a.b, 1h)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "movingAverage", 1},
		{"sumSeries(summarize(a.b, '1h'), summarize(c.d, '1d'))", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 2},
		{"hitcount(a.b, '1h')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "hitcount", 1},
		{"hitcount(a.b, '1h', true)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "hitcount", 1},
		{"hitcount(a.b, '1h', alignToInterval=true)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "hitcount", 1},
		{"hitcount(a.b, 'x')", StepUnknown, 0, 0, false, ReasonParseError, "hitcount", 1},
		{"smartSummarize(a.b, '1h')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "smartSummarize", 1},
		{"smartSummarize(a.b, 'x')", StepUnknown, 0, 0, false, ReasonParseError, "smartSummarize", 1},
		// time shifting
		{"timeShift(a.b, '1d')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "timeShift", 1},
		{"timeShift(a.b, 'x')", StepUnknown, 0, 0, false, ReasonParseError, "timeShift", 1},
		{"timeShift(a.b, 5)", StepUnknown, 0, 0, false, ReasonParseError, "timeShift", 1},
		// not allowlisted
		{"movingAverage(a.b, '5min')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "movingAverage", 1},
		{"highestMax(a.*, 1)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "highestMax", 1},
		// the outermost function is judged first, so summarize is named
		{"summarize(movingAverage(a.b, '5min'), '1h')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "summarize", 1},
		{"alias(derivative(a.b), 'x')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "derivative", 1},
		{"notAFunction(a.b)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "notAFunction", 1},
		{"template(a.$1, 'x')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "template", 1},
		{"constantLine(5)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "constantLine", 0},
		{"seriesByTag('name=cpu')", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "seriesByTag", 0},
		{"sumSeries()", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "sumSeries", 0},
		{"scale(1, 2)", StepUnknown, 0, 0, false, ReasonFunctionNotAllowlisted, "scale", 0},
		{"hitcount(a.b, '1h', a.b)", StepUnknown, 0, 0, false, ReasonParseError, "hitcount", 1},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			n, err := ParseTarget(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			c := Classify(n)
			if c.Step != tc.step || c.FixedStep != tc.fixed || c.Shift != tc.shift ||
				c.Accelerable() != tc.accel || c.Reason != tc.reason || c.Offender != tc.offender ||
				len(c.Leaves) != tc.leaves {
				t.Errorf("got %+v", c)
			}
		})
	}
}

func TestEveryAllowlistedFunctionClassifies(t *testing.T) {
	// a typo'd name or a seriesArgs/arity mismatch would sit in the allowlist
	// looking correct; conditional entries have their own cases in TestClassify
	conditional := []string{"summarize", "hitcount", "smartSummarize", "timeShift"}
	for name, spec := range allowlist {
		t.Run(name, func(t *testing.T) {
			if spec.conditional != nil {
				if !slices.Contains(conditional, name) {
					t.Fatalf("%s is conditional but is not one of %v: add a case for it", name, conditional)
				}
				return
			}
			// seriesArgs is the count of leading series arguments, and 0
			// means every argument is one
			n := max(spec.seriesArgs, 1)
			args := make([]string, 0, n+1)
			for i := range n {
				args = append(args, fmt.Sprintf("l%d.leaf", i))
			}
			// a trailing scalar, which must never be counted as a leaf
			if spec.seriesArgs > 0 {
				args = append(args, "1")
			}
			expr := name + "(" + strings.Join(args, ", ") + ")"
			node, err := ParseTarget(expr)
			if err != nil {
				t.Fatalf("%s: %v", expr, err)
			}
			c := Classify(node)
			if !c.Accelerable() {
				t.Fatalf("%s: not accelerable (%s / %s)", expr, c.Reason, c.Offender)
			}
			if c.Step != spec.step {
				t.Errorf("%s: step %v, want %v", expr, c.Step, spec.step)
			}
			if len(c.Leaves) != n {
				t.Errorf("%s: %d leaves %v, want %d", expr, len(c.Leaves), c.Leaves, n)
			}
		})
	}
}

func TestClassifyTimeShiftValues(t *testing.T) {
	// timeShift is not decomposable in v1, but its shift must still be read
	// correctly for the day it is: unsigned shifts default to negative
	for in, want := range map[string]time.Duration{
		"timeShift(a.b, '1d')": -24 * time.Hour, "timeShift(a.b, '-1d')": -24 * time.Hour,
		"timeShift(a.b, '+1d')": 24 * time.Hour, "timeShift(a.b, 1d)": -24 * time.Hour,
	} {
		n, _ := ParseTarget(in)
		c := n.(*Call)
		step, d, decomposable := classifyTimeShift(c)
		if step != StepShift || d != want || decomposable {
			t.Errorf("%s: got %v %v %t", in, step, d, decomposable)
		}
	}
}
