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
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/fanout"
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
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"golang.org/x/sync/errgroup"
)

const (
	ShortName            = names.MechanismTSM
	Name      types.Name = "time_series_merge"
)

type handler struct {
	mech.PoolHolder
	mergePaths            []string            // paths handled by the alb client that are enabled for tsmerge
	nonmergeHandler       types.PoolMechanism // when methodology is tsmerge, this handler is for non-mergeable paths
	outputFormat          string              // the provider output format (e.g., "prometheus")
	tsmOptions            options.TimeSeriesMergeOptions
	maxCaptureBytes       int
	maxFanoutCaptureBytes int

	// poolVersion increments on every SetPool so cached pool-derived data
	// (stripKeys) can be invalidated without locking.
	poolVersion atomic.Uint64
	// cachedStripKeys memoizes the stripKeys slice across requests as long
	// as the pool hasn't been replaced. Hot path is a single atomic load
	// plus a uint64 compare.
	cachedStripKeys atomic.Pointer[stripKeysSnapshot]
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
	nmh, _ := rr.New(nil, nil)
	nmpm, _ := nmh.(types.PoolMechanism)
	out := &handler{
		nonmergeHandler:       nmpm,
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

// SetPool overrides PoolHolder.SetPool to also propagate the pool to the
// non-merge handler used for paths that bypass the TSM merge path.
func (h *handler) SetPool(p pool.Pool) {
	h.PoolHolder.SetPool(p)
	if h.nonmergeHandler != nil {
		h.nonmergeHandler.SetPool(p)
	}
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
	if hl[0] != nil {
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
	// Stripping is only needed when a non-dedup strategy is in play. The set
	// is cached and reused across requests until the pool is replaced.
	var stripKeys []string
	if mergeStrategy != dataset.MergeStrategyDedup || needsDualQuery {
		stripKeys = h.computeStripKeys(hl)
	}

	// Single-live-member fast path: with one shard there is nothing to merge
	// or dedup against, so the lone backend's response IS the answer. Skip
	// only when label stripping or dual-query rewriting would still need to
	// happen (D1 covers the strip case; weighted-avg covers the dual-query
	// case). Otherwise the merge path's OnResult stripping handles solo pools
	// correctly, and degraded N-pools where N-1 are unhealthy now return the
	// surviving member's response directly instead of 502'ing on a one-shard
	// merge that has no peer to dedup or cross-merge against.
	// dual-query still needs merge even for one healthy target; one-shard fast path requires all three conditions
	if l == 1 && len(stripKeys) == 0 && !needsDualQuery {
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
	results := make([]gatherResult, l)

	r, err := fanout.PrimeBody(r)
	if err != nil {
		failures.HandleBadGateway(w, r)
		return
	}

	dedupToleranceNanos := h.dedupToleranceNanos()
	fanoutResults, _ := fanout.All(r.Context(), r, hl, fanout.Config{
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
			if len(stripKeys) > 0 && rsc2.TS != nil {
				if ds, ok := rsc2.TS.(*dataset.DataSet); ok {
					ds.StripTags(stripKeys)
				}
			}
			var contributed bool
			if rsc2.MergeFunc != nil {
				if rsc2.TS != nil {
					rsc2.MergeFunc(accumulator, rsc2.TS, i)
					contributed = true
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
						rsc2.MergeFunc(accumulator, body, i)
						contributed = true
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
				failed:     !contributed,
			}
		},
	})

	for i, fr := range fanoutResults {
		if fr.Failed && !results[i].failed {
			results[i].failed = true
		}
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
	if mrf != nil {
		mrf(w, r, accumulator, statusCode)
	}
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
		if pairingQueryStatement == "" {
			return sh.CalculateHash()
		}
		return sh.CalculateHashWithQueryStatement(pairingQueryStatement)
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

	sumBase, err := fanout.PrimeBody(sumBase)
	if err != nil {
		failures.HandleBadGateway(w, r)
		return
	}
	countBase, err = fanout.PrimeBody(countBase)
	if err != nil {
		failures.HandleBadGateway(w, r)
		return
	}

	sumAccum := merge.NewAccumulator()
	countAccum := merge.NewAccumulator()

	limit := h.tsmOptions.ConcurrencyOptions.GetQueryConcurrencyLimit()

	// results captures sum-query outcomes only -- the response envelope
	// (status, headers, RespondFunc) comes from the sum side; count results
	// affect the arithmetic, not the envelope.
	results := make([]gatherResult, l)

	dedupToleranceNanos := h.dedupToleranceNanos()
	resourcesFn := func(int) *request.Resources {
		return &request.Resources{
			IsMergeMember:         true,
			TSReqestOptions:       rsc.TSReqestOptions,
			TSMergeStrategy:       int(dataset.MergeStrategySum),
			TSDedupToleranceNanos: dedupToleranceNanos,
		}
	}

	parentCtx := r.Context()
	var eg errgroup.Group
	eg.Go(func() error {
		_, err := fanout.All(parentCtx, sumBase, hl, fanout.Config{
			Mechanism:             names.MechanismTSM,
			Variant:               "avg-sum",
			ConcurrencyLimit:      limit,
			MaxCaptureBytes:       h.maxCaptureBytes,
			MaxFanoutCaptureBytes: h.maxFanoutCaptureBytes,
			Resources:             resourcesFn,
			OnResult: func(i int, fr *fanout.Result) {
				if fr.Failed || fr.Request == nil || fr.Capture == nil {
					results[i].failed = true
					return
				}
				rsc2 := request.GetResources(fr.Request)
				if rsc2 == nil {
					results[i].failed = true
					return
				}
				if len(stripKeys) > 0 && rsc2.TS != nil {
					if ds, ok := rsc2.TS.(*dataset.DataSet); ok {
						ds.StripTags(stripKeys)
					}
				}
				if rsc2.MergeFunc != nil && rsc2.TS != nil {
					rsc2.MergeFunc(sumAccum, rsc2.TS, i)
				}
				sc := fr.Capture.StatusCode()
				if rsc2.Response != nil && rsc2.Response.StatusCode > 0 {
					sc = rsc2.Response.StatusCode
				}
				results[i] = gatherResult{
					statusCode: sc,
					header:     fr.Capture.Header(),
					mergeFunc:  rsc2.MergeRespondFunc,
				}
			},
		})
		return err
	})
	eg.Go(func() error {
		_, err := fanout.All(parentCtx, countBase, hl, fanout.Config{
			Mechanism:             names.MechanismTSM,
			Variant:               "avg-count",
			ConcurrencyLimit:      limit,
			MaxCaptureBytes:       h.maxCaptureBytes,
			MaxFanoutCaptureBytes: h.maxFanoutCaptureBytes,
			Resources:             resourcesFn,
			OnResult: func(i int, fr *fanout.Result) {
				// Count-side intentionally does not touch results[i]: the
				// sum-side owns the response envelope. Panics in this slot
				// are already recovered + countered by fanout.All.
				if fr.Failed || fr.Request == nil {
					return
				}
				rsc2 := request.GetResources(fr.Request)
				if rsc2 == nil {
					return
				}
				if len(stripKeys) > 0 && rsc2.TS != nil {
					if ds, ok := rsc2.TS.(*dataset.DataSet); ok {
						ds.StripTags(stripKeys)
					}
				}
				if rsc2.MergeFunc != nil && rsc2.TS != nil {
					rsc2.MergeFunc(countAccum, rsc2.TS, i)
				}
			},
		})
		return err
	})
	if err := eg.Wait(); err != nil {
		logger.Warn("tsm avg gather failure", logging.Pairs{"error": err})
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
				pruneUnpairedWeightedAvgSeries(sumDS, countDS, query)
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
	statusCode, statusHeader, has2xx, hasNon2xx := aggregateStatus(results)
	if has2xx && hasNon2xx {
		statusHeader = headers.MergeResultHeaderVals(statusHeader, "engine=ALB; status=phit")
	}

	// See serveStandard for the rationale -- carry the winner's custom
	// response headers through the fanout so backend-set headers like
	// `X-Test-Origin` survive the merge. (#970)
	if mrf == nil {
		failures.HandleBadGateway(w, r)
		return
	}
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
