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

package prometheus

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

// promQueryParam is the Prometheus HTTP API parameter name for the query
// expression, used in both GET (query string) and POST (request body) requests.
const promQueryParam = "query"

// ClassifyMerge implements backends.TSMMergeProvider for the Prometheus backend.
// It inspects the outermost PromQL aggregation operator and returns the merge
// strategy (as the int value of dataset.MergeStrategy), whether a dual-query
// weighted average is required, and an optional warning for operators whose
// results cannot be correctly merged across shards.
func (c *Client) ClassifyMerge(query string) (strategy int, needsDualQuery bool, warning string) {
	agg, found := promql.OuterAggregator(query)
	if !found {
		return int(dataset.MergeStrategyDedup), false, ""
	}
	switch agg {
	case "sum", "count", "count_values":
		return int(dataset.MergeStrategySum), false, ""
	case "avg":
		// needsDualQuery=true: TSM fires two sub-queries (sum + count) per shard
		// and calls FinalizeWeightedAvg(..., originalQuery) to compute the weighted mean.
		// MergeStrategySum is used during accumulation; the final division happens
		// after all shards have responded.
		return int(dataset.MergeStrategySum), true, ""
	case "min":
		return int(dataset.MergeStrategyMin), false, ""
	case "max":
		return int(dataset.MergeStrategyMax), false, ""
	case "stddev", "stdvar", "quantile", "topk", "bottomk", "limitk", "limit_ratio":
		return int(dataset.MergeStrategyDedup), false,
			`trickster: outer aggregator "` + agg + `" cannot be correctly ` +
				`merged across fanout backends; results may be inaccurate`
	default: // covers "group"
		return int(dataset.MergeStrategyDedup), false, ""
	}
}

// RewriteForWeightedAvg implements backends.TSMMergeProvider for the Prometheus
// backend. It returns two copies of r — one with the outer "avg" aggregator
// replaced by "sum" and one by "count" — with the rewritten expression injected
// into the Prometheus "query" parameter. Both GET (query string) and POST
// (request body) encodings are handled transparently by params.SetRequestValues.
func (c *Client) RewriteForWeightedAvg(r *http.Request, query string) (sumReq, countReq *http.Request) {
	sumQuery := promql.ReplaceOuterAggregator(query, "avg", "sum")
	countQuery := promql.ReplaceOuterAggregator(query, "avg", "count")

	sumReq = r.Clone(r.Context())
	qp, _, _ := params.GetRequestValues(sumReq)
	qp.Set(promQueryParam, sumQuery)
	params.SetRequestValues(sumReq, qp)

	countReq = r.Clone(r.Context())
	qp, _, _ = params.GetRequestValues(countReq)
	qp.Set(promQueryParam, countQuery)
	params.SetRequestValues(countReq, qp)

	return sumReq, countReq
}
