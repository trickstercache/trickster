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
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

// LimitRatioAggregation describes a literal limit_ratio aggregation that TSM
// can either leave on each shard or apply after globally merging its input.
type LimitRatioAggregation struct {
	Ratio            float64
	InnerQuery       string
	AggregationQuery string
	Grouping         AggregationGrouping
	SortSet          bool
	SortDescending   bool
}

// ParseLimitRatioAggregation parses an outer literal limit_ratio aggregation,
// optionally wrapped in sort or sort_desc. Scalar parameter expressions are
// deliberately rejected because TSM cannot safely evaluate them per shard.
func ParseLimitRatioAggregation(query string) (LimitRatioAggregation, bool) {
	q := strings.TrimSpace(query)
	if sortSpec, ok := ParseSortWrapper(q); ok {
		spec, found := parseLimitRatioAggregation(sortSpec.InnerQuery)
		if found {
			spec.SortSet = true
			spec.SortDescending = sortSpec.Descending
		}
		return spec, found
	}
	return parseLimitRatioAggregation(q)
}

func parseLimitRatioAggregation(query string) (LimitRatioAggregation, bool) {
	const operator = aggregation.LimitRatio

	q := strings.TrimSpace(query)
	ql := strings.ToLower(q)
	if !strings.HasPrefix(ql, operator) ||
		(len(q) > len(operator) && !isPromQLBoundary(q[len(operator)])) {
		return LimitRatioAggregation{}, false
	}
	aggregationQuery := q
	q = strings.TrimSpace(q[len(operator):])

	var grouping AggregationGrouping
	var hasGrouping bool
	if g, rest, ok := parseGrouping(q); ok {
		grouping = g
		hasGrouping = true
		q = rest
	}

	if q == "" || q[0] != '(' {
		return LimitRatioAggregation{}, false
	}
	closeIdx := findMatchingCloser(q, 0, '(', ')')
	if closeIdx < 0 {
		return LimitRatioAggregation{}, false
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
		return LimitRatioAggregation{}, false
	}

	comma := findTopLevelComma(args)
	if comma < 0 {
		return LimitRatioAggregation{}, false
	}
	ratio, ok := parseLimitRatio(args[:comma])
	if !ok {
		return LimitRatioAggregation{}, false
	}
	innerQuery := strings.TrimSpace(args[comma+1:])
	if innerQuery == "" || findTopLevelComma(innerQuery) >= 0 {
		return LimitRatioAggregation{}, false
	}
	if !hasGrouping {
		grouping = AggregationGrouping{}
	}

	return LimitRatioAggregation{
		Ratio:            ratio,
		InnerQuery:       innerQuery,
		AggregationQuery: aggregationQuery,
		Grouping:         grouping,
	}, true
}

func parseLimitRatio(input string) (float64, bool) {
	ratio, err := strconv.ParseFloat(strings.TrimSpace(input), 64)
	if err != nil || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < -1 || ratio > 1 {
		return 0, false
	}
	return ratio, true
}
