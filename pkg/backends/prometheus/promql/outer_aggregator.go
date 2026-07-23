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

// Package promql provides utilities for parsing and rewriting PromQL queries.
package promql

import (
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

// AllAggregators lists all known PromQL aggregation operators, sorted with
// longer names first to avoid prefix collisions (e.g. count_values vs count).
var AllAggregators = []aggregation.Operator{
	aggregation.CountValues,
	aggregation.LimitRatio,
	aggregation.BottomK,
	aggregation.LimitK,
	aggregation.StdDev,
	aggregation.StdVar,
	aggregation.Quantile,
	aggregation.TopK,
	aggregation.Count,
	aggregation.Group,
	aggregation.Average,
	aggregation.Sum,
	aggregation.Minimum,
	aggregation.Maximum,
}

// OuterAggregator returns the name of the outermost PromQL aggregation
// operator in query (lowercased), and true if one was found. The remainder
// of the query string is unchanged; this function only inspects the prefix.
//
// Examples:
//
//	OuterAggregator("sum(rate(http_requests_total[5m]))")        → "sum", true
//	OuterAggregator("avg by (region) (requests)")               → "avg", true
//	OuterAggregator("http_requests_total{job=\"api\"}")          → "",    false
//	OuterAggregator("avg_over_time(cpu[5m])")                   → "",    false  (function, not aggregator)
func OuterAggregator(query string) (string, bool) {
	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	for _, agg := range AllAggregators {
		if !strings.HasPrefix(ql, agg) {
			continue
		}
		rest := ql[len(agg):]
		if len(rest) == 0 {
			return agg, true
		}
		// Ensure the character immediately after the keyword is a valid
		// non-identifier character so we don't match "avg_over_time" as "avg".
		switch rest[0] {
		case '(', ' ', '\t', '\n', '\r':
			return agg, true
		}
	}
	return "", false
}

// CompleteOuterAggregator returns the outer aggregation only when it consumes
// the complete query. This lets callers distinguish sum(up) from shapes such
// as sum(up) + vector(1), which cannot use the same cross-shard merge plan.
func CompleteOuterAggregator(query string) (string, bool) {
	agg, _, found := CompleteOuterAggregation(query)
	return agg, found
}

// CompleteOuterAggregation returns the outer aggregation and its vector input
// only when the aggregation consumes the complete query.
func CompleteOuterAggregation(query string) (string, string, bool) {
	q := strings.TrimSpace(query)
	agg, found := OuterAggregator(q)
	if !found {
		return "", "", false
	}
	rest := strings.TrimSpace(q[len(agg):])

	var hasGrouping bool
	if _, next, ok := parseGrouping(rest); ok {
		hasGrouping = true
		rest = next
	}
	if rest == "" || rest[0] != '(' {
		return "", "", false
	}
	closeIdx := findMatchingCloser(rest, 0, '(', ')')
	if closeIdx < 0 {
		return "", "", false
	}
	args := strings.TrimSpace(rest[1:closeIdx])
	trailer := strings.TrimSpace(rest[closeIdx+1:])
	if !hasGrouping && trailer != "" {
		if _, next, ok := parseGrouping(trailer); ok {
			trailer = next
		}
	}
	if trailer != "" || args == "" {
		return "", "", false
	}

	input := args
	switch agg {
	case aggregation.CountValues, aggregation.TopK, aggregation.BottomK,
		aggregation.Quantile, aggregation.LimitK, aggregation.LimitRatio:
		comma := findTopLevelComma(args)
		if comma < 0 || strings.TrimSpace(args[:comma]) == "" {
			return "", "", false
		}
		input = strings.TrimSpace(args[comma+1:])
	}
	if input == "" || findTopLevelComma(input) >= 0 {
		return "", "", false
	}
	return agg, input, true
}

// ContainsAggregator reports whether query contains an aggregation expression,
// including one nested below a function or parenthesized expression.
func ContainsAggregator(query string) bool {
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
		operator := q[start:i]
		if !slices.Contains(AllAggregators, operator) {
			continue
		}
		for i < len(q) && isPromQLSpace(q[i]) {
			i++
		}
		if i < len(q) && q[i] == '(' {
			return true
		}
		for _, grouping := range []string{"by", "without"} {
			if strings.HasPrefix(q[i:], grouping) &&
				(i+len(grouping) == len(q) || !isPromQLIdentifierPart(q[i+len(grouping)])) {
				return true
			}
		}
	}
	return false
}

// ReplaceOuterAggregator substitutes the outermost aggregator keyword in
// query with replacement, preserving the rest of the query verbatim.
// aggregator must match the lowercased aggregator as returned by OuterAggregator.
//
// Example:
//
//	ReplaceOuterAggregator("avg by (r) (errors)", "avg", "sum") → "sum by (r) (errors)"
func ReplaceOuterAggregator(query, aggregator, replacement string) string {
	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	if strings.HasPrefix(ql, aggregator) {
		// Preserve original casing for the remainder of the query
		return replacement + q[len(aggregator):]
	}
	return query
}
