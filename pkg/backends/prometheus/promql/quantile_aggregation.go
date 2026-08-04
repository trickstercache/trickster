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

import "github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"

// QuantileAggregation describes an outer literal quantile aggregation,
// optionally wrapped in sort or sort_desc.
type QuantileAggregation struct {
	Phi              float64
	InnerQuery       string
	AggregationQuery string
	Grouping         AggregationGrouping
	SortSet          bool
	SortDescending   bool
}

// ParseQuantileAggregation parses an outer quantile with a literal scalar
// parameter. Scalar expressions are left on the established warning fallback
// because TSM cannot evaluate them once per global timestamp.
func ParseQuantileAggregation(query string) (QuantileAggregation, bool) {
	spec, found := parseParameterizedAggregation(query, aggregation.Quantile)
	if !found {
		return QuantileAggregation{}, false
	}
	phi, ok := parsePromQLScalarLiteral(spec.Parameter)
	if !ok {
		return QuantileAggregation{}, false
	}
	return QuantileAggregation{
		Phi:              phi,
		InnerQuery:       spec.InnerQuery,
		AggregationQuery: spec.AggregationQuery,
		Grouping:         spec.Grouping,
		SortSet:          spec.SortSet,
		SortDescending:   spec.SortDescending,
	}, true
}
