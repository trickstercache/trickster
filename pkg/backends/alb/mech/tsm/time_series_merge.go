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
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/rr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/encoding"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/capture"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"golang.org/x/sync/errgroup"
)

const (
	ID        types.ID   = 4
	ShortName            = names.MechanismTSM
	Name      types.Name = "time_series_merge"
)

type handler struct {
	pool            pool.Pool
	mergePaths      []string        // paths handled by the alb client that are enabled for tsmerge
	nonmergeHandler types.Mechanism // when methodology is tsmerge, this handler is for non-mergeable paths
	outputFormat    string          // the provider output format (e.g., "prometheus")
	tsmOptions      options.TimeSeriesMergeOptions
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{ID: ID, Name: Name, ShortName: ShortName, New: New}
}

func New(o *options.Options, factories rt.Lookup) (types.Mechanism, error) {
	nmh, _ := rr.New(nil, nil)
	out := &handler{
		nonmergeHandler: nmh,
		tsmOptions:      o.TSMOptions,
	}
	// this validates the merge configuration for the ALB client as it sets it up
	// First, verify the output format is a support merge provider
	if !providers.IsSupportedTimeSeriesMergeProvider(o.OutputFormat) {
		return nil, errors.ErrInvalidTimeSeriesMergeProvider
	}

	// next, get the factory function required to create a backend handler for the supplied format
	f, ok := factories[o.OutputFormat]
	if !ok {
		return nil, errors.ErrInvalidTimeSeriesMergeProvider
	}
	// now, create a handler for the merge provider based on the supplied factory function
	mc1, err := f(providers.ALB, nil, nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	// convert the new time series handler to a mergeable timeseries handler to get the merge paths
	mc2, ok := mc1.(backends.MergeableTimeseriesBackend)
	if !ok {
		return nil, errors.ErrInvalidTimeSeriesMergeProvider
	}
	// set the merge paths in the ALB client
	out.mergePaths = mc2.MergeablePaths()
	out.outputFormat = o.OutputFormat
	return out, nil
}

func (h *handler) ID() types.ID {
	return ID
}

func (h *handler) Name() types.Name {
	return ShortName
}

func (h *handler) SetPool(p pool.Pool) {
	h.pool = p
	h.nonmergeHandler.SetPool(p)
}

func (h *handler) StopPool() {
	if h.pool != nil {
		h.pool.Stop()
	}
}

func (h *handler) Pool() pool.Pool {
	return h.pool
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.pool == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	hl := h.pool.HealthyTargets() // should return a fanout list
	l := len(hl)
	if l == 0 {
		failures.HandleBadGateway(w, r)
		return
	}
	defaultHandler := hl[0].Handler()
	var isMergeablePath bool
	for _, v := range h.mergePaths {
		if strings.HasPrefix(r.URL.Path, v) {
			isMergeablePath = true
			break
		}
	}
	if !isMergeablePath {
		defaultHandler.ServeHTTP(w, r)
		return
	}
	// just proxy 1:1 if no folds in the fan or if there
	// are no merge functions attached to the request
	rsc := request.GetResources(r)
	if rsc == nil || l == 1 {
		defaultHandler.ServeHTTP(w, r)
		return
	}

	// Determine the correct merge strategy for this query. We ask the first
	// healthy pool backend to parse the request via its ParseTimeRangeQuery
	// method. If rsc already carries a parsed TimeRangeQuery (set by upstream
	// middleware), we use its Statement directly.
	var query string
	switch {
	case rsc.TimeRangeQuery != nil:
		query = rsc.TimeRangeQuery.Statement
	case len(hl) > 0 && hl[0] != nil:
		if b := hl[0].Backend(); b != nil {
			if tsb, ok := b.(backends.TimeseriesBackend); ok {
				if trq, _, _, err := tsb.ParseTimeRangeQuery(r); err == nil && trq != nil {
					query = trq.Statement
				}
			}
		}
	}
	var mergeStrategy dataset.MergeStrategy
	var needsDualQuery bool
	var warnMsg string
	var mp backends.TSMMergeProvider
	if len(hl) > 0 && hl[0] != nil {
		if b := hl[0].Backend(); b != nil {
			if p, ok := b.(backends.TSMMergeProvider); ok {
				mp = p
				strategyInt, dq, w := mp.ClassifyMerge(query)
				mergeStrategy, needsDualQuery, warnMsg = dataset.MergeStrategy(strategyInt), dq, w
			}
			// backends that don't implement TSMMergeProvider default to dedup.
		}
	}

	// Collect injected label keys from pool backends so they can be stripped
	// before merging. This ensures series from different backends hash
	// identically despite having different injected labels (e.g., region tags).
	// Stripping is only needed when a non-dedup strategy is in play.
	var stripKeys []string
	if mergeStrategy != dataset.MergeStrategyDedup || needsDualQuery {
		seen := make(map[string]struct{})
		for _, t := range hl {
			if t == nil {
				continue
			}
			b := t.Backend()
			if b == nil {
				continue
			}
			cfg := b.Configuration()
			if cfg != nil && cfg.Prometheus != nil {
				for k := range cfg.Prometheus.Labels {
					if _, ok := seen[k]; !ok {
						seen[k] = struct{}{}
						stripKeys = append(stripKeys, k)
					}
				}
			}
		}
	}

	if needsDualQuery {
		h.serveWeightedAvg(w, r, hl, rsc, mp, query, stripKeys)
		return
	}

	// Standard scatter/gather for all non-avg strategies.
	h.serveStandard(w, r, hl, rsc, mergeStrategy, stripKeys, warnMsg)
}

// serveStandard handles the common scatter/gather path: each shard gets one
// request, results are merged with mergeStrategy, and an optional warning is
// appended to the accumulated dataset before writing the response.
func (h *handler) serveStandard(
	w http.ResponseWriter, r *http.Request,
	hl pool.Targets, rsc *request.Resources,
	mergeStrategy dataset.MergeStrategy,
	stripKeys []string,
	warnMsg string,
) {
	l := len(hl)
	var mrf merge.RespondFunc

	accumulator := merge.NewAccumulator()
	var eg errgroup.Group
	if limit := h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit(); limit > 0 {
		eg.SetLimit(limit)
	}

	type result struct {
		statusCode int
		header     http.Header
		mergeFunc  merge.RespondFunc
	}
	results := make([]result, l)

	for i := range l {
		if hl[i] == nil {
			continue
		}
		eg.Go(func() error {
			r2, _ := request.CloneWithoutResources(r)
			rsc2 := &request.Resources{
				IsMergeMember:   true,
				TSReqestOptions: rsc.TSReqestOptions,
				TSMergeStrategy: int(mergeStrategy),
			}
			r2 = request.SetResources(r2, rsc2)
			crw := capture.NewCaptureResponseWriter()
			hl[i].Handler().ServeHTTP(crw, r2)
			rsc2 = request.GetResources(r2)
			if rsc2 == nil {
				return stderrors.New("tsm gather failed due to nil resources")
			}
			// ensure merge functions are set on cloned request
			if rsc2.MergeFunc == nil || rsc2.MergeRespondFunc == nil {
				logger.Warn("tsm gather failed due to nil func", nil)
			}
			// strip injected labels so series from different backends hash
			// identically for aggregation
			if len(stripKeys) > 0 && rsc2.TS != nil {
				if ds, ok := rsc2.TS.(*dataset.DataSet); ok {
					ds.StripTags(stripKeys)
				}
			}
			// as soon as response is complete, unmarshal and merge
			// this happens in parallel for each response as it arrives
			if rsc2.MergeFunc != nil {
				if rsc2.TS != nil {
					rsc2.MergeFunc(accumulator, rsc2.TS, i)
				} else {
					body, err := encoding.DecompressResponseBody(
						crw.Header().Get(headers.NameContentEncoding),
						crw.Body(),
					)
					if err != nil {
						return err
					}
					if len(body) > 0 {
						// For non-timeseries paths (labels, series, etc.), rsc.TS is not
						// populated. Fall back to passing the captured response body to
						// MergeFunc, which handles []byte input via JSON unmarshal.
						rsc2.MergeFunc(accumulator, body, i)
					}
				}
			}
			results[i] = result{
				statusCode: crw.StatusCode(),
				header:     crw.Header(),
				mergeFunc:  rsc2.MergeRespondFunc,
			}
			return nil
		})
	}

	// wait for all fanout requests to complete
	if err := eg.Wait(); err != nil {
		logger.Warn("tsm gather failure", logging.Pairs{"error": err})
	}

	// For non-supportable aggregators, inject a warning into the Prometheus
	// response so clients know the merged results may be inaccurate.
	if warnMsg != "" {
		if ts := accumulator.GetTSData(); ts != nil {
			if ds, ok := ts.(*dataset.DataSet); ok {
				ds.Warnings = append(ds.Warnings, warnMsg)
			}
		}
	}

	var statusCode int
	var statusHeader string
	for _, res := range results {
		if mrf == nil {
			mrf = res.mergeFunc
		}
		if res.statusCode > 0 {
			if statusCode == 0 || res.statusCode < statusCode {
				statusCode = res.statusCode
			}
		}
		if res.header != nil {
			headers.StripMergeHeaders(res.header)
			statusHeader = headers.MergeResultHeaderVals(statusHeader,
				res.header.Get(headers.NameTricksterResult))
		}
	}

	// set aggregated status header
	if statusHeader != "" {
		w.Header().Set(headers.NameTricksterResult, statusHeader)
	}

	// marshal and write the merged series to the client
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if mrf != nil {
		mrf(w, r, accumulator, statusCode)
	}
}

// serveWeightedAvg implements the dual-query scatter/gather for outer avg
// aggregators. For each shard it fires two concurrent requests:
//   - a "sum" variant (avg → sum) to accumulate the total sum per series
//   - a "count" variant (avg → count) to accumulate the total count per series
//
// After all shards respond, FinalizeWeightedAvg divides sum by count to
// produce a true weighted arithmetic mean, avoiding the skew that
// avg-of-averages produces when shards have different cardinalities.
// The original query string is passed for pairing so sum/count rewrites
// align with the same logical statement (see dataset.FinalizeWeightedAvg).
func (h *handler) serveWeightedAvg(
	w http.ResponseWriter, r *http.Request,
	hl pool.Targets, rsc *request.Resources,
	mp backends.TSMMergeProvider, query string, stripKeys []string,
) {
	l := len(hl)

	// Rewrite the request once; the provider encapsulates both the query
	// expression substitution and the wire-protocol injection (URL param,
	// POST body, etc.).
	sumBase, countBase := mp.RewriteForWeightedAvg(r, query)

	sumAccum := merge.NewAccumulator()
	countAccum := merge.NewAccumulator()

	var eg errgroup.Group
	if limit := h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit(); limit > 0 {
		// Each shard spawns two goroutines; scale the limit accordingly so we
		// don't serialize unnecessarily.
		eg.SetLimit(limit * 2)
	}

	type result struct {
		statusCode int
		header     http.Header
		mergeFunc  merge.RespondFunc // from the sum query (used to write the final response)
	}
	results := make([]result, l)

	for i := range l {
		if hl[i] == nil {
			continue
		}
		// Sum query for shard i — clone from the pre-rewritten base request.
		eg.Go(func() error {
			r2, _ := request.CloneWithoutResources(sumBase)
			rsc2 := &request.Resources{
				IsMergeMember:   true,
				TSReqestOptions: rsc.TSReqestOptions,
				TSMergeStrategy: int(dataset.MergeStrategySum),
			}
			r2 = request.SetResources(r2, rsc2)
			crw := capture.NewCaptureResponseWriter()
			hl[i].Handler().ServeHTTP(crw, r2)
			rsc2 = request.GetResources(r2)
			if rsc2 == nil {
				return stderrors.New("tsm avg/sum gather failed due to nil resources")
			}
			if len(stripKeys) > 0 && rsc2.TS != nil {
				if ds, ok := rsc2.TS.(*dataset.DataSet); ok {
					ds.StripTags(stripKeys)
				}
			}
			if rsc2.MergeFunc != nil && rsc2.TS != nil {
				rsc2.MergeFunc(sumAccum, rsc2.TS, i)
			}
			results[i] = result{
				statusCode: crw.StatusCode(),
				header:     crw.Header(),
				mergeFunc:  rsc2.MergeRespondFunc,
			}
			return nil
		})

		// Count query for shard i — clone from the pre-rewritten base request.
		eg.Go(func() error {
			r2, _ := request.CloneWithoutResources(countBase)
			rsc2 := &request.Resources{
				IsMergeMember:   true,
				TSReqestOptions: rsc.TSReqestOptions,
				TSMergeStrategy: int(dataset.MergeStrategySum),
			}
			r2 = request.SetResources(r2, rsc2)
			crw := capture.NewCaptureResponseWriter()
			hl[i].Handler().ServeHTTP(crw, r2)
			rsc2 = request.GetResources(r2)
			if rsc2 == nil {
				return stderrors.New("tsm avg/count gather failed due to nil resources")
			}
			if len(stripKeys) > 0 && rsc2.TS != nil {
				if ds, ok := rsc2.TS.(*dataset.DataSet); ok {
					ds.StripTags(stripKeys)
				}
			}
			if rsc2.MergeFunc != nil && rsc2.TS != nil {
				rsc2.MergeFunc(countAccum, rsc2.TS, i)
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		logger.Warn("tsm weighted-avg gather failure", logging.Pairs{"error": err})
	}

	// Finalize: divide sum totals by count totals to obtain the weighted average.
	sumTS := sumAccum.GetTSData()
	countTS := countAccum.GetTSData()
	if sumTS != nil && countTS != nil {
		if sumDS, ok := sumTS.(*dataset.DataSet); ok {
			if countDS, ok := countTS.(*dataset.DataSet); ok {
				sumDS.FinalizeWeightedAvg(countDS, query)
			}
		}
	}

	// Aggregate status and headers from sum-query results (count results are
	// only used for the arithmetic and do not affect the response envelope).
	var mrf merge.RespondFunc
	var statusCode int
	var statusHeader string
	for _, res := range results {
		if mrf == nil {
			mrf = res.mergeFunc
		}
		if res.statusCode > 0 {
			if statusCode == 0 || res.statusCode < statusCode {
				statusCode = res.statusCode
			}
		}
		if res.header != nil {
			headers.StripMergeHeaders(res.header)
			statusHeader = headers.MergeResultHeaderVals(statusHeader,
				res.header.Get(headers.NameTricksterResult))
		}
	}

	if statusHeader != "" {
		w.Header().Set(headers.NameTricksterResult, statusHeader)
	}

	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	// Write the finalized sum accumulator (which now contains weighted averages)
	// using the RespondFunc from the sum queries.
	if mrf != nil {
		mrf(w, r, sumAccum, statusCode)
	}
}
