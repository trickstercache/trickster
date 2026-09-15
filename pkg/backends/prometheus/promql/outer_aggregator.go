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

	"github.com/prometheus/prometheus/promql/parser"
)

// AggregationGrouping holds an aggregation's sorted, de-duplicated by or
// without labels.
type AggregationGrouping struct {
	Labels  []string
	Without bool
}

type outerAggregation struct {
	Operator       string
	Parameter      parser.Expr
	Inner          Expr
	Aggregation    Expr
	Grouping       AggregationGrouping
	SortSet        bool
	SortDescending bool
	inputPrefix    string
	inputSuffix    string
}

func parseOuterAggregation(e Expr, operators ...string) (outerAggregation, bool) {
	sortSpec, sorted := ParseSortWrapper(e)
	if sorted {
		e = sortSpec.Inner
	}
	agg, ok := e.node.(*parser.AggregateExpr)
	if !ok {
		return outerAggregation{}, false
	}
	operator := agg.Op.String()
	if !slices.Contains(operators, operator) {
		return outerAggregation{}, false
	}
	aggRange, inputRange := agg.PositionRange(), agg.Expr.PositionRange()
	return outerAggregation{
		Operator:       operator,
		Parameter:      agg.Param,
		Inner:          e.child(agg.Expr),
		Aggregation:    e,
		Grouping:       aggregationGrouping(agg),
		SortSet:        sorted,
		SortDescending: sortSpec.Descending,
		inputPrefix:    sourceRange(e.source, int(aggRange.Start), int(inputRange.Start)),
		inputSuffix:    sourceRange(e.source, int(inputRange.End), int(aggRange.End)),
	}, true
}

func aggregationGrouping(agg *parser.AggregateExpr) AggregationGrouping {
	if len(agg.Grouping) == 0 {
		return AggregationGrouping{Without: agg.Without}
	}
	labels := slices.Clone(agg.Grouping)
	slices.Sort(labels)
	return AggregationGrouping{Labels: slices.Compact(labels), Without: agg.Without}
}

// CompleteOuterAggregation returns the operator and vector input of an
// aggregation only when that aggregation is the complete expression.
func CompleteOuterAggregation(e Expr) (string, Expr, bool) {
	agg, ok := e.node.(*parser.AggregateExpr)
	if !ok {
		return "", Expr{}, false
	}
	return agg.Op.String(), e.child(agg.Expr), true
}

// CompleteOuterAggregationGrouping returns the grouping of a complete outer
// aggregation.
func CompleteOuterAggregationGrouping(e Expr) (AggregationGrouping, bool) {
	agg, ok := e.node.(*parser.AggregateExpr)
	if !ok {
		return AggregationGrouping{}, false
	}
	return aggregationGrouping(agg), true
}

// ReplaceOuterAggregator substitutes the complete outer aggregation's operator
// with replacement, preserving the remaining source text verbatim.
func ReplaceOuterAggregator(e Expr, aggregator, replacement string) string {
	text := e.String()
	agg, ok := e.node.(*parser.AggregateExpr)
	if !ok || agg.Op.String() != aggregator || len(text) < len(aggregator) {
		return text
	}
	return replacement + text[len(aggregator):]
}

// ZeroFallback describes `aggregation or vector(0)`, where the zero supplies a
// label-free result only when the aggregation has no matching series.
type ZeroFallback struct {
	Operator        string
	Input           Expr
	Grouping        AggregationGrouping
	DefaultMatching bool
}

// ParseZeroFallback matches a complete `aggregation or vector(0)` expression.
func ParseZeroFallback(e Expr) (ZeroFallback, bool) {
	binary, ok := e.node.(*parser.BinaryExpr)
	if !ok || binary.Op != parser.LOR || !isZeroVector(e.child(binary.RHS)) {
		return ZeroFallback{}, false
	}
	lhs := e.child(binary.LHS)
	agg, ok := lhs.node.(*parser.AggregateExpr)
	if !ok {
		return ZeroFallback{}, false
	}
	defaultMatching := true
	if matching := binary.VectorMatching; matching != nil {
		defaultMatching = !matching.On && len(matching.MatchingLabels) == 0
	}
	return ZeroFallback{
		Operator:        agg.Op.String(),
		Input:           lhs.child(agg.Expr),
		Grouping:        aggregationGrouping(agg),
		DefaultMatching: defaultMatching,
	}, true
}

func isZeroVector(e Expr) bool {
	call, ok := e.node.(*parser.Call)
	if !ok || call.Func == nil || call.Func.Name != functionVector || len(call.Args) != 1 {
		return false
	}
	value, ok := scalarLiteral(call.Args[0])
	return ok && value == 0
}
