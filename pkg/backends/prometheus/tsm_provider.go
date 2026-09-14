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
	"strconv"
	"strings"

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

const (
	tsmUnparsableWarning = "trickster: query could not be parsed as PromQL and cannot be " +
		"correctly merged across fanout backends; results may be inaccurate"
	tsmBinaryAggregationWarning = "trickster: query contains an aggregation and binary " +
		"expression that may require global evaluation; results may be inaccurate"
	tsmNestedAggregationWarning = "trickster: query contains an aggregation that is not its " +
		"outermost operation and cannot be correctly merged across fanout backends; " +
		"results may be inaccurate"
)

// PlanTSMMerge constructs the complete TSM execution plan for a Prometheus
// request. Query syntax and wire-format rewriting stay provider-owned; the ALB
// executor only consumes variants and reduction metadata.
func (c *Client) PlanTSMMerge(r *http.Request, query string) (*merge.TSMMergePlan, error) {
	if r == nil {
		return nil, errors.New("cannot plan a nil request")
	}
	// An unparsable query leaves expr empty, so it falls through to deduplication.
	expr, err := promql.Parse(query)
	unparsable := err != nil && strings.TrimSpace(query) != ""
	if spec, found := promql.ParseLimitRatioAggregation(expr); found {
		return c.planLimitRatio(r, query, spec)
	}
	if spec, found := promql.ParseLimitKAggregation(expr); found {
		return c.planLimitK(r, query, spec)
	}
	if spec, found := promql.ParseQuantileAggregation(expr); found {
		return c.planQuantile(r, query, spec)
	}
	if spec, found := promql.ParseVarianceAggregation(expr); found {
		if plan, handled, err := c.planVariance(r, query, spec); handled || err != nil {
			return plan, err
		}
	}
	fanout, rewritten := tsmInnerQuery(expr)
	finalizer := tsmFinalizer(query, expr)

	strategy := int(merge.StrategyDedup)
	unsupportedWarning := ""
	reduction := merge.TSMReductionSpec{
		Kind:          merge.TSMReductionStandard,
		InputVariants: merge.TSMReductionPrimaryVariant(),
	}
	completeness := merge.TSMCompletenessResponseAuthority

	agg, aggregationInput, found := promql.CompleteOuterAggregation(fanout)
	switch {
	case unparsable:
		unsupportedWarning = tsmUnparsableWarning
	case fanout.IsScalar():
		strategy = int(merge.StrategyScalar)
	case found:
		inputWarning := shardInputWarning(agg, aggregationInput)
		switch agg {
		case aggregation.Sum, aggregation.Count, aggregation.CountValues:
			strategy = int(merge.StrategySum)
		case aggregation.Average:
			plan, err := weightedAveragePlan(r, query, fanout, finalizer, false)
			if plan != nil {
				plan.UnsupportedWarning = inputWarning
			}
			return plan, err
		case aggregation.Minimum:
			strategy = int(merge.StrategyMin)
		case aggregation.Maximum:
			strategy = int(merge.StrategyMax)
		case aggregation.StdDev, aggregation.StdVar, aggregation.Quantile,
			aggregation.TopK, aggregation.BottomK, aggregation.LimitK,
			aggregation.LimitRatio:
			unsupportedWarning = "trickster: outer aggregator " + strconv.Quote(agg) +
				" cannot be correctly merged across fanout backends; results may be inaccurate"
		}
		if unsupportedWarning == "" {
			unsupportedWarning = inputWarning
		}
	case zeroFallbackMergesBySum(fanout):
		strategy = int(merge.StrategySum)
	case fanout.ContainsAggregation() && fanout.ContainsBinaryExpression():
		unsupportedWarning = tsmBinaryAggregationWarning
	case fanout.ContainsAggregation():
		unsupportedWarning = tsmNestedAggregationWarning
	}

	variantRequest := r
	if rewritten {
		variantRequest, err = rewritePromQueryParam(r, fanout.String())
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

func zeroFallbackMergesBySum(e promql.Expr) bool {
	// Summing shard-local `agg or vector(0)` equals global evaluation because
	// shards without matches contribute only an explicit zero.
	fallback, found := promql.ParseZeroFallback(e)
	if !found {
		return false
	}
	switch fallback.Operator {
	case aggregation.Sum, aggregation.Count, aggregation.CountValues:
	default:
		return false
	}
	// A shard can only contribute zero correctly if its aggregation input is shard-local.
	if shardInputWarning(fallback.Operator, fallback.Input) != "" {
		return false
	}
	if fallback.DefaultMatching {
		return true
	}
	// With on or ignoring, the zero can only replace the single label-free series.
	return fallback.Operator != aggregation.CountValues && !fallback.Grouping.Without &&
		len(fallback.Grouping.Labels) == 0
}

func (c *Client) planLimitK(r *http.Request, query string,
	spec promql.LimitKAggregation,
) (*merge.TSMMergePlan, error) {
	return c.planGlobalParameterizedAggregation(r, query, aggregation.LimitK,
		spec.Inner, spec.AggregationQuery, spec.SortSet)
}

func (c *Client) planQuantile(r *http.Request, query string,
	spec promql.QuantileAggregation,
) (*merge.TSMMergePlan, error) {
	return c.planGlobalParameterizedAggregation(r, query, aggregation.Quantile,
		spec.Inner, spec.AggregationQuery, spec.SortSet)
}

func (c *Client) planGlobalParameterizedAggregation(r *http.Request, query, operator string,
	inner promql.Expr, aggregationQuery string, sortSet bool,
) (*merge.TSMMergePlan, error) {
	strategy, warning, weightedAverage := globalInnerMergeStrategy(operator, inner)
	supported := warning == ""

	fanoutQuery := inner.String()
	rewritten := true
	finalizer := merge.TSMFinalizerSpec{Enabled: true, Query: query}
	if weightedAverage {
		return weightedAveragePlan(r, query, inner, finalizer, true)
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

func shardInputWarning(operator string, input promql.Expr) string {
	warningPrefix := "trickster: " + operator + " "
	if input.ContainsAggregation() {
		return warningPrefix + "contains a nested aggregation that cannot be " +
			"correctly merged across fanout backends; results may be inaccurate"
	}
	if input.ContainsBinaryExpression() {
		return warningPrefix + "contains a binary expression that may require " +
			"cross-shard matching; results may be inaccurate"
	}
	if globalFunction, found := input.NonShardLocalFunction(); found {
		return warningPrefix + "contains function " + strconv.Quote(globalFunction) +
			" that may require globally complete input; results may be inaccurate"
	}
	return ""
}

func globalInnerMergeStrategy(operator string, inner promql.Expr) (int, string, bool) {
	strategy := int(merge.StrategyDedup)
	innerAggregation, aggregationInput, found := promql.CompleteOuterAggregation(inner)
	if !found {
		return strategy, shardInputWarning(operator, inner), false
	}
	if warning := shardInputWarning(operator, aggregationInput); warning != "" {
		return strategy, warning, false
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
		return strategy, "trickster: " + operator + " inner aggregator " + strconv.Quote(innerAggregation) +
			" cannot be correctly merged across fanout backends; results may be inaccurate", false
	}
}

func (c *Client) planVariance(r *http.Request, query string,
	spec promql.VarianceAggregation,
) (*merge.TSMMergePlan, bool, error) {
	finalizer := merge.TSMFinalizerSpec{Enabled: true, Query: query}
	innerAggregation, aggregationInput, found := promql.CompleteOuterAggregation(spec.Inner)
	if !found {
		if shardInputWarning(spec.Operator, spec.Inner) != "" {
			return nil, false, nil
		}
		plan, err := pooledVariancePlan(r, query, spec)
		return plan, true, err
	}
	if shardInputWarning(spec.Operator, aggregationInput) != "" {
		return nil, false, nil
	}

	strategy := int(merge.StrategyDedup)
	switch innerAggregation {
	case aggregation.Sum, aggregation.Count, aggregation.CountValues:
		strategy = int(merge.StrategySum)
	case aggregation.Average:
		plan, err := weightedAveragePlan(r, query, spec.Inner, finalizer, true)
		return plan, true, err
	case aggregation.Minimum:
		strategy = int(merge.StrategyMin)
	case aggregation.Maximum:
		strategy = int(merge.StrategyMax)
	case aggregation.Group:
	default:
		return nil, false, nil
	}

	variantRequest, err := rewritePromQueryParam(r, spec.Inner.String())
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

	if agg, aggregationInput, found := promql.CompleteOuterAggregation(spec.Inner); found {
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
			unsupportedWarning = "trickster: limit_ratio inner aggregator " + strconv.Quote(agg) +
				" cannot be correctly merged across fanout backends; results may be inaccurate"
		}
		if unsupportedWarning == "" {
			unsupportedWarning = shardInputWarning(aggregation.LimitRatio, aggregationInput)
			switch {
			case unsupportedWarning != "":
			case weightedAverage:
				return weightedAveragePlan(r, query, spec.Inner,
					merge.TSMFinalizerSpec{Enabled: true, Query: query}, true)
			default:
				strategy = candidateStrategy
				fanoutQuery = spec.Inner.String()
				rewritten = true
				finalizer = merge.TSMFinalizerSpec{Enabled: true, Query: query}
			}
		}
	} else {
		unsupportedWarning = shardInputWarning(aggregation.LimitRatio, spec.Inner)
	}
	if spec.SortSet {
		// Global ordering already requires a finalizer. Always merge the
		// unsampled inner vectors so finalization applies the ratio exactly once,
		// including when the inner expression retains an inaccuracy warning.
		fanoutQuery = spec.Inner.String()
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

func weightedAveragePlan(r *http.Request, originalQuery string, fanout promql.Expr,
	finalizer merge.TSMFinalizerSpec, stripInjectedLabels bool,
) (*merge.TSMMergePlan, error) {
	sumQuery := promql.ReplaceOuterAggregator(fanout, aggregation.Average, aggregation.Sum)
	countQuery := promql.ReplaceOuterAggregator(fanout, aggregation.Average, aggregation.Count)
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

func tsmInnerQuery(expr promql.Expr) (promql.Expr, bool) {
	if spec, ok := promql.ParseRankAggregation(expr); ok {
		return spec.Inner, true
	}
	if spec, ok := promql.ParseSortWrapper(expr); ok {
		if _, _, found := promql.CompleteOuterAggregation(spec.Inner); found ||
			zeroFallbackMergesBySum(spec.Inner) {
			return spec.Inner, true
		}
	}
	return expr, false
}

func tsmFinalizer(query string, expr promql.Expr) merge.TSMFinalizerSpec {
	if _, ok := promql.ParseRankAggregation(expr); ok {
		return merge.TSMFinalizerSpec{Enabled: true, Query: query}
	}
	if _, ok := promql.ParseSortWrapper(expr); ok {
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
