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

package aftership

import (
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	chast "github.com/AfterShip/clickhouse-sql-parser/parser"
)

func TestAnalyzeAdditionalBucketForms(t *testing.T) {
	tests := []struct {
		name string
		expr string
		unit timeseries.FieldDataType
	}{
		{"unix seconds", "intDiv(toUInt32(ts), 60) * 60", timeseries.DateTimeUnixSecs},
		{"unix microseconds", "(intDiv(toUInt32(ts), 60) * 60) * 1000000", timeseries.DateTimeUnixMicro},
		{"unix nanoseconds", "(intDiv(toUInt32(ts), 60) * 60) * 1000000000", timeseries.DateTimeUnixNano},
		{"unsigned bucket cast", "toUInt32(toStartOfMinute(ts))", timeseries.DateTimeUnixSecs},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := "SELECT " + test.expr + " AS t, count() FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY t"
			analysis := NewAnalyzer().Analyze(query, time.Unix(500, 0))
			if analysis.Err != nil {
				t.Fatal(analysis.Err)
			}
			if analysis.Mode != sqlanalyzer.CacheModeDelta || analysis.Plan.OutputUnit != test.unit {
				t.Errorf("analysis = %+v, want delta output unit %v", analysis, test.unit)
			}
		})
	}
}

func TestAnalyzeReversedPredicates(t *testing.T) {
	query := "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
		"WHERE 120 <= ts AND 240 > ts GROUP BY t"
	analysis := NewAnalyzer().Analyze(query, time.Unix(500, 0))
	if analysis.Err != nil {
		t.Fatal(analysis.Err)
	}
	if analysis.Plan.TimeColumn != "ts" || analysis.Plan.LowerBound.Value.Unix() != 120 ||
		analysis.Plan.UpperBound.Value.Unix() != 240 || analysis.Plan.UpperBound.Inclusive {
		t.Errorf("unexpected reversed-bound analysis: %+v", analysis.Plan)
	}
	rendered, err := analysis.Plan.RenderExtent(timeseries.Extent{
		Start: time.Unix(300, 0), End: time.Unix(360, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "300 <= ts") || !strings.Contains(rendered, "420 > ts") {
		t.Errorf("reversed comparisons were not rendered correctly: %s", rendered)
	}
}

func TestAnalyzeUnsupportedBucketBranches(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"fixed bucket missing argument", "toStartOfMinute()"},
		{"fixed bucket non-column", "toStartOfMinute(1)"},
		{"interval missing argument", "toStartOfInterval(ts)"},
		{"interval non-column", "toStartOfInterval(1, INTERVAL 1 minute)"},
		{"interval non-interval", "toStartOfInterval(ts, 60)"},
		{"interval zero", "toStartOfInterval(ts, INTERVAL 0 minute)"},
		{"interval unsupported unit", "toStartOfInterval(ts, INTERVAL 1 year)"},
		{"cast missing argument", "toInt32()"},
		{"intDiv missing argument", "intDiv(ts) * 60"},
		{"intDiv non-column", "intDiv(1, 60) * 60"},
		{"intDiv zero step", "intDiv(ts, 0) * 0"},
		{"intDiv missing step factor", "intDiv(ts, 60)"},
		{"intDiv unknown factor", "intDiv(ts, 60) * 60 * unknown"},
		{"intDiv unsupported multiplier", "intDiv(ts, 60) * 60 * 10"},
		{"multiple intDiv", "intDiv(ts, 60) * intDiv(ts, 60) * 60"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := "SELECT " + test.expr + " AS t, count() FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY t"
			analysis := NewAnalyzer().Analyze(query, time.Unix(500, 0))
			if analysis.Mode != sqlanalyzer.CacheModeObject ||
				analysis.Reason != sqlanalyzer.ReasonUnsupportedBucket || analysis.Err == nil {
				t.Errorf("analysis = %+v, want unsupported-bucket object caching", analysis)
			}
		})
	}
}

func TestAnalyzeRangeAndGroupingBranches(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		reason sqlanalyzer.AnalysisReason
	}{
		{"no range clause",
			"SELECT toStartOfMinute(ts) AS t, count() FROM events GROUP BY t",
			sqlanalyzer.ReasonNotTimeRange},
		{"missing lower bound",
			"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts < 240 GROUP BY t",
			sqlanalyzer.ReasonNotTimeRange},
		{"ambiguous lower bound",
			"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 60 AND ts >= 120 AND ts < 240 GROUP BY t",
			sqlanalyzer.ReasonAmbiguousTimeAxis},
		{"ambiguous upper bound",
			"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 60 AND ts < 180 AND ts < 240 GROUP BY t",
			sqlanalyzer.ReasonAmbiguousTimeAxis},
		{"non-column grouping",
			"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 60 GROUP BY tuple(t)",
			sqlanalyzer.ReasonUnsupportedGrouping},
		{"group by rollup",
			"SELECT toStartOfMinute(ts) AS t, count() FROM events WHERE ts >= 60 GROUP BY t WITH ROLLUP",
			sqlanalyzer.ReasonUnsupportedGrouping},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := NewAnalyzer().Analyze(test.query, time.Unix(500, 0))
			if analysis.Mode != sqlanalyzer.CacheModeObject || analysis.Reason != test.reason || analysis.Err == nil {
				t.Errorf("analysis = %+v, want object mode with reason %v", analysis, test.reason)
			}
		})
	}
}

func TestIntegerAndTimestampHelpers(t *testing.T) {
	t.Run("output units", func(t *testing.T) {
		tests := []struct {
			multiplier int64
			want       timeseries.FieldDataType
			ok         bool
		}{
			{1, timeseries.DateTimeUnixSecs, true},
			{1_000, timeseries.DateTimeUnixMilli, true},
			{1_000_000, timeseries.DateTimeUnixMicro, true},
			{1_000_000_000, timeseries.DateTimeUnixNano, true},
			{10, timeseries.Unknown, false},
		}
		for _, test := range tests {
			got, ok := outputUnitForMultiplier(test.multiplier)
			if got != test.want || ok != test.ok {
				t.Errorf("outputUnitForMultiplier(%d) = (%v, %t), want (%v, %t)",
					test.multiplier, got, ok, test.want, test.ok)
			}
		}
	})

	t.Run("integer expressions", func(t *testing.T) {
		number := func(value string) *chast.NumberLiteral {
			return &chast.NumberLiteral{Literal: value, Base: 10}
		}
		constants := map[string]int64{"x": 7}
		tests := []struct {
			name string
			expr chast.Expr
			want int64
			ok   bool
		}{
			{"literal", number("5"), 5, true},
			{"constant", &chast.Ident{Name: "x"}, 7, true},
			{"missing constant", &chast.Ident{Name: "y"}, 0, false},
			{"addition", &chast.BinaryOperation{LeftExpr: number("5"), Operation: chast.TokenKindPlus, RightExpr: number("2")}, 7, true},
			{"subtraction", &chast.BinaryOperation{LeftExpr: number("5"), Operation: chast.TokenKindMinus, RightExpr: number("2")}, 3, true},
			{"invalid operand", &chast.BinaryOperation{LeftExpr: number("bad"), Operation: chast.TokenKindPlus, RightExpr: number("2")}, 0, false},
			{"unsupported operation", &chast.BinaryOperation{LeftExpr: number("5"), Operation: chast.TokenKindMul, RightExpr: number("2")}, 0, false},
		}
		for _, test := range tests {
			got, ok := evalInteger(test.expr, constants)
			if got != test.want || ok != test.ok {
				t.Errorf("%s: evalInteger() = (%d, %t), want (%d, %t)", test.name, got, ok, test.want, test.ok)
			}
		}
	})

	t.Run("timestamp units", func(t *testing.T) {
		value := int64(1_234_567_890)
		tests := []struct {
			style boundStyle
			want  time.Time
			unit  timeseries.FieldDataType
		}{
			{boundUnixSeconds, time.Unix(value, 0), timeseries.DateTimeUnixSecs},
			{boundUnixMilli, time.Unix(1_234_567, 890*time.Millisecond.Nanoseconds()), timeseries.DateTimeUnixMilli},
			{boundUnixMicro, time.Unix(1_234, 567_890*time.Microsecond.Nanoseconds()), timeseries.DateTimeUnixMicro},
			{boundUnixNano, time.Unix(0, value), timeseries.DateTimeUnixNano},
			{boundSQLDateTime, time.Unix(value, 0), timeseries.DateTimeSQL},
		}
		for _, test := range tests {
			if got := timeFromInteger(value, test.style); !got.Equal(test.want) {
				t.Errorf("timeFromInteger(%v) = %s, want %s", test.style, got, test.want)
			}
			if got := inputTypeForBound(test.style); got != test.unit {
				t.Errorf("inputTypeForBound(%v) = %v, want %v", test.style, got, test.unit)
			}
		}
	})
}

func TestBoundRenderingAndParsingHelpers(t *testing.T) {
	extent := timeseries.Extent{
		Start: time.Unix(1, 234_567_890),
		End:   time.Unix(2, 987_654_321),
	}
	tests := []struct {
		name   string
		target endpoint
		style  boundStyle
		want   string
	}{
		{"seconds", endpointLower, boundUnixSeconds, "1"},
		{"milliseconds", endpointLower, boundUnixMilli, "1234"},
		{"microseconds", endpointUpper, boundUnixMicro, "2987654"},
		{"nanoseconds", endpointUpper, boundUnixNano, "2987654321"},
		{"SQL datetime", endpointLower, boundSQLDateTime, "'1970-01-01 00:00:01.23456789'"},
		{"toDateTime", endpointUpper, boundToDateTime, "toDateTime(2)"},
		{"toDate", endpointUpper, boundToDate, "toDate(2)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := chast.Format(boundExpression(test.target, test.style, extent))
			if got != test.want {
				t.Errorf("bound expression = %q, want %q", got, test.want)
			}
		})
	}

	for _, test := range []struct {
		value string
		want  int64
		ok    bool
	}{
		{"2020-01-02 03:04:05", 1_577_934_245, true},
		{"2020-01-02", 1_577_923_200, true},
		{"2020-01-02T03:04:05Z", 1_577_934_245, true},
		{"not-a-time", -62_135_596_800, false},
	} {
		got, ok := sqlanalyzer.ParseSQLTime(test.value)
		if got.Unix() != test.want || ok != test.ok {
			t.Errorf("ParseSQLTime(%q) = (%s, %t), want Unix %d, %t", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestInvertOperator(t *testing.T) {
	for input, want := range map[string]string{
		">": "<", ">=": "<=", "<": ">", "<=": ">=", "=": "=",
	} {
		if got := invertOperator(input); got != want {
			t.Errorf("invertOperator(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEvaluateBoundBranches(t *testing.T) {
	number := func(value string) *chast.NumberLiteral {
		return &chast.NumberLiteral{Literal: value, Base: 10}
	}
	now := time.Unix(1_700_000_000, 0)
	constants := map[string]int64{"start": 120}
	tests := []struct {
		name  string
		expr  chast.Expr
		style boundStyle
		want  time.Time
		ok    bool
	}{
		{"number", number("1234"), boundUnixMilli, time.UnixMilli(1234), true},
		{"invalid number", number("bad"), boundUnixSeconds, time.Unix(0, 0), false},
		{"SQL datetime", &chast.StringLiteral{Literal: "2020-01-02 03:04:05"}, boundUnixSeconds,
			time.Unix(1_577_934_245, 0), true},
		{"invalid SQL datetime", &chast.StringLiteral{Literal: "bad"}, boundUnixSeconds, time.Time{}, false},
		{"now", functionExpression("now"), boundUnixSeconds, now, true},
		{"toDateTime", functionExpression("toDateTime", number("120")), boundUnixSeconds,
			time.Unix(120, 0), true},
		{"toDate", functionExpression("toDate", number("120")), boundUnixSeconds,
			time.Unix(120, 0), true},
		{"function without name", &chast.FunctionExpr{}, boundUnixSeconds, time.Time{}, false},
		{"conversion missing argument", functionExpression("toDateTime"), boundUnixSeconds, time.Time{}, false},
		{"conversion invalid argument", functionExpression("toDateTime", &chast.Ident{Name: "missing"}),
			boundUnixSeconds, time.Time{}, false},
		{"constant", &chast.Ident{Name: "start"}, boundUnixSeconds, time.Unix(120, 0), true},
		{"missing constant", &chast.Ident{Name: "missing"}, boundUnixSeconds, time.Unix(0, 0), false},
		{"addition", &chast.BinaryOperation{LeftExpr: number("120"), Operation: chast.TokenKindPlus,
			RightExpr: number("30")}, boundUnixSeconds, time.Unix(150, 0), true},
		{"subtraction", &chast.BinaryOperation{LeftExpr: number("120"), Operation: chast.TokenKindMinus,
			RightExpr: number("30")}, boundUnixSeconds, time.Unix(90, 0), true},
		{"invalid binary operand", &chast.BinaryOperation{LeftExpr: number("bad"), Operation: chast.TokenKindPlus,
			RightExpr: number("30")}, boundUnixSeconds, time.Time{}, false},
		{"unsupported binary operation", &chast.BinaryOperation{LeftExpr: number("120"), Operation: chast.TokenKindMul,
			RightExpr: number("30")}, boundUnixSeconds, time.Time{}, false},
		{"unsupported expression", &chast.PlaceHolder{Type: "x"}, boundUnixSeconds, time.Time{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := evaluateBound(test.expr, true, test.style, constants, now)
			if ok != test.ok {
				t.Fatalf("evaluateBound() ok = %t, want %t; bound=%+v", ok, test.ok, got)
			}
			if test.ok && !got.value.Equal(test.want) {
				t.Errorf("evaluateBound() time = %s, want %s", got.value, test.want)
			}
			if test.ok && !got.inclusive {
				t.Error("evaluateBound() discarded inclusive comparator")
			}
			if test.name == "toDateTime" && got.style != boundToDateTime {
				t.Errorf("toDateTime style = %v, want %v", got.style, boundToDateTime)
			}
			if test.name == "toDate" && got.style != boundToDate {
				t.Errorf("toDate style = %v, want %v", got.style, boundToDate)
			}
		})
	}
}

func TestNumericBoundStyles(t *testing.T) {
	bucket := bucketSpec{timeColumn: "ts", outputColumn: "t"}
	tests := []struct {
		field string
		unit  timeseries.FieldDataType
		want  boundStyle
	}{
		{"ts", timeseries.DateTimeUnixNano, boundUnixSeconds},
		{"other", timeseries.DateTimeUnixNano, boundUnixSeconds},
		{"t", timeseries.DateTimeUnixMilli, boundUnixMilli},
		{"t", timeseries.DateTimeUnixMicro, boundUnixMicro},
		{"t", timeseries.DateTimeUnixNano, boundUnixNano},
		{"t", timeseries.DateTimeUnixSecs, boundUnixSeconds},
	}
	for _, test := range tests {
		bucket.outputUnit = test.unit
		if got := numericBoundStyle(test.field, bucket); got != test.want {
			t.Errorf("numericBoundStyle(%q, %v) = %v, want %v", test.field, test.unit, got, test.want)
		}
	}
}

func TestUnsafeBooleanASTForms(t *testing.T) {
	number := &chast.NumberLiteral{Literal: "1", Base: 10}
	tests := []struct {
		name string
		expr chast.Expr
	}{
		{"not expression", &chast.NotExpr{Expr: number}},
		{"unary not", &chast.UnaryExpr{Kind: chast.TokenKind("NOT"), Expr: number}},
		{"binary or", &chast.BinaryOperation{LeftExpr: number, Operation: chast.TokenKind(chast.KeywordOr), RightExpr: number}},
		{"binary has-not", &chast.BinaryOperation{LeftExpr: number, Operation: chast.TokenKind("="), RightExpr: number, HasNot: true}},
		{"not between", &chast.BetweenClause{Expr: number, Between: number, And: number, Not: true}},
		{"not function", functionExpression("not", number)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !containsUnsafeBoolean(test.expr) {
				t.Errorf("containsUnsafeBoolean(%T) = false, want true", test.expr)
			}
		})
	}
	if containsUnsafeBoolean(&chast.BinaryOperation{
		LeftExpr: number, Operation: chast.TokenKind("="), RightExpr: number,
	}) {
		t.Error("ordinary equality was classified as unsafe boolean logic")
	}
}

func TestSmallASTHelpers(t *testing.T) {
	if got := functionArgs(nil); got != nil {
		t.Errorf("functionArgs(nil) = %v, want nil", got)
	}
	if got := functionArgs(&chast.FunctionExpr{}); got != nil {
		t.Errorf("functionArgs(empty) = %v, want nil", got)
	}
	qualified := identifierExpression("events.ts")
	if got, ok := sourceColumn(qualified); !ok || got != "events.ts" {
		t.Errorf("qualified source column = (%q, %t), want events.ts", got, ok)
	}
	if got := chast.Format(identifierExpression("ts")); got != "ts" {
		t.Errorf("simple identifier = %q, want ts", got)
	}
	if _, ok := sourceColumn(functionExpression("unknown", &chast.Ident{Name: "ts"})); ok {
		t.Error("unsupported wrapper function was accepted as a source column")
	}
	if _, ok := sourceColumn(&chast.NumberLiteral{Literal: "1", Base: 10}); ok {
		t.Error("number literal was accepted as a source column")
	}
}
