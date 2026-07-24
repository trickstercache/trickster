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
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fanout"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/encoding"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

const (
	ShortName            = names.MechanismTSM
	Name      types.Name = "time_series_merge"
)

type handler struct {
	mech.PoolHolder
	mergePaths            []string // paths handled by the alb client that are enabled for tsmerge
	outputFormat          string   // the provider output format (e.g., "prometheus")
	tsmOptions            options.TimeSeriesMergeOptions
	maxCaptureBytes       int
	maxFanoutCaptureBytes int

	// poolVersion increments on every SetPool so cached pool-derived data
	// (stripKeys) can be invalidated without locking.
	poolVersion atomic.Uint64
	// degradeActive is true while a configured-multi-member pool is dispatching
	// with only one live member. It throttles the operator WARN to once per
	// healthy->degraded transition instead of once per request.
	degradeActive atomic.Bool
	// cachedStripKeys memoizes the stripKeys slice across requests as long
	// as the pool hasn't been replaced. Hot path is a single atomic load
	// plus a uint64 compare.
	cachedStripKeys atomic.Pointer[stripKeysSnapshot]
}

type mergeFinalizer interface {
	FinalizeTSMMerge(query string, ts timeseries.Timeseries)
}

// stripKeysSnapshot binds a computed stripKeys slice to the poolVersion it
// was derived from. Readers compare version against the current poolVersion;
// a mismatch triggers a rebuild. The seen set lets subsequent calls union new
// label keys in as targets become healthy without bumping poolVersion (a
// target unhealthy on the first compute would otherwise be permanently
// excluded until SetPool fires; see computeStripKeys). Snapshots are
// immutable once stored, so copy-on-write is required to grow them.
type stripKeysSnapshot struct {
	version uint64
	keys    []string
	seen    map[string]struct{}
}

func RegistryEntry() types.RegistryEntry {
	return types.RegistryEntry{Name: Name, ShortName: ShortName, New: New}
}

func New(o *options.Options, factories rt.Lookup) (types.Mechanism, error) {
	out := &handler{
		tsmOptions:            o.TSMOptions,
		maxCaptureBytes:       o.MaxCaptureBytes,
		maxFanoutCaptureBytes: o.MaxFanoutCaptureBytes,
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

// dedupToleranceNanos returns the configured per-shard dedup tolerance as
// nanoseconds. Nil/zero means tolerance is disabled (legacy exact-epoch dedup).
func (h *handler) dedupToleranceNanos() int64 {
	if h.tsmOptions.DedupToleranceMs == nil || *h.tsmOptions.DedupToleranceMs <= 0 {
		return 0
	}
	// Clamp at math.MaxInt64/1e6 to avoid int64 multiply overflow producing a negative window.
	const maxMs = math.MaxInt64 / 1_000_000
	ms := min(*h.tsmOptions.DedupToleranceMs, maxMs)
	return int64(ms) * 1_000_000
}

func (h *handler) Name() types.Name {
	return ShortName
}

// SetPool overrides PoolHolder.SetPool so pool-derived caches can be
// invalidated when the pool is replaced.
func (h *handler) SetPool(p pool.Pool) {
	h.PoolHolder.SetPool(p)
	h.poolVersion.Add(1)
}

// computeStripKeys returns the union of Prometheus injected-label keys across
// pool backends. The cache is keyed by poolVersion (bumped by SetPool); within
// the same poolVersion the cached union grows as new healthy targets are
// observed. This avoids a regression where a target unhealthy at first
// compute, then healthy on a later request, would have its injected labels
// permanently excluded from the cached set until SetPool fired -- causing its
// series to ship with un-stripped backend labels and split during dedup.
// hl is the live-target snapshot for the current request. The pool's full
// configured target list is not reachable through the public Pool interface;
// the union-on-each-call design keeps the cache eventually consistent with
// every target that has been healthy at least once, which is sufficient
// because labels can only need stripping for targets whose responses have
// reached the merge.
//
// Concurrent calls may build divergent snapshots; atomic.Pointer.Store is
// last-writer-wins, but every snapshot is a superset of previously-observed
// keys for the same version, so the missed writer's union is harmless --
// future calls will re-add anything dropped.
func (h *handler) computeStripKeys(hl pool.Targets) []string {
	ver := h.poolVersion.Load()
	snap := h.cachedStripKeys.Load()

	var baseKeys []string
	var baseSeen map[string]struct{}
	if snap != nil && snap.version == ver {
		baseKeys = snap.keys
		baseSeen = snap.seen
	}

	keys, seen := baseKeys, baseSeen
	var grew bool
	for _, t := range hl {
		if t == nil {
			continue
		}
		b := t.Backend()
		if b == nil {
			continue
		}
		cfg := b.Configuration()
		if cfg == nil || cfg.Prometheus == nil {
			continue
		}
		for k := range cfg.Prometheus.Labels {
			if _, ok := seen[k]; ok {
				continue
			}
			if !grew {
				// Copy-on-write: existing snapshot may be observed by other
				// goroutines, so we cannot mutate seen/keys in place.
				seen = make(map[string]struct{}, len(baseSeen)+1)
				for sk := range baseSeen {
					seen[sk] = struct{}{}
				}
				keys = append([]string(nil), baseKeys...)
				grew = true
			}
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}

	if grew || snap == nil || snap.version != ver {
		h.cachedStripKeys.Store(&stripKeysSnapshot{
			version: ver,
			keys:    keys,
			seen:    seen,
		})
	}
	return keys
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
	hl := p.Targets() // should return a fanout list
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
	// just proxy 1:1 if there's no per-request resources object.
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
	case hl[0] != nil:
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
	// falls back to Dedup, which defeats the per-query strip-injected-
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
	plan := defaultTSMMergePlan(r, query)
	var finalizer mergeFinalizer
	if hl[0] != nil {
		if b := hl[0].Backend(); b != nil {
			if planner, ok := b.(backends.TSMMergeProvider); ok {
				var err error
				plan, err = planner.PlanTSMMerge(r, query)
				if err != nil {
					logger.Warn("tsm merge plan construction failure", logging.Pairs{"error": err})
					failures.HandleBadGateway(w, r)
					return
				}
			}
			if f, ok := b.(mergeFinalizer); ok {
				finalizer = f
			}
		}
	}
	if err := plan.Validate(); err != nil {
		logger.Warn("invalid tsm merge plan", logging.Pairs{"error": err})
		failures.HandleBadGateway(w, r)
		return
	}
	if plan.Finalizer.Enabled && finalizer == nil {
		logger.Warn("tsm merge plan requires an unavailable finalizer", nil)
		failures.HandleBadGateway(w, r)
		return
	}
	warnMsg := plan.UnsupportedWarning

	// Collect injected label keys from pool backends so they can be stripped
	// before merging. This ensures series from different backends hash
	// identically despite having different injected labels (e.g., region tags).
	// Stripping is needed for non-dedup strategies and plans that explicitly
	// require routing labels to be removed before logical selection. The set is
	// cached and reused across requests until the pool is replaced.
	var stripKeys []string
	if planNeedsLabelStripping(plan) {
		stripKeys = h.computeStripKeys(hl)
	}

	// A configured multi-member pool serving from a single live member is
	// degraded: the merge has silently collapsed to one shard. Warn once per
	// healthy->degraded transition (not per request) and route through the
	// merge path so the warning reaches the response `warnings` field.
	configured := p.ConfiguredLen()
	degraded := configured > 1 && l == 1
	if degraded {
		if h.degradeActive.CompareAndSwap(false, true) {
			bn := ""
			if rsc.BackendOptions != nil {
				bn = rsc.BackendOptions.Name
			}
			logger.Warn("alb tsm pool degraded to single live member",
				logging.Pairs{"backend_name": bn, "configured": configured, "live": l})
		}
		dw := fmt.Sprintf("trickster: served from 1 of %d pool members; results may be incomplete", configured)
		if warnMsg == "" {
			warnMsg = dw
		} else {
			warnMsg += "; " + dw
		}
	} else {
		h.degradeActive.Store(false)
	}

	// A plan may explicitly allow direct proxying when no planned rewrite,
	// reduction, finalization, warning, or injected-label cleanup is needed.
	if l == 1 && len(stripKeys) == 0 && plan.AllowSingleMemberBypass && !degraded {
		defaultHandler.ServeHTTP(w, r)
		return
	}

	h.servePlan(w, r, hl, rsc, plan, stripKeys, finalizer, warnMsg)
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
	contrib    *gatherContribution
	failed     bool
}

type gatherContribution struct {
	data           any
	mergeFunc      merge.MergeFunc
	batchMergeFunc merge.BatchMergeFunc
	member         int
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

// aggregateStatus collapses per-shard outcomes into the outbound response's
// status code and trickster status header. When any shard returned 2xx, the
// outbound code is the lowest 2xx seen (so 200 wins over 206); otherwise the
// highest non-2xx wins so the more severe failure propagates instead of being
// masked by an incidentally-lower error code.
func allFanoutFailed(results []fanout.Result) bool {
	if len(results) == 0 {
		return false
	}
	for _, r := range results {
		if !r.Failed {
			return false
		}
	}
	return true
}

func aggregateStatus(results []gatherResult) (status int, statusHeader string, has2xx, hasNon2xx bool) {
	var min2xx, maxErr int
	for _, res := range results {
		if res.statusCode > 0 {
			if res.statusCode >= 200 && res.statusCode < 300 {
				has2xx = true
				if min2xx == 0 || res.statusCode < min2xx {
					min2xx = res.statusCode
				}
			} else {
				hasNon2xx = true
				if res.statusCode > maxErr {
					maxErr = res.statusCode
				}
			}
		}
		if res.header != nil {
			headers.StripMergeHeaders(res.header)
			statusHeader = headers.MergeResultHeaderVals(statusHeader,
				res.header.Get(headers.NameTricksterResult))
		}
	}
	if has2xx {
		status = min2xx
	} else {
		status = maxErr
	}
	return
}

func prepareGatherContribution(ctx context.Context, rsc *request.Resources, body []byte, member int,
	stripKeys []string,
) *gatherContribution {
	if ctx.Err() != nil || rsc == nil || rsc.MergeFunc == nil {
		return nil
	}
	ts := rsc.TS
	if ts == nil && len(body) > 0 && rsc.TSUnmarshaler != nil && rsc.TimeRangeQuery != nil {
		var err error
		ts, err = rsc.TSUnmarshaler(body, rsc.TimeRangeQuery)
		if err != nil {
			logger.Warn("tsm gather timeseries decode failure", logging.Pairs{
				"member": member, "error": err,
			})
			return nil
		}
		rsc.TS = ts
	}
	if ctx.Err() != nil {
		return nil
	}
	if len(stripKeys) > 0 && ts != nil {
		if ds, ok := ts.(*dataset.DataSet); ok {
			ds.StripTags(stripKeys)
		}
	}
	var data any
	if ts != nil {
		data = ts
	} else if len(body) > 0 {
		data = body
	} else {
		return nil
	}
	return &gatherContribution{
		data:           data,
		mergeFunc:      rsc.MergeFunc,
		batchMergeFunc: rsc.BatchMergeFunc,
		member:         member,
	}
}

// mergeGatherContributions preserves slot order and lets the backend-provided
// batch function handle compatible inputs. Otherwise each contribution is
// folded through its original MergeFunc.
func mergeGatherContributions(ctx context.Context, accumulator *merge.Accumulator,
	contributions []*gatherContribution,
) []int {
	items := make([]merge.BatchItem, 0, len(contributions))
	batchCompatible := true
	var batchMergeFunc merge.BatchMergeFunc
	for _, contribution := range contributions {
		if ctx.Err() != nil {
			return nil
		}
		if contribution == nil {
			continue
		}
		items = append(items, merge.BatchItem{
			Data:   contribution.data,
			Member: contribution.member,
		})
		if contribution.batchMergeFunc == nil {
			batchCompatible = false
		} else if batchMergeFunc == nil {
			batchMergeFunc = contribution.batchMergeFunc
		}
	}
	if batchCompatible && batchMergeFunc != nil {
		if ctx.Err() != nil {
			return nil
		}
		handled, err := mergeContributionBatch(accumulator, batchMergeFunc, items)
		if err != nil {
			logger.Warn("tsm gather batch merge failure", logging.Pairs{
				"members": len(items), "error": err,
			})
			failed := make([]int, len(items))
			for i, item := range items {
				failed[i] = item.Member
			}
			return failed
		}
		if handled {
			return nil
		}
	}

	failed := make([]int, 0)
	for _, contribution := range contributions {
		if ctx.Err() != nil {
			return failed
		}
		if contribution == nil {
			continue
		}
		if err := mergeContribution(accumulator, contribution); err != nil {
			logger.Warn("tsm gather merge failure", logging.Pairs{
				"member": contribution.member, "error": err,
			})
			failed = append(failed, contribution.member)
		}
	}
	return failed
}

func mergeContributionBatch(accumulator *merge.Accumulator,
	batchMergeFunc merge.BatchMergeFunc, items []merge.BatchItem,
) (handled bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while batch merging contributions: %v", recovered)
		}
	}()
	return batchMergeFunc(accumulator, items)
}

func mergeContribution(accumulator *merge.Accumulator,
	contribution *gatherContribution,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("panic while merging member %d: %v", contribution.member, recovered)
		}
	}()
	return contribution.mergeFunc(accumulator, contribution.data, contribution.member)
}

// serveStandard handles the common scatter/gather path: each shard gets one
// request, results are merged with mergeStrategy, and an optional warning is
// appended to the accumulated dataset before writing the response.
func (h *handler) serveStandard(
	w http.ResponseWriter, r *http.Request,
	hl pool.Targets, rsc *request.Resources,
	mergeStrategy tsmerge.Strategy,
	stripKeys []string,
	query string,
	finalizer mergeFinalizer,
	warnMsg string,
) {
	l := len(hl)

	accumulator := merge.NewAccumulator()
	results := make([]gatherResult, l)

	r, err := fanout.PrimeBody(r)
	if err != nil {
		failures.HandleBadGateway(w, r)
		return
	}
	parentCtx := r.Context()

	dedupToleranceNanos := h.dedupToleranceNanos()
	fanoutResults, _ := fanout.All(parentCtx, r, hl, fanout.Config{
		Mechanism:             names.MechanismTSM,
		ConcurrencyLimit:      h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit(),
		MaxCaptureBytes:       h.maxCaptureBytes,
		MaxFanoutCaptureBytes: h.maxFanoutCaptureBytes,
		Resources: func(int) *request.Resources {
			return &request.Resources{
				IsMergeMember:         true,
				TSReqestOptions:       rsc.TSReqestOptions,
				TSMergeStrategy:       int(mergeStrategy),
				TSDedupToleranceNanos: dedupToleranceNanos,
			}
		},
		OnResult: func(i int, fr *fanout.Result) {
			if parentCtx.Err() != nil {
				results[i].failed = true
				return
			}
			if fr.Failed || fr.Request == nil || fr.Capture == nil {
				results[i].failed = true
				return
			}
			rsc2 := request.GetResources(fr.Request)
			if rsc2 == nil {
				results[i].failed = true
				return
			}
			if rsc2.MergeFunc == nil || rsc2.MergeRespondFunc == nil {
				logger.Warn("tsm gather failed due to nil func", nil)
			}
			var contribution *gatherContribution
			if rsc2.MergeFunc != nil {
				if rsc2.TS != nil {
					contribution = prepareGatherContribution(parentCtx, rsc2, nil, i, stripKeys)
				} else {
					body, derr := encoding.DecompressResponseBody(
						fr.Capture.Header().Get(headers.NameContentEncoding),
						fr.Capture.Body(),
					)
					if derr != nil {
						logger.Warn("tsm gather decode failure", logging.Pairs{
							"member": i, "error": derr,
						})
						results[i].failed = true
						return
					}
					if len(body) > 0 {
						contribution = prepareGatherContribution(parentCtx, rsc2, body, i, stripKeys)
					}
				}
			}
			sc := fr.Capture.StatusCode()
			if rsc2.Response != nil && rsc2.Response.StatusCode > 0 {
				sc = rsc2.Response.StatusCode
			}
			results[i] = gatherResult{
				statusCode: sc,
				header:     fr.Capture.Header(),
				mergeFunc:  rsc2.MergeRespondFunc,
				contrib:    contribution,
				failed:     contribution == nil,
			}
		},
	})
	if parentCtx.Err() != nil {
		return
	}

	for i, fr := range fanoutResults {
		if fr.Failed && !results[i].failed {
			results[i].failed = true
		}
	}
	contributions := make([]*gatherContribution, l)
	for i := range results {
		contributions[i] = results[i].contrib
	}
	for _, member := range mergeGatherContributions(parentCtx, accumulator, contributions) {
		results[member].failed = true
	}
	if parentCtx.Err() != nil {
		return
	}

	// Surface goroutine-level failures (e.g. unsupported Content-Encoding,
	// parse error) where a member produced no contribution but no HTTP
	// status reached this layer. Without this, the merged response would
	// silently look identical to a fully-successful fanout.
	var hasGatherFailure bool
	for i, res := range results {
		if res.failed {
			hasGatherFailure = true
			metrics.ALBFanoutFailures.WithLabelValues(names.MechanismTSM, "", "no_contribution").Inc()
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

	if parentCtx.Err() != nil {
		return
	}
	if finalizer != nil {
		finalizer.FinalizeTSMMerge(query, accumulator.GetTSData())
	}
	if parentCtx.Err() != nil {
		return
	}

	// If every fanout slot failed at the dispatch level (panics, transport
	// errors, all-clone-errors), surface 502 rather than the empty-200
	// branch below.
	if allFanoutFailed(fanoutResults) {
		failures.HandleBadGateway(w, r)
		return
	}

	// winnerHeaders carries custom response headers (e.g. those set by a
	// pool member's path override via response_headers:) from the same
	// member whose mergeFunc will write the final response. Without this,
	// TSM fanout would strip any backend-set headers that FGR would
	// happily propagate. See #970.
	mrf, winnerHeaders := pickWinner(results)
	statusCode, statusHeader, has2xx, hasNon2xx := aggregateStatus(results)
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
	if mrf == nil {
		failures.HandleBadGateway(w, r)
		return
	}
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
	mrf(w, r, accumulator, statusCode)
}

// mergeMultiValuedHeaders forwards Set-Cookie from the winning shard only
// and clears Set-Cookie on winnerHeaders so the subsequent headers.Merge
// (which uses Set semantics) doesn't collapse multi-valued cookies. RFC 6265
// allows multiple Set-Cookie response headers per response.
//
// Set-Cookie is winner-only (not aggregated across shards) so a TSM ALB
// placed in front of tenant-scoped upstreams doesn't mix session cookies
// between tenants. The results slice is retained in the signature for
// future multi-valued headers that genuinely should aggregate.
func mergeMultiValuedHeaders(dst http.Header, _ []gatherResult, winnerHeaders http.Header) {
	if winnerHeaders == nil {
		return
	}
	for _, v := range winnerHeaders.Values(headers.NameSetCookie) {
		dst.Add(headers.NameSetCookie, v)
	}
	winnerHeaders.Del(headers.NameSetCookie)
}

// pruneUnpairedWeightedAvgSeries drops series from sumDS that have no
// matching series in countDS under the same (statementID, pairing hash).
// FinalizeWeightedAvg silently leaves unmatched series unfinalized (the
// raw summed value is returned as if it were an average), so series with
// no countDS counterpart must be removed before finalize. A single
// warning naming the dropped series is appended to sumDS.Warnings so the
// client can see which results were affected. pairingQueryStatement is
// the same statement passed to FinalizeWeightedAvg.
func pruneUnpairedWeightedAvgSeries(sumDS, countDS *dataset.DataSet, pairingQueryStatement string) {
	if sumDS == nil || countDS == nil {
		return
	}
	pairingHash := func(sh *dataset.SeriesHeader) dataset.Hash {
		return sumDS.PairingHash(sh, pairingQueryStatement)
	}
	countSeries := make(map[int]map[dataset.Hash]struct{}, len(countDS.Results))
	for _, r := range countDS.Results {
		if r == nil {
			continue
		}
		set := make(map[dataset.Hash]struct{}, len(r.SeriesList))
		countSeries[r.StatementID] = set
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			set[pairingHash(&s.Header)] = struct{}{}
		}
	}
	var dropped []string
	sumDS.UpdateLock.Lock()
	for _, r := range sumDS.Results {
		if r == nil {
			continue
		}
		set := countSeries[r.StatementID]
		kept := r.SeriesList[:0]
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			if _, ok := set[pairingHash(&s.Header)]; !ok {
				dropped = append(dropped, s.Header.Name)
				continue
			}
			kept = append(kept, s)
		}
		r.SeriesList = kept
	}
	if len(dropped) > 0 {
		sumDS.Warnings = append(sumDS.Warnings,
			"trickster: weighted-avg dropped "+strconv.Itoa(len(dropped))+
				" series with no matching count side: "+strings.Join(dropped, ","))
	}
	sumDS.UpdateLock.Unlock()
}
