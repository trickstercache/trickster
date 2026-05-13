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
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
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
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
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
	mech.PoolHolder
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

// SetPool overrides PoolHolder.SetPool to also propagate the pool to the
// non-merge handler used for paths that bypass the TSM merge path.
func (h *handler) SetPool(p pool.Pool) {
	h.PoolHolder.SetPool(p)
	if h.nonmergeHandler != nil {
		h.nonmergeHandler.SetPool(p)
	}
}

func (h *handler) StopPool() {
	if p := h.Pool(); p != nil {
		p.Stop()
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := h.Pool()
	if p == nil {
		failures.HandleBadGateway(w, r)
		return
	}
	hl := p.LiveTargets() // should return a fanout list
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
	// just proxy 1:1 if there's no per-request resources object;
	// the single-member shortcut is decided after stripKeys is computed
	// so we don't bypass label-stripping for solo pools (D1).
	rsc := request.GetResources(r)
	if rsc == nil {
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
	// Fallback for instant queries (/api/v1/query): ParseTimeRangeQuery
	// rejects requests without start/end/step, so classify directly off
	// the `query` form parameter. Without this, the merge-strategy
	// classifier sees an empty string on every instant query and always
	// falls back to Dedup — which defeats the per-query strip-injected-
	// labels path for any PromQL aggregation issued as an instant query.
	//
	// Body safety for POST form requests: params.GetRequestValues reads
	// r.Body during form parsing and then replaces it with a fresh
	// bytes.NewReader over the cached []byte (see pkg/proxy/request/body.go
	// GetBody: rsc.RequestBody caches the bytes). Subsequent per-member
	// request.CloneWithoutResources calls go through GetBodyReader which
	// wraps another fresh bytes.NewReader over the same read-only bytes,
	// so concurrent reads from pool-member handlers each have an
	// independent reader position and do not race.
	if query == "" {
		if qp, _, _ := params.GetRequestValues(r); qp != nil {
			query = qp.Get("query")
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

	// Single-member fast path: only safe when there's nothing to strip and
	// the strategy is plain dedup. Otherwise the merge path is still needed
	// to apply StripTags or the dual-query rewrite to the lone backend's
	// response (D1).
	if l == 1 && len(stripKeys) == 0 && mergeStrategy == dataset.MergeStrategyDedup && !needsDualQuery {
		defaultHandler.ServeHTTP(w, r)
		return
	}

	if needsDualQuery {
		h.serveWeightedAvg(w, r, hl, rsc, mp, query, stripKeys)
		return
	}

	// Standard scatter/gather for all non-avg strategies.
	h.serveStandard(w, r, hl, rsc, mergeStrategy, stripKeys, warnMsg)
}

// gatherResult captures the per-member fanout outcome used to assemble the
// merged response (status, headers, and the RespondFunc that knows how to
// marshal the accumulator). failed flags a goroutine-level failure (e.g.
// unsupported Content-Encoding, parse error) where the member produced no
// usable contribution to the accumulator even though no HTTP error code
// reached this layer.
type gatherResult struct {
	statusCode int
	header     http.Header
	mergeFunc  merge.RespondFunc
	failed     bool
}

// pickWinner chooses which member's RespondFunc and headers feed the outbound
// response. A 2xx member is preferred over a non-2xx one so a successful
// member's body wins over an error envelope from an earlier-indexed shard
// (V2). Falls back to the first non-nil entry when no 2xx exists.
func pickWinner(results []gatherResult) (merge.RespondFunc, http.Header) {
	for _, res := range results {
		if res.mergeFunc != nil && res.statusCode >= 200 && res.statusCode < 300 {
			return res.mergeFunc, res.header
		}
	}
	for _, res := range results {
		if res.mergeFunc != nil {
			return res.mergeFunc, res.header
		}
	}
	return nil, nil
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

	accumulator := merge.NewAccumulator()
	var eg errgroup.Group
	if limit := h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit(); limit > 0 {
		eg.SetLimit(limit)
	}

	results := make([]gatherResult, l)

	for i := range l {
		if hl[i] == nil {
			continue
		}
		eg.Go(func() error {
			// recover so a single bad upstream doesn't crash the proxy; mark
			// the slot failed so partial-failure surfacing fires downstream
			defer mech.RecoverFanoutPanic("tsm", i, func() { results[i] = gatherResult{failed: true} })
			r2, err := request.CloneWithoutResources(r)
			if err != nil {
				results[i].failed = true
				return err
			}
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
				results[i].failed = true
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
			var contributed bool
			if rsc2.MergeFunc != nil {
				if rsc2.TS != nil {
					rsc2.MergeFunc(accumulator, rsc2.TS, i)
					contributed = true
				} else {
					body, err := encoding.DecompressResponseBody(
						crw.Header().Get(headers.NameContentEncoding),
						crw.Body(),
					)
					if err != nil {
						results[i].failed = true
						return err
					}
					if len(body) > 0 {
						// For non-timeseries paths (labels, series, etc.), rsc.TS is not
						// populated. Fall back to passing the captured response body to
						// MergeFunc, which handles []byte input via JSON unmarshal.
						rsc2.MergeFunc(accumulator, body, i)
						contributed = true
					}
				}
			}
			// rsc2.Response carries the upstream's true status code (set by
			// ObjectProxyCacheRequest for merge members). The CRW status can
			// stay at the default 200 when the per-shard handler unmarshals
			// an error envelope but writes nothing through the outer writer
			// (see prometheus.processVectorTransformations + MarshalTSOrVectorWriter
			// returning ErrUnknownFormat for zero-result error responses).
			// Without this fallback, mixed 2xx/5xx fanouts look uniformly OK
			// to TSM and the partial-hit detection below misses (V2).
			sc := crw.StatusCode()
			if rsc2.Response != nil && rsc2.Response.StatusCode > 0 {
				sc = rsc2.Response.StatusCode
			}
			results[i] = gatherResult{
				statusCode: sc,
				header:     crw.Header(),
				mergeFunc:  rsc2.MergeRespondFunc,
				// A member that wrote no contribution to the accumulator is
				// a silent failure: typically the per-member handler hit a
				// proxy-error (e.g. unsupported Content-Encoding decoded
				// upstream then unmarshal failed) and surfaced 200 + empty
				// body to us. Without this, the merged response would look
				// identical to a fully-successful fanout.
				failed: !contributed,
			}
			return nil
		})
	}

	// wait for all fanout requests to complete
	if err := eg.Wait(); err != nil {
		logger.Warn("tsm gather failure", logging.Pairs{"error": err})
	}

	// Surface goroutine-level failures (e.g. unsupported Content-Encoding,
	// parse error) where a member produced no contribution but no HTTP
	// status reached this layer. Without this, the merged response would
	// silently look identical to a fully-successful fanout.
	var hasGatherFailure bool
	for i, res := range results {
		if res.failed {
			hasGatherFailure = true
			if ts := accumulator.GetTSData(); ts != nil {
				if ds, ok := ts.(*dataset.DataSet); ok {
					ds.Warnings = append(ds.Warnings,
						"trickster: tsm partial failure: pool member "+strconv.Itoa(i)+" returned no usable response")
				}
			}
		}
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

	// winnerHeaders carries custom response headers (e.g. those set by a
	// pool member's path override via response_headers:) from the same
	// member whose mergeFunc will write the final response. Without this,
	// TSM fanout would strip any backend-set headers that FGR would
	// happily propagate. See #970.
	mrf, winnerHeaders := pickWinner(results)
	var statusCode int
	var statusHeader string
	var has2xx, hasNon2xx bool
	for _, res := range results {
		if res.statusCode > 0 {
			if statusCode == 0 || res.statusCode < statusCode {
				statusCode = res.statusCode
			}
			if res.statusCode >= 200 && res.statusCode < 300 {
				has2xx = true
			} else {
				hasNon2xx = true
			}
		}
		if res.header != nil {
			headers.StripMergeHeaders(res.header)
			statusHeader = headers.MergeResultHeaderVals(statusHeader,
				res.header.Get(headers.NameTricksterResult))
		}
	}
	// Mixed 2xx + non-2xx fanout: surface a partial-hit marker so clients
	// can detect that some members failed even when each member's own
	// Trickster status string happens to agree (V2). hasGatherFailure
	// extends this to silent goroutine failures (e.g. unsupported
	// Content-Encoding) where the member returned 200 but produced no
	// usable contribution to the merged result.
	if (has2xx && hasNon2xx) || (hasGatherFailure && has2xx) {
		statusHeader = headers.MergeResultHeaderVals(statusHeader, "engine=ALB; status=phit")
	}

	// preserve Set-Cookie from all members; headers.Merge below would otherwise collapse to winner only
	mergeMultiValuedHeaders(w.Header(), results, winnerHeaders)

	// Carry the winner's custom headers onto the outbound response BEFORE
	// setting the aggregated X-Trickster-Result. headers.Merge makes the
	// source value authoritative for every key it touches, so the Set
	// below keeps TSM's own merged status header regardless of what the
	// member advertised for that key. Structural headers (Content-Type,
	// Content-Length, Date, Last-Modified, Transfer-Encoding) were already
	// removed by StripMergeHeaders.
	if winnerHeaders != nil {
		headers.Merge(w.Header(), winnerHeaders)
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

// mergeMultiValuedHeaders appends every member's Set-Cookie values onto dst
// and clears Set-Cookie on winnerHeaders so the subsequent headers.Merge
// (which uses Set semantics) doesn't collapse them. RFC 6265 allows multiple
// Set-Cookie response headers; Warning (RFC 7234) is similar but no current
// backend sets it.
func mergeMultiValuedHeaders(dst http.Header, results []gatherResult, winnerHeaders http.Header) {
	for _, res := range results {
		if res.header == nil {
			continue
		}
		for _, v := range res.header.Values(headers.NameSetCookie) {
			dst.Add(headers.NameSetCookie, v)
		}
	}
	if winnerHeaders != nil {
		winnerHeaders.Del(headers.NameSetCookie)
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

	// results captures sum-query outcomes only — the response envelope
	// (status, headers, RespondFunc) comes from the sum side; count results
	// affect the arithmetic, not the envelope.
	results := make([]gatherResult, l)

	for i := range l {
		if hl[i] == nil {
			continue
		}
		// Sum query for shard i — clone from the pre-rewritten base request.
		eg.Go(func() error {
			// recover so a single bad upstream doesn't crash the proxy; mark
			// the slot failed so partial-failure surfacing fires downstream
			defer mech.RecoverFanoutPanic("tsm/avg-sum", i, func() { results[i] = gatherResult{failed: true} })
			r2, err := request.CloneWithoutResources(sumBase)
			if err != nil {
				return err
			}
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
			// See serveStandard for why we prefer rsc2.Response.StatusCode.
			sc := crw.StatusCode()
			if rsc2.Response != nil && rsc2.Response.StatusCode > 0 {
				sc = rsc2.Response.StatusCode
			}
			results[i] = gatherResult{
				statusCode: sc,
				header:     crw.Header(),
				mergeFunc:  rsc2.MergeRespondFunc,
			}
			return nil
		})

		// Count query for shard i — clone from the pre-rewritten base request.
		eg.Go(func() error {
			// recover so a single bad upstream doesn't crash the proxy; the
			// count side doesn't own the response envelope, so we don't touch
			// results[i] here — the sum-side recover handles that
			defer mech.RecoverFanoutPanic("tsm/avg-count", i, nil)
			r2, err := request.CloneWithoutResources(countBase)
			if err != nil {
				return err
			}
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

	// Finalize: divide sum totals by count totals to obtain the weighted
	// average. If either side fanned out to zero usable responses the
	// surviving accumulator is unfinalized and not a true average — surface
	// that to the client via a Prometheus warning rather than returning
	// silently-wrong numbers (D2).
	sumTS := sumAccum.GetTSData()
	countTS := countAccum.GetTSData()
	const (
		warnCountFailed = "trickster: weighted-avg count fanout returned no usable responses; values are unfinalized sums and not a true average"
		warnSumFailed   = "trickster: weighted-avg sum fanout returned no usable responses; values are unfinalized counts and not a true average"
	)
	switch {
	case sumTS != nil && countTS != nil:
		if sumDS, ok := sumTS.(*dataset.DataSet); ok {
			if countDS, ok := countTS.(*dataset.DataSet); ok {
				sumDS.FinalizeWeightedAvg(countDS, query)
			}
		}
	case sumTS != nil && countTS == nil:
		if sumDS, ok := sumTS.(*dataset.DataSet); ok {
			sumDS.Warnings = append(sumDS.Warnings, warnCountFailed)
		}
	case sumTS == nil && countTS != nil:
		// Promote countTS into the response slot so the client sees a body
		// (with a warning) instead of nothing. The sum-side mrf still
		// writes — it just marshals whichever DataSet the accumulator now
		// holds.
		if countDS, ok := countTS.(*dataset.DataSet); ok {
			countDS.Warnings = append(countDS.Warnings, warnSumFailed)
		}
		sumAccum.SetTSData(countTS)
	}

	// Aggregate status and headers from sum-query results (count results are
	// only used for the arithmetic and do not affect the response envelope).
	mrf, winnerHeaders := pickWinner(results)
	var statusCode int
	var statusHeader string
	var has2xx, hasNon2xx bool
	for _, res := range results {
		if res.statusCode > 0 {
			if statusCode == 0 || res.statusCode < statusCode {
				statusCode = res.statusCode
			}
			if res.statusCode >= 200 && res.statusCode < 300 {
				has2xx = true
			} else {
				hasNon2xx = true
			}
		}
		if res.header != nil {
			headers.StripMergeHeaders(res.header)
			statusHeader = headers.MergeResultHeaderVals(statusHeader,
				res.header.Get(headers.NameTricksterResult))
		}
	}
	if has2xx && hasNon2xx {
		statusHeader = headers.MergeResultHeaderVals(statusHeader, "engine=ALB; status=phit")
	}

	// See serveStandard for the rationale -- carry the winner's custom
	// response headers through the fanout so backend-set headers like
	// `X-Test-Origin` survive the merge. (#970)
	mergeMultiValuedHeaders(w.Header(), results, winnerHeaders)
	if winnerHeaders != nil {
		headers.Merge(w.Header(), winnerHeaders)
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
