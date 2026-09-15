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
	operator   parser.ItemType
	scalar     float64
	scalarLeft bool
	returnBool bool
}

// HistogramScalarOperations applies provider-native histogram arithmetic.
type HistogramScalarOperations interface {
	ApplyScalarBinary(value any, operator string, scalar float64, scalarLeft bool) (any, bool)
}

// ScalarBinaryWrapper describes literal-scalar binary operations around one
// vector expression, ordered from the innermost operation to the outermost.
type ScalarBinaryWrapper struct {
	Inner      Expr
	operations []scalarBinaryOperation
}

// ParseScalarBinaryWrapper parses binary vector-scalar wrappers around an
// expression containing an aggregation.
func ParseScalarBinaryWrapper(e Expr) (ScalarBinaryWrapper, bool) {
	current := e
	var operations []scalarBinaryOperation
	for {
		binary, ok := current.node.(*parser.BinaryExpr)
		if !ok || !supportedScalarBinaryOperator(binary.Op) {
			break
		}
		operation := scalarBinaryOperation{
			operator:   binary.Op,
			returnBool: binary.ReturnBool,
		}
		switch leftType, rightType := binary.LHS.Type(), binary.RHS.Type(); {
		case leftType == parser.ValueTypeVector && rightType == parser.ValueTypeScalar:
			operation.scalar, ok = scalarLiteral(binary.RHS)
			current = current.child(binary.LHS)
		case leftType == parser.ValueTypeScalar && rightType == parser.ValueTypeVector:
			operation.scalar, ok = scalarLiteral(binary.LHS)
			operation.scalarLeft = true
			current = current.child(binary.RHS)
		default:
			ok = false
		}
		if !ok {
			break
		}
		operations = append(operations, operation)
	}
	if len(operations) == 0 || !current.ContainsAggregation() {
		return ScalarBinaryWrapper{}, false
	}
	slices.Reverse(operations)
	return ScalarBinaryWrapper{Inner: current, operations: operations}, true
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
func (w ScalarBinaryWrapper) ApplyFloat(value float64) (float64, bool) {
	for _, operation := range w.operations {
		original := value
		left, right := value, operation.scalar
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
	operations HistogramScalarOperations,
) (any, bool) {
	if operations == nil {
		return nil, false
	}
	for _, operation := range w.operations {
		var ok bool
		value, ok = operations.ApplyScalarBinary(value, operation.operator.String(),
			operation.scalar, operation.scalarLeft)
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
