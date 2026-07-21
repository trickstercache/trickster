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
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/aggregation"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

// promQueryParam is the Prometheus HTTP API parameter name for the query
// expression, used in both GET (query string) and POST (request body) requests.
const promQueryParam = "query"

// PlanTSMMerge constructs the complete TSM execution plan for a Prometheus
// request. Query syntax and wire-format rewriting stay provider-owned; the ALB
// executor only consumes variants and reduction metadata.
func (c *Client) PlanTSMMerge(r *http.Request, query string) (*merge.TSMMergePlan, error) {
	if r == nil {
		return nil, errors.New("cannot plan a nil request")
	}
	fanoutQuery, rewritten := tsmInnerQuery(query)
	finalizer := tsmFinalizer(query)

	strategy := int(merge.StrategyDedup)
	unsupportedWarning := ""
	reduction := merge.TSMReductionSpec{
		Kind:          merge.TSMReductionStandard,
		InputVariants: merge.TSMReductionPrimaryVariant(),
	}
	completeness := merge.TSMCompletenessResponseAuthority

	agg, found := promql.OuterAggregator(fanoutQuery)
	if promql.IsScalarExpression(fanoutQuery) {
		strategy = int(merge.StrategyScalar)
	} else if found {
		switch agg {
		case aggregation.Sum, aggregation.Count, aggregation.CountValues:
			strategy = int(merge.StrategySum)
		case aggregation.Average:
			return weightedAveragePlan(r, query, fanoutQuery, finalizer)
		case aggregation.Minimum:
			strategy = int(merge.StrategyMin)
		case aggregation.Maximum:
			strategy = int(merge.StrategyMax)
		case aggregation.StdDev, aggregation.StdVar, aggregation.Quantile,
			aggregation.TopK, aggregation.BottomK, aggregation.LimitK,
			aggregation.LimitRatio:
			unsupportedWarning = `trickster: outer aggregator "` + agg + `" cannot be correctly ` +
				`merged across fanout backends; results may be inaccurate`
		}
	}

	variantRequest := r
	var err error
	if rewritten {
		variantRequest, err = rewritePromQueryParam(r, fanoutQuery)
		if err != nil {
			return nil, fmt.Errorf("prepare tsm primary variant: %w", err)
		}
	}

	plan := &merge.TSMMergePlan{
		OriginalQuery: query,
		Variants: []merge.TSMQueryVariant{{
			Name:              merge.TSMVariantPrimary,
			Request:           variantRequest,
			MergeStrategy:     strategy,
			ResponseAuthority: true,
		}},
		Reduction:          reduction,
		Finalizer:          finalizer,
		Completeness:       completeness,
		UnsupportedWarning: unsupportedWarning,
	}
	plan.AllowSingleMemberBypass = !rewritten && !finalizer.Enabled && unsupportedWarning == ""
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

func weightedAveragePlan(r *http.Request, originalQuery, fanoutQuery string,
	finalizer merge.TSMFinalizerSpec,
) (*merge.TSMMergePlan, error) {
	sumQuery := promql.ReplaceOuterAggregator(fanoutQuery, aggregation.Average, aggregation.Sum)
	countQuery := promql.ReplaceOuterAggregator(fanoutQuery, aggregation.Average, aggregation.Count)
	sumReq, err := rewritePromQueryParam(r, sumQuery)
	if err != nil {
		return nil, fmt.Errorf("prepare tsm %s variant: %w",
			merge.TSMVariantWeightedAverageSum, err)
	}
	countReq, err := rewritePromQueryParam(r, countQuery)
	if err != nil {
		return nil, fmt.Errorf("prepare tsm %s variant: %w",
			merge.TSMVariantWeightedAverageCount, err)
	}

	plan := &merge.TSMMergePlan{
		OriginalQuery: originalQuery,
		Variants: []merge.TSMQueryVariant{
			{
				Name:              merge.TSMVariantWeightedAverageSum,
				Request:           sumReq,
				MergeStrategy:     int(merge.StrategySum),
				ResponseAuthority: true,
			},
			{
				Name:          merge.TSMVariantWeightedAverageCount,
				Request:       countReq,
				MergeStrategy: int(merge.StrategySum),
			},
		},
		Reduction: merge.TSMReductionSpec{
			Kind:          merge.TSMReductionWeightedAverage,
			InputVariants: merge.TSMReductionWeightedAverageVariants(),
		},
		Finalizer:    finalizer,
		Completeness: merge.TSMCompletenessAllVariants,
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
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

func tsmFinalizer(query string) merge.TSMFinalizerSpec {
	if _, ok := promql.ParseRankAggregation(query); ok {
		return merge.TSMFinalizerSpec{Enabled: true, Query: query}
	}
	if _, ok := promql.ParseSortWrapper(query); ok {
		return merge.TSMFinalizerSpec{Enabled: true, Query: query}
	}
	return merge.TSMFinalizerSpec{}
}

func rewritePromQueryParam(r *http.Request, query string) (*http.Request, error) {
	qp, _, _ := params.GetRequestValues(r)
	req, err := request.Clone(r)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.New("cannot rewrite a nil request")
	}
	nextQP := maps.Clone(qp)
	if nextQP == nil {
		nextQP = url.Values{}
	}
	nextQP.Set(promQueryParam, query)
	params.SetRequestValues(req, nextQP)
	return req, nil
}

var _ backends.TSMMergeProvider = (*Client)(nil)
