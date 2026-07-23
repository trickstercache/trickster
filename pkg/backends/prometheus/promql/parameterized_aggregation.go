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

// parameterizedAggregation is the shared syntactic shape used by PromQL
// aggregators whose first argument is a scalar parameter and whose second
// argument is a vector expression.
type parameterizedAggregation struct {
	Parameter        string
	InnerQuery       string
	AggregationQuery string
	Grouping         AggregationGrouping
	SortSet          bool
	SortDescending   bool
}

func parseParameterizedAggregation(query, operator string) (parameterizedAggregation, bool) {
	q := strings.TrimSpace(query)
	if sortSpec, ok := ParseSortWrapper(q); ok {
		spec, found := parseDirectParameterizedAggregation(sortSpec.InnerQuery, operator)
		if found {
			spec.SortSet = true
			spec.SortDescending = sortSpec.Descending
		}
		return spec, found
	}
	return parseDirectParameterizedAggregation(q, operator)
}

func parseDirectParameterizedAggregation(query, operator string) (parameterizedAggregation, bool) {
	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	if !strings.HasPrefix(ql, operator) ||
		(len(q) > len(operator) && !isPromQLBoundary(q[len(operator)])) {
		return parameterizedAggregation{}, false
	}

	pos := skipPromQLSpaces(q, len(operator))
	grouping, next, hasPrefixGrouping := parseGroupingAt(q, pos)
	if hasPrefixGrouping {
		pos = skipPromQLSpaces(q, next)
	}
	if pos >= len(q) || q[pos] != '(' {
		return parameterizedAggregation{}, false
	}
	closeIdx := findMatchingCloser(q, pos, '(', ')')
	if closeIdx < 0 {
		return parameterizedAggregation{}, false
	}

	args := q[pos+1 : closeIdx]
	comma := findTopLevelComma(args)
	if comma < 0 {
		return parameterizedAggregation{}, false
	}
	parameter := strings.TrimSpace(args[:comma])
	innerQuery := strings.TrimSpace(args[comma+1:])
	if parameter == "" || innerQuery == "" || findTopLevelComma(innerQuery) >= 0 {
		return parameterizedAggregation{}, false
	}

	trailer := skipPromQLSpaces(q, closeIdx+1)
	if !hasPrefixGrouping && trailer < len(q) {
		var ok bool
		grouping, trailer, ok = parseGroupingAt(q, trailer)
		if !ok {
			return parameterizedAggregation{}, false
		}
		trailer = skipPromQLSpaces(q, trailer)
	}
	if trailer != len(q) {
		return parameterizedAggregation{}, false
	}

	return parameterizedAggregation{
		Parameter:        parameter,
		InnerQuery:       innerQuery,
		AggregationQuery: q,
		Grouping:         grouping,
	}, true
}
