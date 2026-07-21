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
	spec, found := parseParameterizedAggregation(query, aggregation.LimitRatio)
	if !found {
		return LimitRatioAggregation{}, false
	}
	ratio, ok := parseLimitRatio(spec.Parameter)
	if !ok {
		return LimitRatioAggregation{}, false
	}

	return LimitRatioAggregation{
		Ratio:            ratio,
		InnerQuery:       spec.InnerQuery,
		AggregationQuery: spec.AggregationQuery,
		Grouping:         spec.Grouping,
		SortSet:          spec.SortSet,
		SortDescending:   spec.SortDescending,
	}, true
}

func parseLimitRatio(input string) (float64, bool) {
	ratio, ok := parsePromQLScalarLiteral(strings.TrimSpace(input))
	if !ok || math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < -1 || ratio > 1 {
		return 0, false
	}
	return ratio, true
}
