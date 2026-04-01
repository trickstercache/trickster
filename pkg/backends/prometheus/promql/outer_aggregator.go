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

import "strings"

// allAggregators lists all known PromQL aggregation operators, sorted with
// longer names first to avoid prefix collisions (e.g. count_values vs count).
var allAggregators = []string{
	"count_values",
	"limit_ratio",
	"bottomk",
	"limitk",
	"stddev",
	"stdvar",
	"quantile",
	"topk",
	"count",
	"group",
	"avg",
	"sum",
	"min",
	"max",
}

// orderedAggregators is the deduplicated version of allAggregators, built at
// init time. Longest names are checked first to avoid prefix collisions.
var orderedAggregators []string

func init() {
	seen := make(map[string]bool)
	for _, a := range allAggregators {
		if !seen[a] {
			seen[a] = true
			orderedAggregators = append(orderedAggregators, a)
		}
	}
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
	for _, agg := range orderedAggregators {
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
