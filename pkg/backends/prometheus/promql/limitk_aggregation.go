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

	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
)

// This is the first float64 value at the upper int64 boundary that the current
// Prometheus evaluator rejects before converting a ranking parameter to int64.
const prometheusLimitKMaxInt64 = 9223372036854774784.0

// LimitKAggregation describes an outer literal limitk aggregation, optionally
// wrapped in sort or sort_desc.
type LimitKAggregation struct {
	K                int64
	InnerQuery       string
	AggregationQuery string
	Grouping         AggregationGrouping
	SortSet          bool
	SortDescending   bool
}

// ParseLimitKAggregation parses an outer limitk with a non-negative literal
// scalar parameter. Prometheus truncates positive fractional parameters when
// converting them to int64, after separately rejecting NaN and overflow.
func ParseLimitKAggregation(query string) (LimitKAggregation, bool) {
	spec, found := parseParameterizedAggregation(query, aggregation.LimitK)
	if !found {
		return LimitKAggregation{}, false
	}
	parameter, ok := parsePromQLScalarLiteral(spec.Parameter)
	if !ok || math.IsNaN(parameter) || math.IsInf(parameter, 0) || parameter < 0 ||
		parameter >= prometheusLimitKMaxInt64 {
		return LimitKAggregation{}, false
	}
	return LimitKAggregation{
		K:                int64(parameter),
		InnerQuery:       spec.InnerQuery,
		AggregationQuery: spec.AggregationQuery,
		Grouping:         spec.Grouping,
		SortSet:          spec.SortSet,
		SortDescending:   spec.SortDescending,
	}, true
}
