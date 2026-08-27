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

import "strings"

// NonShardLocalFunction returns a PromQL function whose result may depend on
// seeing a globally complete vector rather than one fanout member's shard.
func NonShardLocalFunction(query string) (string, bool) {
	q := strings.ToLower(query)
	var quote byte
	var escaped bool
	for i := 0; i < len(q); {
		c := q[i]
		if quote != 0 {
			i++
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
		if c == '"' || c == '\'' || c == '`' {
			quote = c
			i++
			continue
		}
		if !isPromQLIdentifierStart(c) {
			i++
			continue
		}
		start := i
		for i < len(q) && isPromQLIdentifierPart(q[i]) {
			i++
		}
		name := q[start:i]
		switch name {
		case "absent", "absent_over_time", "histogram_fraction", "histogram_quantile",
			"info", "scalar", "sort", "sort_desc", "sort_by_label", "sort_by_label_desc":
		default:
			continue
		}
		for i < len(q) && isPromQLSpace(q[i]) {
			i++
		}
		if i < len(q) && q[i] == '(' {
			return name, true
		}
	}
	return "", false
}

// ContainsBinaryExpression reports whether query contains a PromQL binary
// expression outside label matchers, range selectors, and quoted strings.
// TSM uses this as a conservative boundary for potentially cross-shard joins.
func ContainsBinaryExpression(query string) bool {
	q := strings.ToLower(query)
	var quote byte
	var escaped bool
	var braces, brackets int
	for i := 0; i < len(q); {
		c := q[i]
		if quote != 0 {
			i++
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
		if c == '"' || c == '\'' || c == '`' {
			quote = c
			i++
			continue
		}
		switch c {
		case '{':
			braces++
			i++
			continue
		case '}':
			if braces > 0 {
				braces--
			}
			i++
			continue
		case '[':
			brackets++
			i++
			continue
		case ']':
			if brackets > 0 {
				brackets--
			}
			i++
			continue
		}
		if braces > 0 || brackets > 0 {
			i++
			continue
		}
		if isPromQLDigit(c) || c == '.' && i+1 < len(q) && isPromQLDigit(q[i+1]) {
			i = skipPromQLNumber(q, i)
			continue
		}
		if isPromQLIdentifierStart(c) {
			start := i
			for i < len(q) && isPromQLIdentifierPart(q[i]) {
				i++
			}
			switch q[start:i] {
			case "and", "or", "unless", "atan2":
				return true
			}
			continue
		}
		switch c {
		case '*', '/', '%', '^', '>', '<', '=':
			return true
		case '!':
			if i+1 < len(q) && q[i+1] == '=' {
				return true
			}
		case '+', '-':
			if isBinaryAddSub(q, i) {
				return true
			}
		}
		i++
	}
	return false
}

func isBinaryAddSub(query string, index int) bool {
	j := index - 1
	for j >= 0 && isPromQLSpace(query[j]) {
		j--
	}
	if j < 0 {
		return false
	}
	switch query[j] {
	case '(', ',', '+', '-', '*', '/', '%', '^', '<', '>', '=', '!', ':', '@':
		return false
	}
	end := j + 1
	for j >= 0 && isPromQLIdentifierPart(query[j]) {
		j--
	}
	return query[j+1:end] != "offset"
}

func skipPromQLNumber(query string, index int) int {
	i := index
	if i+1 < len(query) && query[i] == '0' && query[i+1] == 'x' {
		i += 2
		for i < len(query) && (isPromQLHexDigit(query[i]) || query[i] == '.') {
			i++
		}
		if i < len(query) && query[i] == 'p' {
			i = skipPromQLExponent(query, i+1)
		}
		return i
	}
	for i < len(query) && (isPromQLDigit(query[i]) || query[i] == '.') {
		i++
	}
	if i < len(query) && query[i] == 'e' {
		i = skipPromQLExponent(query, i+1)
	}
	return i
}

func skipPromQLExponent(query string, index int) int {
	i := index
	if i < len(query) && (query[i] == '+' || query[i] == '-') {
		i++
	}
	for i < len(query) && isPromQLDigit(query[i]) {
		i++
	}
	return i
}

func isPromQLDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isPromQLHexDigit(c byte) bool {
	return isPromQLDigit(c) || c >= 'a' && c <= 'f'
}

func isPromQLIdentifierStart(c byte) bool {
	return c == '_' || c == ':' || c >= 'a' && c <= 'z'
}

func isPromQLIdentifierPart(c byte) bool {
	return isPromQLIdentifierStart(c) || c >= '0' && c <= '9'
}

func isPromQLSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}
