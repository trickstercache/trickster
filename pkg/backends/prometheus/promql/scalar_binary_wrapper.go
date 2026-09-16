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
	"slices"

	"github.com/prometheus/prometheus/promql/parser"
)

type scalarBinaryOperation struct {
	operator       parser.ItemType
	scalar         float64
	scalarLeft     bool
	returnBool     bool
	evaluationTime bool
}

// HistogramScalarOperations applies provider-native histogram arithmetic.
type HistogramScalarOperations interface {
	ApplyScalarBinary(value any, operator string, scalar float64, scalarLeft bool) (any, bool)
}

// ScalarBinaryWrapper describes supported scalar operations around one vector
// expression, ordered from the innermost operation to the outermost.
type ScalarBinaryWrapper struct {
	Inner      Expr
	operations []scalarBinaryOperation
}

// ParseScalarBinaryWrapper parses scalar and unary wrappers around an expression
// containing an aggregation.
func ParseScalarBinaryWrapper(e Expr) (ScalarBinaryWrapper, bool) {
	current := e
	var operations []scalarBinaryOperation

parseOperations:
	for {
		switch node := current.node.(type) {
		case *parser.UnaryExpr:
			if node.Op != parser.SUB || node.Expr.Type() != parser.ValueTypeVector {
				break parseOperations
			}
			operations = append(operations, scalarBinaryOperation{
				operator: parser.MUL,
				scalar:   -1,
			})
			current = current.child(node.Expr)
		case *parser.BinaryExpr:
			if !supportedScalarBinaryOperator(node.Op) {
				break parseOperations
			}
			operation, next, ok := parseScalarBinaryOperation(current, node)
			if !ok {
				break parseOperations
			}
			operations = append(operations, operation)
			current = next
		default:
			break parseOperations
		}
	}
	if len(operations) == 0 || !current.ContainsAggregation() {
		return ScalarBinaryWrapper{}, false
	}
	slices.Reverse(operations)
	return ScalarBinaryWrapper{Inner: current, operations: operations}, true
}

func parseScalarBinaryOperation(current Expr, binary *parser.BinaryExpr) (
	scalarBinaryOperation, Expr, bool,
) {
	operation := scalarBinaryOperation{
		operator:   binary.Op,
		returnBool: binary.ReturnBool,
	}
	var next Expr
	var ok bool
	switch leftType, rightType := binary.LHS.Type(), binary.RHS.Type(); {
	case leftType == parser.ValueTypeVector && rightType == parser.ValueTypeScalar:
		operation.scalar, operation.evaluationTime, ok = scalarBinaryOperand(binary.RHS)
		next = current.child(binary.LHS)
	case leftType == parser.ValueTypeScalar && rightType == parser.ValueTypeVector:
		operation.scalar, operation.evaluationTime, ok = scalarBinaryOperand(binary.LHS)
		operation.scalarLeft = true
		next = current.child(binary.RHS)
	}
	return operation, next, ok
}

func scalarBinaryOperand(node parser.Expr) (float64, bool, bool) {
	if value, ok := scalarLiteral(node); ok {
		return value, false, true
	}
	factor, ok := evaluationTimeFactor(node)
	return factor, ok, ok
}

func evaluationTimeFactor(node parser.Expr) (float64, bool) {
	switch n := newExpr(node, "").node.(type) {
	case *parser.Call:
		if n.Func != nil && n.Func.Name == functionTime && len(n.Args) == 0 {
			return 1, true
		}
	case *parser.UnaryExpr:
		factor, ok := evaluationTimeFactor(n.Expr)
		if !ok {
			return 0, false
		}
		switch n.Op {
		case parser.ADD:
			return factor, true
		case parser.SUB:
			return -factor, true
		}
	}
	return 0, false
}

func supportedScalarBinaryOperator(operator parser.ItemType) bool {
	switch operator {
	case parser.ADD, parser.SUB, parser.MUL, parser.DIV, parser.POW, parser.MOD,
		parser.EQLC, parser.NEQ, parser.GTR, parser.LSS, parser.GTE, parser.LTE,
		parser.ATAN2:
		return true
	default:
		return false
	}
}

// ApplyFloat applies the wrapper to one float sample. A false result means a
// filtering comparison removed the sample.
func (w ScalarBinaryWrapper) ApplyFloat(value, evaluationTime float64) (float64, bool) {
	for _, operation := range w.operations {
		scalar := operation.scalar
		if operation.evaluationTime {
			scalar *= evaluationTime
		}
		original := value
		left, right := value, scalar
		if operation.scalarLeft {
			left, right = right, left
		}
		keep := true
		switch operation.operator {
		case parser.ADD:
			value = left + right
		case parser.SUB:
			value = left - right
		case parser.MUL:
			value = left * right
		case parser.DIV:
			value = left / right
		case parser.POW:
			value = math.Pow(left, right)
		case parser.MOD:
			value = math.Mod(left, right)
		case parser.ATAN2:
			value = math.Atan2(left, right)
		case parser.EQLC:
			keep = left == right
		case parser.NEQ:
			keep = left != right
		case parser.GTR:
			keep = left > right
		case parser.LSS:
			keep = left < right
		case parser.GTE:
			keep = left >= right
		case parser.LTE:
			keep = left <= right
		}
		if operation.operator.IsComparisonOperator() {
			if operation.returnBool {
				value = 0
				if keep {
					value = 1
				}
				continue
			}
			if !keep {
				return 0, false
			}
			value = original
		}
	}
	return value, true
}

// ApplyHistogram applies the histogram-compatible subset of the wrapper.
func (w ScalarBinaryWrapper) ApplyHistogram(value any,
	operations HistogramScalarOperations, evaluationTime float64,
) (any, bool) {
	if operations == nil {
		return nil, false
	}
	for _, operation := range w.operations {
		scalar := operation.scalar
		if operation.evaluationTime {
			scalar *= evaluationTime
		}
		var ok bool
		value, ok = operations.ApplyScalarBinary(value, operation.operator.String(),
			scalar, operation.scalarLeft)
		if !ok {
			return nil, false
		}
	}
	return value, true
}

// DropsMetricName reports whether the wrapper changes the metric schema.
func (w ScalarBinaryWrapper) DropsMetricName() bool {
	for _, operation := range w.operations {
		if !operation.operator.IsComparisonOperator() || operation.returnBool {
			return true
		}
	}
	return false
}

// UsesEvaluationTime reports whether an operation contains time().
func (w ScalarBinaryWrapper) UsesEvaluationTime() bool {
	for _, operation := range w.operations {
		if operation.evaluationTime {
			return true
		}
	}
	return false
}
