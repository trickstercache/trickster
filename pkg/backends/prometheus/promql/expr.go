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

// Package promql analyzes PromQL expressions for time series merge planning
// using the upstream Prometheus parser.
package promql

import (
	"errors"

	"github.com/prometheus/prometheus/promql/parser"
)

const (
	functionSort     = "sort"
	functionSortDesc = "sort_desc"
	functionVector   = "vector"
)

var promQLParser = parser.NewParser(parser.Options{
	// Accept experimental syntax so planning does not reject what a backend can evaluate.
	EnableExperimentalFunctions:  true,
	ExperimentalDurationExpr:     true,
	EnableExtendedRangeSelectors: true,
	EnableBinopFillModifiers:     true,
})

var errStopInspect = errors.New("stop inspection")

var nonShardLocalFunctions = map[string]struct{}{
	"absent":             {},
	"absent_over_time":   {},
	"histogram_fraction": {},
	"histogram_quantile": {},
	"info":               {},
	"scalar":             {},
	functionSort:         {},
	functionSortDesc:     {},
	"sort_by_label":      {},
	"sort_by_label_desc": {},
}

// Expr is a parsed PromQL expression with redundant grouping parentheses
// removed. It retains the query text so rewrites preserve the original syntax.
type Expr struct {
	node   parser.Expr
	source string
}

// Parse parses a complete PromQL expression.
func Parse(query string) (Expr, error) {
	node, err := promQLParser.ParseExpr(query)
	if err != nil {
		return Expr{}, err
	}
	return newExpr(node, query), nil
}

func newExpr(node parser.Expr, source string) Expr {
	for {
		paren, ok := node.(*parser.ParenExpr)
		if !ok {
			return Expr{node: node, source: source}
		}
		node = paren.Expr
	}
}

func (e Expr) child(node parser.Expr) Expr {
	return newExpr(node, e.source)
}

// String returns the expression's original source text, excluding redundant
// outer parentheses and any surrounding whitespace or comments.
func (e Expr) String() string {
	if e.node == nil {
		return ""
	}
	pr := e.node.PositionRange()
	return sourceRange(e.source, int(pr.Start), int(pr.End))
}

func sourceRange(source string, start, end int) string {
	if start < 0 || end > len(source) || start > end {
		return ""
	}
	return source[start:end]
}

// IsScalar reports whether the expression evaluates to a scalar.
func (e Expr) IsScalar() bool {
	return e.node != nil && e.node.Type() == parser.ValueTypeScalar
}

// ContainsAggregation reports whether the expression contains an aggregation
// at any depth.
func (e Expr) ContainsAggregation() bool {
	return e.find(func(node parser.Node) bool {
		_, ok := node.(*parser.AggregateExpr)
		return ok
	}) != nil
}

// ContainsBinaryExpression reports whether the expression contains a binary
// operation at any depth. TSM treats these as potentially cross-shard joins.
func (e Expr) ContainsBinaryExpression() bool {
	return e.find(func(node parser.Node) bool {
		_, ok := node.(*parser.BinaryExpr)
		return ok
	}) != nil
}

// ContainsCrossShardBinaryExpression reports whether the expression contains
// a vector-to-vector operation that may require series from another shard.
func (e Expr) ContainsCrossShardBinaryExpression() bool {
	return e.find(func(node parser.Node) bool {
		binary, ok := node.(*parser.BinaryExpr)
		return ok && binary.LHS.Type() == parser.ValueTypeVector &&
			binary.RHS.Type() == parser.ValueTypeVector
	}) != nil
}

// NonShardLocalFunction returns the first called function whose result may
// depend on seeing a globally complete vector rather than one fanout shard.
func (e Expr) NonShardLocalFunction() (string, bool) {
	node := e.find(func(node parser.Node) bool {
		call, ok := node.(*parser.Call)
		if !ok || call.Func == nil {
			return false
		}
		_, found := nonShardLocalFunctions[call.Func.Name]
		return found
	})
	if node == nil {
		return "", false
	}
	return node.(*parser.Call).Func.Name, true
}

func (e Expr) find(match func(parser.Node) bool) parser.Node {
	if e.node == nil {
		return nil
	}
	var found parser.Node
	parser.Inspect(e.node, func(node parser.Node, _ []parser.Node) error {
		if node != nil && match(node) {
			found = node
			return errStopInspect
		}
		return nil
	})
	return found
}

func scalarLiteral(node parser.Expr) (float64, bool) {
	// Parentheses and unary signs are accepted; per-step scalar expressions are not.
	switch n := newExpr(node, "").node.(type) {
	case *parser.NumberLiteral:
		return n.Val, true
	case *parser.UnaryExpr:
		value, ok := scalarLiteral(n.Expr)
		if !ok {
			return 0, false
		}
		if n.Op == parser.SUB {
			return -value, true
		}
		return value, true
	default:
		return 0, false
	}
}
