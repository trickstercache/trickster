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
	"maps"
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
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
	query, _ = tsmInnerQuery(query)
	return classifyPromMerge(query)
}

func tsmInnerQuery(query string) (string, bool) {
	if spec, ok := promql.ParseRankAggregation(query); ok {
		return spec.InnerQuery, true
	}
	if spec, ok := promql.ParseSortWrapper(query); ok {
		if _, found := promql.OuterAggregator(spec.InnerQuery); found {
			return spec.InnerQuery, true
		}
	}
	return query, false
}

func classifyPromMerge(query string) (strategy int, needsDualQuery bool, warning string) {
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

// RewriteForTSMMerge removes Prometheus operations that must run after TSM
// fanout. The shards receive the inner query; FinalizeTSMMerge later applies
// the original rank and/or sort operation to the merged result.
func (c *Client) RewriteForTSMMerge(r *http.Request, query string) (*http.Request, string) {
	innerQuery, ok := tsmInnerQuery(query)
	if !ok {
		return r, query
	}
	return rewritePromQueryParam(r, innerQuery, "tsm finalizer"), innerQuery
}

// RewriteForWeightedAvg implements backends.TSMMergeProvider for the Prometheus
// backend. It returns two copies of r — one with the outer "avg" aggregator
// replaced by "sum" and one by "count" — with the rewritten expression injected
// into the Prometheus "query" parameter. Both GET (query string) and POST
// (request body) encodings are handled transparently by params.SetRequestValues.
func (c *Client) RewriteForWeightedAvg(r *http.Request, query string) (*http.Request, *http.Request) {
	query, _ = tsmInnerQuery(query)

	sumQuery := promql.ReplaceOuterAggregator(query, "avg", "sum")
	countQuery := promql.ReplaceOuterAggregator(query, "avg", "count")
	sumReq := rewritePromQueryParam(r, sumQuery, "avg aggregator sumReq")
	countReq := rewritePromQueryParam(r, countQuery, "avg aggregator countReq")

	return sumReq, countReq
}

func rewritePromQueryParam(r *http.Request, query, cloneName string) *http.Request {
	qp, _, _ := params.GetRequestValues(r)
	req, err := request.Clone(r)
	if err != nil {
		logger.Error("failed to clone "+cloneName, logging.Pairs{"error": err})
		return r
	}
	nextQP := maps.Clone(qp)
	if nextQP == nil {
		nextQP = url.Values{}
	}
	nextQP.Set(promQueryParam, query)
	params.SetRequestValues(req, nextQP)
	return req
}
