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
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"

	"github.com/prometheus/prometheus/promql/parser"
)

// PresenceAggregation describes an outer count or group over an inner count or group of a vector
// selector, optionally wrapped in sort or sort_desc. Its result depends only on which inner label sets exist.
type PresenceAggregation struct {
	Operator         string
	Inner            Expr
	AggregationQuery string
	Grouping         AggregationGrouping
	SortSet          bool
	SortDescending   bool
}

// ParsePresenceAggregation matches an outer count or group whose input is a count or group of a
// vector selector. Other inputs can change which label sets each backend returns, so they don't match.
func ParsePresenceAggregation(e Expr) (PresenceAggregation, bool) {
	spec, found := parseOuterAggregation(e, aggregation.Count, aggregation.Group)
	if !found {
		return PresenceAggregation{}, false
	}
	inner, ok := spec.Inner.node.(*parser.AggregateExpr)
	if !ok || !isPresenceOperator(inner.Op.String()) {
		return PresenceAggregation{}, false
	}
	if _, ok := spec.Inner.child(inner.Expr).node.(*parser.VectorSelector); !ok {
		return PresenceAggregation{}, false
	}
	return PresenceAggregation{
		Operator:         spec.Operator,
		Inner:            spec.Inner,
		AggregationQuery: spec.Aggregation.String(),
		Grouping:         spec.Grouping,
		SortSet:          spec.SortSet,
		SortDescending:   spec.SortDescending,
	}, true
}

func isPresenceOperator(operator string) bool {
	return operator == aggregation.Count || operator == aggregation.Group
}
