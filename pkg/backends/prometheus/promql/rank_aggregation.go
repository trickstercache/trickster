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
	"math/big"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

type AggregationGrouping struct {
	Labels  []string
	Without bool
}

type RankAggregation struct {
	Operator       string
	K              int
	InnerQuery     string
	Grouping       AggregationGrouping
	SortSet        bool
	SortDescending bool
}

var maxRankK = int(^uint(0) >> 1)

func ParseRankAggregation(query string) (RankAggregation, bool) {
	q := strings.TrimSpace(query)
	if sortSpec, ok := ParseSortWrapper(q); ok {
		spec, found := parseRankAggregation(sortSpec.InnerQuery)
		if found {
			spec.SortSet = true
			spec.SortDescending = sortSpec.Descending
		}
		return spec, found
	}
	return parseRankAggregation(q)
}

func parseRankAggregation(query string) (RankAggregation, bool) {
	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	var op string
	for _, candidate := range []string{aggregation.BottomK, aggregation.TopK} {
		if !strings.HasPrefix(ql, candidate) {
			continue
		}
		if len(q) == len(candidate) || isPromQLBoundary(q[len(candidate)]) {
			op = candidate
			q = strings.TrimSpace(q[len(candidate):])
			break
		}
	}
	if op == "" {
		return RankAggregation{}, false
	}

	var grouping AggregationGrouping
	var hasGrouping bool
	if g, rest, ok := parseGrouping(q); ok {
		grouping = g
		hasGrouping = true
		q = rest
	}

	if q == "" || q[0] != '(' {
		return RankAggregation{}, false
	}
	closeIdx := findMatchingCloser(q, 0, '(', ')')
	if closeIdx < 0 {
		return RankAggregation{}, false
	}
	args := q[1:closeIdx]
	trailer := strings.TrimSpace(q[closeIdx+1:])
	if !hasGrouping && trailer != "" {
		if g, rest, ok := parseGrouping(trailer); ok {
			grouping = g
			hasGrouping = true
			trailer = rest
		}
	}
	if trailer != "" {
		return RankAggregation{}, false
	}

	comma := findTopLevelComma(args)
	if comma < 0 {
		return RankAggregation{}, false
	}
	k, ok := parseRankK(args[:comma])
	if !ok {
		return RankAggregation{}, false
	}
	innerQuery := strings.TrimSpace(args[comma+1:])
	if innerQuery == "" {
		return RankAggregation{}, false
	}
	if !hasGrouping {
		grouping = AggregationGrouping{}
	}
	return RankAggregation{Operator: op, K: k, InnerQuery: innerQuery, Grouping: grouping}, true
}

func parseRankK(input string) (int, bool) {
	v, _, err := big.ParseFloat(strings.TrimSpace(input), 10, 256, big.ToNearestEven)
	if err != nil || v.IsInf() || v.Sign() < 0 {
		return 0, false
	}
	bi, acc := v.Int(nil)
	if acc != big.Exact || bi.Cmp(big.NewInt(int64(maxRankK))) > 0 {
		return 0, false
	}
	return int(bi.Int64()), true
}

func parseGrouping(input string) (AggregationGrouping, string, bool) {
	q := strings.TrimSpace(input)
	ql := strings.ToLower(q)
	for _, kw := range []string{"without", "by"} {
		if !strings.HasPrefix(ql, kw) {
			continue
		}
		if len(q) > len(kw) && !isPromQLBoundary(q[len(kw)]) {
			continue
		}
		rest := strings.TrimSpace(q[len(kw):])
		if rest == "" || rest[0] != '(' {
			return AggregationGrouping{}, input, false
		}
		closeIdx := findMatchingCloser(rest, 0, '(', ')')
		if closeIdx < 0 {
			return AggregationGrouping{}, input, false
		}
		labels := parseLabels(rest[1:closeIdx])
		return AggregationGrouping{
			Labels:  labels,
			Without: kw == "without",
		}, strings.TrimSpace(rest[closeIdx+1:]), true
	}
	return AggregationGrouping{}, input, false
}

func parseLabels(input string) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	seen := make(map[string]struct{}, len(parts))
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		label := strings.TrimSpace(part)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		labels = append(labels, label)
	}
	slices.Sort(labels)
	return labels
}

func unwrapUnaryFunction(query, name string) (string, bool) {
	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	if !strings.HasPrefix(ql, name) {
		return "", false
	}
	openIdx := len(name)
	for openIdx < len(q) {
		c := q[openIdx]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		openIdx++
	}
	if openIdx >= len(q) || q[openIdx] != '(' {
		return "", false
	}
	closeIdx := findMatchingCloser(q, openIdx, '(', ')')
	if closeIdx != len(q)-1 {
		return "", false
	}
	return q[openIdx+1 : closeIdx], true
}

func findTopLevelComma(input string) int {
	var parens, brackets, braces int
	var quote byte
	var escaped bool
	for i := range input {
		c := input[i]
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
		case '(':
			parens++
		case ')':
			parens--
		case '[':
			brackets++
		case ']':
			brackets--
		case '{':
			braces++
		case '}':
			braces--
		case ',':
			if parens == 0 && brackets == 0 && braces == 0 {
				return i
			}
		}
	}
	return -1
}

func findMatchingCloser(input string, openIdx int, open, close byte) int {
	if openIdx < 0 || openIdx >= len(input) || input[openIdx] != open {
		return -1
	}
	depth := 0
	var quote byte
	var escaped bool
	for i := openIdx; i < len(input); i++ {
		c := input[i]
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
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isPromQLBoundary(c byte) bool {
	switch c {
	case '(', ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}
