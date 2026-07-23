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
	if spec, found := promql.ParseLimitRatioAggregation(query); found {
		return c.planLimitRatio(r, query, spec)
	}
	if spec, found := promql.ParseQuantileAggregation(query); found {
		return c.planQuantile(r, query, spec)
	}
	if spec, found := promql.ParseVarianceAggregation(query); found {
		if plan, handled, err := c.planVariance(r, query, spec); handled || err != nil {
			return plan, err
		}
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
	if found {
		switch agg {
		case aggregation.Sum, aggregation.Count, aggregation.CountValues:
			strategy = int(merge.StrategySum)
		case aggregation.Average:
			return weightedAveragePlan(r, query, fanoutQuery, finalizer, false)
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

func (c *Client) planQuantile(r *http.Request, query string,
	spec promql.QuantileAggregation,
) (*merge.TSMMergePlan, error) {
	return c.planGlobalParameterizedAggregation(r, query, aggregation.Quantile,
		spec.InnerQuery, spec.AggregationQuery, spec.SortSet)
}

func (c *Client) planGlobalParameterizedAggregation(r *http.Request, query, operator,
	innerQuery, aggregationQuery string, sortSet bool,
) (*merge.TSMMergePlan, error) {
	strategy, warning, weightedAverage := globalInnerMergeStrategy(operator, innerQuery)
	supported := warning == ""

	fanoutQuery := innerQuery
	rewritten := true
	finalizer := merge.TSMFinalizerSpec{Enabled: true, Query: query}
	if weightedAverage {
		return weightedAveragePlan(r, query, innerQuery, finalizer, true)
	}
	if !supported {
		fanoutQuery = aggregationQuery
		rewritten = sortSet
		if !sortSet {
			finalizer = merge.TSMFinalizerSpec{}
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
		Reduction: merge.TSMReductionSpec{
			Kind:          merge.TSMReductionStandard,
			InputVariants: merge.TSMReductionPrimaryVariant(),
		},
		Finalizer:           finalizer,
		Completeness:        merge.TSMCompletenessResponseAuthority,
		UnsupportedWarning:  warning,
		StripInjectedLabels: true,
	}
	plan.AllowSingleMemberBypass = !rewritten && !finalizer.Enabled && warning == ""
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

func globalInnerMergeStrategy(operator, innerQuery string) (int, string, bool) {
	strategy := int(merge.StrategyDedup)
	warningPrefix := "trickster: " + operator + " "

	if innerAggregation, aggregationInput, found := promql.CompleteOuterAggregation(innerQuery); found {
		if promql.ContainsAggregator(aggregationInput) {
			return strategy, warningPrefix + "contains a nested aggregation that cannot be " +
				"correctly merged across fanout backends; results may be inaccurate", false
		}
		if promql.ContainsBinaryExpression(aggregationInput) {
			return strategy, warningPrefix + "contains a binary expression that may require " +
				"cross-shard matching; results may be inaccurate", false
		}
		if globalFunction, found := promql.NonShardLocalFunction(aggregationInput); found {
			return strategy, warningPrefix + `contains function "` + globalFunction +
				`" that may require globally complete input; results may be inaccurate`, false
		}

		switch innerAggregation {
		case aggregation.Sum, aggregation.Count, aggregation.CountValues:
			return int(merge.StrategySum), "", false
		case aggregation.Average:
			return int(merge.StrategySum), "", true
		case aggregation.Minimum:
			return int(merge.StrategyMin), "", false
		case aggregation.Maximum:
			return int(merge.StrategyMax), "", false
		case aggregation.Group:
			return strategy, "", false
		default:
			return strategy, warningPrefix + `inner aggregator "` + innerAggregation +
				`" cannot be correctly merged across fanout backends; results may be inaccurate`, false
		}
	}

	if promql.ContainsAggregator(innerQuery) {
		return strategy, warningPrefix + "contains a nested aggregation that cannot be " +
			"correctly merged across fanout backends; results may be inaccurate", false
	}
	if promql.ContainsBinaryExpression(innerQuery) {
		return strategy, warningPrefix + "contains a binary expression that may require " +
			"cross-shard matching; results may be inaccurate", false
	}
	if globalFunction, found := promql.NonShardLocalFunction(innerQuery); found {
		return strategy, warningPrefix + `contains function "` + globalFunction +
			`" that may require globally complete input; results may be inaccurate`, false
	}
	return strategy, "", false
}

func (c *Client) planVariance(r *http.Request, query string,
	spec promql.VarianceAggregation,
) (*merge.TSMMergePlan, bool, error) {
	finalizer := merge.TSMFinalizerSpec{Enabled: true, Query: query}
	if innerAggregation, aggregationInput, found := promql.CompleteOuterAggregation(spec.InnerQuery); found {
		if promql.ContainsAggregator(aggregationInput) ||
			promql.ContainsBinaryExpression(aggregationInput) {
			return nil, false, nil
		}
		if _, found := promql.NonShardLocalFunction(aggregationInput); found {
			return nil, false, nil
		}

		strategy := int(merge.StrategyDedup)
		switch innerAggregation {
		case aggregation.Sum, aggregation.Count, aggregation.CountValues:
			strategy = int(merge.StrategySum)
		case aggregation.Average:
			plan, err := weightedAveragePlan(r, query, spec.InnerQuery, finalizer, true)
			return plan, true, err
		case aggregation.Minimum:
			strategy = int(merge.StrategyMin)
		case aggregation.Maximum:
			strategy = int(merge.StrategyMax)
		case aggregation.Group:
		default:
			return nil, false, nil
		}

		variantRequest, err := rewritePromQueryParam(r, spec.InnerQuery)
		if err != nil {
			return nil, true, fmt.Errorf("prepare tsm primary variant: %w", err)
		}
		plan := &merge.TSMMergePlan{
			OriginalQuery: query,
			Variants: []merge.TSMQueryVariant{{
				Name:              merge.TSMVariantPrimary,
				Request:           variantRequest,
				MergeStrategy:     strategy,
				ResponseAuthority: true,
			}},
			Reduction: merge.TSMReductionSpec{
				Kind:          merge.TSMReductionStandard,
				InputVariants: merge.TSMReductionPrimaryVariant(),
			},
			Finalizer:           finalizer,
			Completeness:        merge.TSMCompletenessResponseAuthority,
			StripInjectedLabels: true,
		}
		if err := plan.Validate(); err != nil {
			return nil, true, err
		}
		return plan, true, nil
	}

	if promql.ContainsAggregator(spec.InnerQuery) ||
		promql.ContainsBinaryExpression(spec.InnerQuery) {
		return nil, false, nil
	}
	if _, found := promql.NonShardLocalFunction(spec.InnerQuery); found {
		return nil, false, nil
	}

	plan, err := pooledVariancePlan(r, query, spec)
	return plan, true, err
}

func pooledVariancePlan(r *http.Request, originalQuery string,
	spec promql.VarianceAggregation,
) (*merge.TSMMergePlan, error) {
	variantNames := merge.TSMReductionPooledVarianceVariants()
	operators := []string{aggregation.Count, aggregation.Average, aggregation.StdVar}
	variants := make([]merge.TSMQueryVariant, len(variantNames))
	for i, name := range variantNames {
		variantQuery := promql.VarianceVariantQuery(spec, operators[i])
		variantRequest, err := rewritePromQueryParam(r, variantQuery)
		if err != nil {
			return nil, fmt.Errorf("prepare tsm %s variant: %w", name, err)
		}
		variants[i] = merge.TSMQueryVariant{
			Name:              name,
			Request:           variantRequest,
			MergeStrategy:     int(merge.StrategyDedup),
			ResponseAuthority: i == 0,
		}
	}

	plan := &merge.TSMMergePlan{
		OriginalQuery: originalQuery,
		Variants:      variants,
		Reduction: merge.TSMReductionSpec{
			Kind:          merge.TSMReductionPooledVariance,
			InputVariants: variantNames,
		},
		Finalizer: merge.TSMFinalizerSpec{
			Enabled: true,
			Query:   originalQuery,
		},
		Completeness:        merge.TSMCompletenessAllVariants,
		StripInjectedLabels: true,
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

func (c *Client) planLimitRatio(r *http.Request, query string,
	spec promql.LimitRatioAggregation,
) (*merge.TSMMergePlan, error) {
	fanoutQuery := spec.AggregationQuery
	rewritten := spec.SortSet
	strategy := int(merge.StrategyDedup)
	unsupportedWarning := ""
	finalizer := merge.TSMFinalizerSpec{}
	if spec.SortSet {
		finalizer = merge.TSMFinalizerSpec{Enabled: true, Query: query}
	}

	if agg, aggregationInput, found := promql.CompleteOuterAggregation(spec.InnerQuery); found {
		candidateStrategy := int(merge.StrategyDedup)
		weightedAverage := false
		switch agg {
		case aggregation.Sum, aggregation.Count, aggregation.CountValues:
			candidateStrategy = int(merge.StrategySum)
		case aggregation.Average:
			weightedAverage = true
		case aggregation.Minimum:
			candidateStrategy = int(merge.StrategyMin)
		case aggregation.Maximum:
			candidateStrategy = int(merge.StrategyMax)
		case aggregation.Group:
			// Deduplication unions the per-shard groups, whose values are all one.
		default:
			unsupportedWarning = `trickster: limit_ratio inner aggregator "` + agg +
				`" cannot be correctly merged across fanout backends; results may be inaccurate`
		}
		if unsupportedWarning == "" {
			globalFunction, hasGlobalFunction := promql.NonShardLocalFunction(aggregationInput)
			switch {
			case promql.ContainsAggregator(aggregationInput):
				unsupportedWarning = "trickster: limit_ratio contains a nested aggregation that " +
					"cannot be correctly merged across fanout backends; results may be inaccurate"
			case promql.ContainsBinaryExpression(aggregationInput):
				unsupportedWarning = "trickster: limit_ratio contains a binary expression that " +
					"may require cross-shard matching; results may be inaccurate"
			case hasGlobalFunction:
				unsupportedWarning = `trickster: limit_ratio contains function "` + globalFunction +
					`" that may require globally complete input; results may be inaccurate`
			case weightedAverage:
				return weightedAveragePlan(r, query, spec.InnerQuery,
					merge.TSMFinalizerSpec{Enabled: true, Query: query}, true)
			default:
				strategy = candidateStrategy
				fanoutQuery = spec.InnerQuery
				rewritten = true
				finalizer = merge.TSMFinalizerSpec{Enabled: true, Query: query}
			}
		}
	} else if promql.ContainsAggregator(spec.InnerQuery) {
		unsupportedWarning = "trickster: limit_ratio contains a nested aggregation that " +
			"cannot be correctly merged across fanout backends; results may be inaccurate"
	} else if promql.ContainsBinaryExpression(spec.InnerQuery) {
		unsupportedWarning = "trickster: limit_ratio contains a binary expression that " +
			"may require cross-shard matching; results may be inaccurate"
	} else if globalFunction, found := promql.NonShardLocalFunction(spec.InnerQuery); found {
		unsupportedWarning = `trickster: limit_ratio contains function "` + globalFunction +
			`" that may require globally complete input; results may be inaccurate`
	}
	if spec.SortSet {
		// Global ordering already requires a finalizer. Always merge the
		// unsampled inner vectors so finalization applies the ratio exactly once,
		// including when the inner expression retains an inaccuracy warning.
		fanoutQuery = spec.InnerQuery
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
		Reduction: merge.TSMReductionSpec{
			Kind:          merge.TSMReductionStandard,
			InputVariants: merge.TSMReductionPrimaryVariant(),
		},
		Finalizer:           finalizer,
		Completeness:        merge.TSMCompletenessResponseAuthority,
		UnsupportedWarning:  unsupportedWarning,
		StripInjectedLabels: true,
	}
	plan.AllowSingleMemberBypass = !rewritten && !finalizer.Enabled && unsupportedWarning == ""
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

func weightedAveragePlan(r *http.Request, originalQuery, fanoutQuery string,
	finalizer merge.TSMFinalizerSpec, stripInjectedLabels bool,
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
		Finalizer:           finalizer,
		Completeness:        merge.TSMCompletenessAllVariants,
		StripInjectedLabels: stripInjectedLabels,
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
