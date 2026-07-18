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

package tsm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fanout"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/encoding"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"golang.org/x/sync/errgroup"
)

func defaultTSMMergePlan(r *http.Request, query string) *tsmerge.TSMMergePlan {
	return &tsmerge.TSMMergePlan{
		OriginalQuery: query,
		Variants: []tsmerge.TSMQueryVariant{{
			Name:              tsmerge.TSMVariantPrimary,
			Request:           r,
			MergeStrategy:     int(tsmerge.StrategyDedup),
			ResponseAuthority: true,
		}},
		Reduction: tsmerge.TSMReductionSpec{
			Kind:          tsmerge.TSMReductionStandard,
			InputVariants: tsmerge.TSMReductionPrimaryVariant(),
		},
		Completeness:            tsmerge.TSMCompletenessResponseAuthority,
		AllowSingleMemberBypass: true,
	}
}

func planNeedsLabelStripping(plan *tsmerge.TSMMergePlan) bool {
	if plan == nil || len(plan.Variants) > 1 {
		return plan != nil
	}
	if plan.StripInjectedLabels {
		return true
	}
	for _, variant := range plan.Variants {
		if tsmerge.Strategy(variant.MergeStrategy) != tsmerge.StrategyDedup {
			return true
		}
	}
	return false
}

func (h *handler) servePlan(
	w http.ResponseWriter,
	r *http.Request,
	hl pool.Targets,
	rsc *request.Resources,
	plan *tsmerge.TSMMergePlan,
	stripKeys []string,
	finalizer mergeFinalizer,
	warnMsg string,
) {
	if plan.Reduction.Kind == tsmerge.TSMReductionStandard {
		variant := plan.Variants[0]
		if !plan.Finalizer.Enabled {
			finalizer = nil
		}
		h.serveStandard(w, variant.Request, hl, rsc,
			tsmerge.Strategy(variant.MergeStrategy), stripKeys,
			plan.Finalizer.Query, finalizer, warnMsg)
		return
	}
	h.serveMultiVariantPlan(w, r, hl, rsc, plan, stripKeys, finalizer, warnMsg)
}

type planVariantExecution struct {
	results       []gatherResult
	contributions []*gatherContribution
	fanoutResults []fanout.Result
}

// serveMultiVariantPlan applies one shared concurrency bound across variants.
// MaxFanoutCaptureBytes remains independent per variant, matching the previous
// weighted-average behavior; MaxCaptureBytes still applies to every response.
func (h *handler) serveMultiVariantPlan(
	w http.ResponseWriter,
	r *http.Request,
	hl pool.Targets,
	rsc *request.Resources,
	plan *tsmerge.TSMMergePlan,
	stripKeys []string,
	finalizer mergeFinalizer,
	warnMsg string,
) {
	parentCtx := r.Context()
	memberCount := len(hl)
	executions := make([]planVariantExecution, len(plan.Variants))
	limiter := fanout.NewConcurrencyLimiter(
		h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit(),
	)
	dedupToleranceNanos := h.dedupToleranceNanos()

	var eg errgroup.Group
	for variantIndex := range plan.Variants {
		variant := plan.Variants[variantIndex]
		executions[variantIndex] = planVariantExecution{
			results:       make([]gatherResult, memberCount),
			contributions: make([]*gatherContribution, memberCount),
		}

		primed, err := fanout.PrimeBody(variant.Request)
		if err != nil {
			logger.Warn("tsm plan variant body preparation failure", logging.Pairs{
				"variant": variant.Name, "error": err,
			})
			failures.HandleBadGateway(w, r)
			return
		}
		plan.Variants[variantIndex].Request = primed

		eg.Go(func() error {
			execution := &executions[variantIndex]
			fanoutResults, err := fanout.All(parentCtx, primed, hl, fanout.Config{
				Mechanism:             names.MechanismTSM,
				Variant:               variant.Name,
				ConcurrencyLimiter:    limiter,
				MaxCaptureBytes:       h.maxCaptureBytes,
				MaxFanoutCaptureBytes: h.maxFanoutCaptureBytes,
				Resources: func(int) *request.Resources {
					return &request.Resources{
						IsMergeMember:         true,
						TSReqestOptions:       rsc.TSReqestOptions,
						TSMergeStrategy:       variant.MergeStrategy,
						TSDedupToleranceNanos: dedupToleranceNanos,
					}
				},
				OnResult: func(member int, fr *fanout.Result) {
					h.collectPlanResult(parentCtx, execution, member, fr, stripKeys, variant.Name)
				},
			})
			execution.fanoutResults = fanoutResults
			return err
		})
	}
	if err := eg.Wait(); err != nil && parentCtx.Err() == nil {
		logger.Warn("tsm plan gather failure", logging.Pairs{"error": err})
	}
	if parentCtx.Err() != nil {
		return
	}

	authorityIndex, _ := plan.ResponseAuthority()
	if allFanoutFailed(executions[authorityIndex].fanoutResults) {
		failures.HandleBadGateway(w, r)
		return
	}

	var responseSeed *dataset.DataSet
	if !hasCompletePlanMember(plan, executions, memberCount) {
		responseSeed = emptyResponseSeed(executions[authorityIndex].contributions)
		if responseSeed == nil {
			for variantIndex := range executions {
				if variantIndex == authorityIndex {
					continue
				}
				if responseSeed = emptyResponseSeed(executions[variantIndex].contributions); responseSeed != nil {
					break
				}
			}
		}
	}
	warnings, hasPlanFailure := applyPlanCompleteness(plan, executions, memberCount)
	accumulators := make([]*merge.Accumulator, len(plan.Variants))
	for variantIndex := range plan.Variants {
		accumulators[variantIndex] = merge.NewAccumulator()
		failedMembers := mergeGatherContributions(parentCtx, accumulators[variantIndex],
			executions[variantIndex].contributions)
		if len(failedMembers) > 0 {
			for _, member := range failedMembers {
				metrics.ALBFanoutFailures.WithLabelValues(
					names.MechanismTSM, plan.Variants[variantIndex].Name, "merge",
				).Inc()
				logger.Warn("tsm plan contribution merge failure", logging.Pairs{
					"variant": plan.Variants[variantIndex].Name,
					"member":  member,
				})
			}
			// Re-running a partially mutated generic accumulator is unsafe. Fail
			// closed instead of reducing variants built from different members.
			failures.HandleBadGateway(w, r)
			return
		}
		if parentCtx.Err() != nil {
			return
		}
	}

	responseAccumulator, err := reducePlan(parentCtx, plan, accumulators)
	if err != nil {
		logger.Warn("tsm plan reduction failure", logging.Pairs{"error": err})
		failures.HandleBadGateway(w, r)
		return
	}
	if responseAccumulator.GetTSData() == nil {
		if responseSeed == nil {
			responseSeed = &dataset.DataSet{}
		}
		responseAccumulator.SetTSData(responseSeed)
	}
	if warnMsg != "" {
		warnings = append(warnings, warnMsg)
	}
	appendPlanWarnings(responseAccumulator, warnings)
	if parentCtx.Err() != nil {
		return
	}
	if plan.Finalizer.Enabled {
		finalizer.FinalizeTSMMerge(plan.Finalizer.Query, responseAccumulator.GetTSData())
	}
	if parentCtx.Err() != nil {
		return
	}

	authorityResults := executions[authorityIndex].results
	mrf, winnerHeaders := pickWinner(authorityResults)
	statusCode, statusHeader, has2xx, hasNon2xx := aggregateStatus(authorityResults)
	if (has2xx && hasNon2xx) || (hasPlanFailure && has2xx) {
		statusHeader = headers.MergeResultHeaderVals(statusHeader, "engine=ALB; status=phit")
	}
	if mrf == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	mergeMultiValuedHeaders(w.Header(), authorityResults, winnerHeaders)
	if winnerHeaders != nil {
		headers.Merge(w.Header(), winnerHeaders)
	}
	if statusHeader != "" {
		w.Header().Set(headers.NameTricksterResult, statusHeader)
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	mrf(w, r, responseAccumulator, statusCode)
}

func (h *handler) collectPlanResult(
	ctx context.Context,
	execution *planVariantExecution,
	member int,
	fr *fanout.Result,
	stripKeys []string,
	variantName string,
) {
	if ctx.Err() != nil || fr == nil || fr.Failed || fr.Request == nil || fr.Capture == nil {
		execution.results[member].failed = true
		return
	}
	rsc := request.GetResources(fr.Request)
	if rsc == nil {
		execution.results[member].failed = true
		return
	}

	result := gatherResult{
		statusCode: fr.Capture.StatusCode(),
		header:     fr.Capture.Header(),
		mergeFunc:  rsc.MergeRespondFunc,
	}
	if rsc.Response != nil && rsc.Response.StatusCode > 0 {
		result.statusCode = rsc.Response.StatusCode
	}
	if rsc.MergeFunc == nil || rsc.MergeRespondFunc == nil {
		logger.Warn("tsm plan gather failed due to nil func", logging.Pairs{
			"variant": variantName, "member": member,
		})
		result.failed = true
		execution.results[member] = result
		return
	}

	var contribution *gatherContribution
	if rsc.TS != nil {
		contribution = prepareGatherContribution(ctx, rsc, nil, member, stripKeys)
	} else {
		body, err := encoding.DecompressResponseBody(
			fr.Capture.Header().Get(headers.NameContentEncoding),
			fr.Capture.Body(),
		)
		if err != nil {
			logger.Warn("tsm plan gather decode failure", logging.Pairs{
				"variant": variantName, "member": member, "error": err,
			})
			result.failed = true
			execution.results[member] = result
			return
		}
		if len(body) > 0 {
			contribution = prepareGatherContribution(ctx, rsc, body, member, stripKeys)
		}
	}
	result.contrib = contribution
	result.failed = contribution == nil || result.statusCode < http.StatusOK ||
		result.statusCode >= http.StatusMultipleChoices
	execution.contributions[member] = contribution
	execution.results[member] = result
}

func applyPlanCompleteness(
	plan *tsmerge.TSMMergePlan,
	executions []planVariantExecution,
	memberCount int,
) ([]string, bool) {
	var warnings []string
	var hasFailure bool
	for member := range memberCount {
		missing := make([]int, 0, len(plan.Variants))
		for variantIndex := range plan.Variants {
			result := executions[variantIndex].results[member]
			if result.failed || executions[variantIndex].contributions[member] == nil {
				missing = append(missing, variantIndex)
				metrics.ALBFanoutFailures.WithLabelValues(
					names.MechanismTSM, plan.Variants[variantIndex].Name, "no_contribution",
				).Inc()
			}
		}
		if len(missing) == 0 {
			continue
		}
		hasFailure = true
		for _, variantIndex := range missing {
			warnings = append(warnings,
				"trickster: tsm excluded pool member "+strconv.Itoa(member)+
					": variant \""+plan.Variants[variantIndex].Name+"\" returned no usable response")
		}
		if plan.Completeness == tsmerge.TSMCompletenessAllVariants {
			for variantIndex := range executions {
				executions[variantIndex].contributions[member] = nil
			}
		}
	}
	return warnings, hasFailure
}

func hasCompletePlanMember(
	plan *tsmerge.TSMMergePlan,
	executions []planVariantExecution,
	memberCount int,
) bool {
	authority, _ := plan.ResponseAuthority()
	for member := range memberCount {
		if plan.Completeness == tsmerge.TSMCompletenessResponseAuthority {
			if !executions[authority].results[member].failed &&
				executions[authority].contributions[member] != nil {
				return true
			}
			continue
		}
		complete := true
		for variantIndex := range executions {
			if executions[variantIndex].results[member].failed ||
				executions[variantIndex].contributions[member] == nil {
				complete = false
				break
			}
		}
		if complete {
			return true
		}
	}
	return false
}

func emptyResponseSeed(contributions []*gatherContribution) *dataset.DataSet {
	for _, contribution := range contributions {
		if contribution == nil {
			continue
		}
		ds, ok := contribution.data.(*dataset.DataSet)
		if !ok || ds == nil {
			continue
		}
		clone, ok := ds.Clone().(*dataset.DataSet)
		if !ok {
			return nil
		}
		clone.ExtentList = nil
		clone.VolatileExtentList = nil
		for _, result := range clone.Results {
			if result != nil {
				result.SeriesList = result.SeriesList[:0]
			}
		}
		return clone
	}
	return nil
}

func reducePlan(
	ctx context.Context,
	plan *tsmerge.TSMMergePlan,
	accumulators []*merge.Accumulator,
) (*merge.Accumulator, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	indexes := make(map[string]int, len(plan.Variants))
	for i, variant := range plan.Variants {
		indexes[variant.Name] = i
	}

	switch plan.Reduction.Kind {
	case tsmerge.TSMReductionStandard:
		return accumulators[indexes[plan.Reduction.InputVariants[0]]], nil
	case tsmerge.TSMReductionWeightedAverage:
		sumAccumulator := accumulators[indexes[plan.Reduction.InputVariants[0]]]
		countAccumulator := accumulators[indexes[plan.Reduction.InputVariants[1]]]
		sumTS, countTS := sumAccumulator.GetTSData(), countAccumulator.GetTSData()
		if sumTS == nil && countTS == nil {
			return sumAccumulator, nil
		}
		sumDS, sumOK := sumTS.(*dataset.DataSet)
		countDS, countOK := countTS.(*dataset.DataSet)
		if !sumOK || !countOK || sumDS == nil || countDS == nil {
			return nil, errors.New("weighted-average reduction requires paired datasets")
		}
		pruneUnpairedWeightedAvgSeries(sumDS, countDS, plan.OriginalQuery)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		sumDS.FinalizeWeightedAvg(countDS, plan.OriginalQuery)
		return sumAccumulator, nil
	default:
		return nil, fmt.Errorf("unsupported tsm reduction kind %d", plan.Reduction.Kind)
	}
}

func appendPlanWarnings(accumulator *merge.Accumulator, warnings []string) {
	if accumulator == nil || len(warnings) == 0 {
		return
	}
	if ds, ok := accumulator.GetTSData().(*dataset.DataSet); ok && ds != nil {
		ds.UpdateLock.Lock()
		ds.Warnings = append(ds.Warnings, warnings...)
		ds.UpdateLock.Unlock()
	}
}
