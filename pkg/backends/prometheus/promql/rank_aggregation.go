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

	"github.com/prometheus/prometheus/promql/parser"
)

// RankAggregation describes an outer topk or bottomk aggregation with a
// literal integral k, optionally wrapped in sort or sort_desc.
type RankAggregation struct {
	Operator       string
	K              int
	Inner          Expr
	Grouping       AggregationGrouping
	SortSet        bool
	SortDescending bool
}

const maxRankK = float64(math.MaxInt)

// ParseRankAggregation parses an outer topk or bottomk aggregation.
func ParseRankAggregation(e Expr) (RankAggregation, bool) {
	spec, found := parseOuterAggregation(e, aggregation.TopK, aggregation.BottomK)
	if !found {
		return RankAggregation{}, false
	}
	k, ok := parseRankK(spec.Parameter)
	if !ok {
		return RankAggregation{}, false
	}
	return RankAggregation{
		Operator:       spec.Operator,
		K:              k,
		Inner:          spec.Inner,
		Grouping:       spec.Grouping,
		SortSet:        spec.SortSet,
		SortDescending: spec.SortDescending,
	}, true
}

func parseRankK(parameter parser.Expr) (int, bool) {
	k, ok := scalarLiteral(parameter)
	if !ok || !(k >= 0) || k >= maxRankK || k != math.Trunc(k) {
		return 0, false
	}
	return int(k), true
}
