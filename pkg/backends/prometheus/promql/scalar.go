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
	"strconv"
	"strings"
)

// IsScalarExpression reports whether query has a scalar result for the scalar
// expression forms TSM can safely classify without a full PromQL parser:
// scalar(), time(), pi(), numeric literals, grouping, unary signs, and binary
// expressions composed entirely from those scalar forms.
func IsScalarExpression(query string) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return false
	}
	if inner, ok := unwrapGrouping(q); ok {
		return IsScalarExpression(inner)
	}
	if inner, ok := unwrapUnaryFunction(q, "scalar"); ok {
		return strings.TrimSpace(inner) != ""
	}
	for _, name := range []string{"time", "pi"} {
		if inner, ok := unwrapUnaryFunction(q, name); ok {
			return strings.TrimSpace(inner) == ""
		}
	}
	if _, err := strconv.ParseFloat(q, 64); err == nil {
		return true
	}
	if (q[0] == '+' || q[0] == '-') && IsScalarExpression(q[1:]) {
		return true
	}
	left, right, operator, ok := splitScalarBinaryExpression(q)
	if !ok || operator == "and" || operator == "or" || operator == "unless" {
		return false
	}
	if isComparisonOperator(operator) {
		right = trimBoolModifier(right)
	}
	return IsScalarExpression(left) && IsScalarExpression(right)
}

func isComparisonOperator(operator string) bool {
	switch operator {
	case "==", "!=", ">", "<", ">=", "<=":
		return true
	default:
		return false
	}
}

func trimBoolModifier(expression string) string {
	q := strings.TrimSpace(expression)
	const modifier = "bool"
	if len(q) <= len(modifier) || !strings.EqualFold(q[:len(modifier)], modifier) {
		return q
	}
	switch q[len(modifier)] {
	case ' ', '\t', '\n', '\r':
		return strings.TrimSpace(q[len(modifier):])
	default:
		return q
	}
}

func unwrapGrouping(query string) (string, bool) {
	q := strings.TrimSpace(query)
	if len(q) < 2 || q[0] != '(' {
		return "", false
	}
	closeIdx := findMatchingCloser(q, 0, '(', ')')
	if closeIdx != len(q)-1 {
		return "", false
	}
	return q[1:closeIdx], true
}

func splitScalarBinaryExpression(query string) (string, string, string, bool) {
	var parens, brackets, braces int
	var quote byte
	var escaped bool
	for i := range len(query) {
		c := query[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' && quote != '`' {
				escaped = true
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
			continue
		case '(':
			parens++
			continue
		case ')':
			parens--
			continue
		case '[':
			brackets++
			continue
		case ']':
			brackets--
			continue
		case '{':
			braces++
			continue
		case '}':
			braces--
			continue
		}
		if parens != 0 || brackets != 0 || braces != 0 {
			continue
		}
		for _, op := range []string{"atan2", "unless", "and", "or", "==", "!=", ">=", "<=", "+", "-", "*", "/", "%", "^", ">", "<"} {
			if !strings.HasPrefix(query[i:], op) || !binaryOperatorBoundary(query, i, op) {
				continue
			}
			left := strings.TrimSpace(query[:i])
			right := strings.TrimSpace(query[i+len(op):])
			if left == "" || right == "" {
				continue
			}
			return left, right, op, true
		}
	}
	return "", "", "", false
}

func binaryOperatorBoundary(query string, index int, operator string) bool {
	if operator[0] >= 'a' && operator[0] <= 'z' {
		before := index == 0 || isPromQLBoundary(query[index-1])
		afterIndex := index + len(operator)
		after := afterIndex == len(query) || isPromQLBoundary(query[afterIndex])
		return before && after
	}
	if (operator == "+" || operator == "-") && index > 0 {
		prev := query[index-1]
		if prev == 'e' || prev == 'E' {
			return false
		}
	}
	return true
}
