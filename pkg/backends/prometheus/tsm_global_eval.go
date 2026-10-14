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
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	tsmql "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	cachestatus "github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
	"github.com/trickstercache/trickster/v2/pkg/util/drain"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

const (
	// partialResponseParam is the Thanos query parameter that overrides an ALB's partial_response setting
	partialResponseParam = "partial_response"
	timeoutParam         = "timeout"
	// globalSubqueryStep is the step of a subquery that names none, Prometheus's default evaluation interval
	globalSubqueryStep = time.Minute
	// globalLookback is how far back the engine looks for a sample, Prometheus's default, and so how far
	// back a sample arriving late can still change a result
	globalLookback = 5 * time.Minute
	// maxGlobalRangePoints is the most steps a range query may evaluate, as Prometheus allows
	maxGlobalRangePoints = 11000
	// statusClientClosedRequest is the status Prometheus answers a canceled query with
	statusClientClosedRequest = 499

	globalResultHeader        = "engine=ALB; status=proxy-only"
	globalPartialResultHeader = "engine=ALB; status=phit"
	globalOutcomeSuccess      = "success"
	globalOutcomePartial      = "partial"
	globalLimitWarning        = "results truncated due to limit"
)

var (
	errGlobalNoMembers = errors.New("no pool member can be read")
	errGlobalBadData   = errors.New("invalid query parameter")
	errGlobalRetired   = errors.New("tsm global evaluator is retired")
)

type globalEvaluator struct {
	settings merge.TSMGlobalSettings
	engine   *promql.Engine
	// slots holds a token for each evaluation running, so no more than MaxConcurrentQueries run at once
	slots chan struct{}
	// cached answers from the ALB's cache when it keeps one, and members lists what its misses read
	cached  *cachedGlobal
	members func() backends.TSMGlobalRequest
	// raw keeps members' raw series in the ALB's cache, so evaluations read members only for what it lacks
	raw *global.RawCache
	// ttl is how long a complete result stays cached once the samples it reads have all arrived, which
	// they may not have within the volatile window of now
	ttl            time.Duration
	volatileWindow time.Duration
	volatilePoints int
	// admitted holds a place for each evaluation running, and admits none once the evaluator retires, which ends retired,
	// canceling those still running
	admitted drain.Gate
	retired  context.Context
	retire   context.CancelFunc
}

// NewTSMGlobalEvaluator returns an evaluator running PromQL over a TSM ALB's raw member series, keeping
// results in the ALB's cache when results names one.
func (c *Client) NewTSMGlobalEvaluator(settings merge.TSMGlobalSettings,
	results backends.TSMGlobalCache,
) (backends.TSMGlobalEvaluator, error) {
	e := newGlobalEvaluator(settings)
	if o := results.Backend; o != nil {
		e.ttl, e.volatileWindow = time.Duration(o.TimeseriesTTL), time.Duration(o.VolatileWindow)
		e.volatilePoints = o.VolatileWindowPoints
		if results.Cache != nil {
			e.raw = e.newRawCache(results)
		}
	}
	if results.Cache == nil || results.Backend == nil || results.Members == nil {
		return e, nil
	}
	cached, err := e.newCachedGlobal(results)
	if err != nil {
		return nil, err
	}
	e.cached, e.members = cached, results.Members
	return e, nil
}

// newRawCache returns a cache of members' raw series in the ALB's cache, keeping for its TTL, in blocks within its object size
// limit, what's within its raw cache window and older than the volatile window: the ALB's when it sets one, else each member's
func (e *globalEvaluator) newRawCache(results backends.TSMGlobalCache) *global.RawCache {
	name := e.settings.Name
	opts := global.RawCacheOptions{
		Name:          name,
		TTL:           e.ttl,
		Window:        e.settings.RawCacheWindow,
		MaxObjectSize: int64(results.Backend.MaxObjectSizeBytes),
		MaxSeries:     int64(e.settings.MaxFetchedSeries), MaxSamples: e.settings.MaxFetchedSamples,
		MaxResponseBytes: e.settings.MaxResponseBytes,
		Observe: func(status cachestatus.LookupStatus) {
			metrics.ALBGlobalRawReads.WithLabelValues(name, status.String()).Inc()
		},
		Subsumed: metrics.ALBGlobalRawSubsumed.WithLabelValues(name).Inc,
	}
	if results.Backend.HasVolatileWindow() {
		volatile := e.volatileWindow
		opts.VolatileWindow = &volatile
	}
	// failures to keep or read blocks, as for their size, count among the cache's events, as other delta engines' do
	if conf := results.Cache.Configuration(); conf != nil {
		cacheName, provider := conf.Name, conf.Provider
		opts.ObserveCacheFailure = func(reason string) {
			metrics.CacheEvents.WithLabelValues(cacheName, provider, keys.Error, reason).Inc()
		}
	}
	return global.NewRawCache(results.Cache, opts)
}

func newGlobalEvaluator(settings merge.TSMGlobalSettings) *globalEvaluator {
	e := &globalEvaluator{settings: settings, engine: promql.NewEngine(promql.EngineOpts{
		MaxSamples:               settings.MaxSamples,
		Timeout:                  settings.QueryTimeout,
		LookbackDelta:            globalLookback,
		NoStepSubqueryIntervalFn: func(int64) int64 { return globalSubqueryStep.Milliseconds() },
		EnableAtModifier:         true,
		EnableNegativeOffset:     true,
		Parser:                   parser.NewParser(tsmql.ParserOptions()),
	})}
	if settings.MaxConcurrentQueries > 0 {
		e.slots = make(chan struct{}, settings.MaxConcurrentQueries)
	}
	e.retired, e.retire = context.WithCancel(context.Background())
	return e
}

// Retire ends the evaluator once its requests have drained: it admits no more evaluations and cancels those running with
// the raw cache's reads, then waits until ctx ends for them to return and for what the raw cache still stores.
func (e *globalEvaluator) Retire(ctx context.Context) {
	e.admitted.Close()
	e.retire()
	stored := e.raw == nil || e.raw.Close(ctx)
	if !e.admitted.Wait(ctx) || !stored {
		logger.Warn("tsm global evaluator retired before its work ended", logging.Pairs{keys.ALB_Name: e.settings.Name})
	}
}

// Handles reports whether r is an instant or range query, which the evaluator answers.
func (*globalEvaluator) Handles(r *http.Request) bool {
	return globalRoute(r) != ""
}

// ServeTSMGlobal answers a query from the ALB's cache, evaluating it over every member's deduplicated raw
// series when the cache lacks it; without a cache, every query is evaluated.
func (e *globalEvaluator) ServeTSMGlobal(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) {
	route := globalRoute(r)
	if route == "" {
		failures.HandleNotFound(w, r)
		return
	}
	// whatever prefix reached the ALB, members' path settings and the cache see the query's own path
	r = r.Clone(r.Context())
	r.URL.Path, r.URL.RawPath = route, ""
	if e.cached != nil {
		e.cached.serve(w, r, req)
		return
	}
	e.evaluate(w, r, req)
}

// serveOrigin evaluates a miss over the pool snapshot its key's namespace came from; the request that
// started it owns it, so its client's cancellation cancels it
func (e *globalEvaluator) serveOrigin(w http.ResponseWriter, r *http.Request) {
	owner := e.cached.owner(r)
	if owner == nil {
		// no client's request, as a revalidation, reads the current pool, kept only under its own namespace
		req := e.members()
		if !e.cached.keyedFor(r, req.Namespace) {
			w = &noStoreWriter{ResponseWriter: w}
		}
		e.evaluate(w, r, req)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(owner.ctx, cancel)()
	e.evaluate(w, r.WithContext(ctx), owner.req)
}

// noStoreWriter answers with Cache-Control no-store, whatever the evaluation allowed
type noStoreWriter struct {
	http.ResponseWriter
	wrote bool
}

func (n *noStoreWriter) WriteHeader(status int) {
	if !n.wrote {
		n.wrote = true
		n.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
	}
	n.ResponseWriter.WriteHeader(status)
}

func (n *noStoreWriter) Write(p []byte) (int, error) {
	if !n.wrote {
		n.WriteHeader(http.StatusOK)
	}
	return n.ResponseWriter.Write(p)
}

// evaluate answers r by evaluating its query over req's members, recording the evaluation's metrics
func (e *globalEvaluator) evaluate(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) {
	start := time.Now()
	outcome := e.serveAdmitted(w, r, req)
	metrics.ALBGlobalEvaluations.WithLabelValues(e.settings.Name, outcome).Inc()
	metrics.ALBGlobalEvaluationDuration.WithLabelValues(e.settings.Name).Observe(time.Since(start).Seconds())
}

// serveAdmitted serves r unless the evaluator has retired, after which no evaluation reads its members
func (e *globalEvaluator) serveAdmitted(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) string {
	if !e.admitted.Enter() {
		w.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
		global.WriteError(w, http.StatusServiceUnavailable, global.ErrorTypeUnavailable, errGlobalRetired, nil,
			e.settings.MaxResponseBytes)
		return global.ErrorTypeUnavailable
	}
	defer e.admitted.Exit()
	return e.serve(w, r, req)
}

// failure returns the status, error type and error that answer an evaluation failing with err; one the evaluator's retirement
// ended is unavailable rather than canceled or timed out, unless it failed with execution, as on a limit, which decides as it would
func (e *globalEvaluator) failure(err error) (int, string, error) {
	status, errorType := globalErrorType(err)
	if errorType != global.ErrorTypeExecution && e.retired.Err() != nil {
		return http.StatusServiceUnavailable, global.ErrorTypeUnavailable, fmt.Errorf("%w: %w", errGlobalRetired, err)
	}
	return status, errorType, err
}

// reject answers a query whose parameters are invalid, as an evaluation of it would
func (e *globalEvaluator) reject(w http.ResponseWriter, err error) {
	w.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
	global.WriteError(w, http.StatusBadRequest, global.ErrorTypeBadData, err, nil, e.settings.MaxResponseBytes)
	metrics.ALBGlobalEvaluations.WithLabelValues(e.settings.Name, global.ErrorTypeBadData).Inc()
}

// partial reports whether q's evaluation answers from the members it can read when others fail
func (e *globalEvaluator) partial(q globalQuery) bool {
	if q.partial != nil {
		return *q.partial
	}
	return e.settings.PartialResponse
}

// timeout returns q's evaluation timeout: the ALB's, or the request's when that is shorter
func (e *globalEvaluator) timeout(q globalQuery) time.Duration {
	if t := e.settings.QueryTimeout; q.timeout <= 0 || (t > 0 && t <= q.timeout) {
		return t
	}
	return q.timeout
}

// cacheControl lets shared caches keep q's complete result for the ALB's time series TTL, or only
// briefly while it reaches into the recent past, where samples may still arrive
func (e *globalEvaluator) cacheControl(q globalQuery, now time.Time) string {
	brief := instantSharedMaxAge
	if e.ttl > 0 {
		brief = min(brief, e.ttl)
	}
	window := max(globalLookback, e.volatileWindow, time.Duration(e.volatilePoints)*q.step)
	if last := q.last(); e.ttl <= 0 || last.IsZero() || last.After(now.Add(-window)) {
		return sharedMaxAge(brief)
	}
	return sharedMaxAge(e.ttl)
}

// serve answers r and returns the outcome its metrics record: success, partial, or the error type
func (e *globalEvaluator) serve(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) string {
	fail := func(status int, errorType string, err error, warnings []string) string {
		// no cache keeps a failure, as a member's recovery or a later evaluation's timing can answer the query; the failure
		// keeps within the result's limit, as one answering a result too large for it must
		w.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
		global.WriteError(w, status, errorType, err, warnings, e.settings.MaxResponseBytes)
		return errorType
	}
	start := time.Now()
	q, err := parseGlobalQuery(r)
	if err != nil {
		return fail(http.StatusBadRequest, global.ErrorTypeBadData, err, nil)
	}
	partial := e.partial(q)
	members, warnings, err := globalMembers(e.settings.Name, req, partial)
	if err != nil {
		return fail(http.StatusServiceUnavailable, global.ErrorTypeUnavailable, err, warnings)
	}
	// the query timeout includes the wait for a free evaluation slot, as does the evaluator retiring, which ends the evaluation
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout := e.timeout(q); timeout > 0 {
		ctx, cancel = context.WithTimeout(r.Context(), timeout)
	} else {
		ctx, cancel = context.WithCancel(r.Context())
	}
	defer cancel()
	defer context.AfterFunc(e.retired, cancel)()
	if e.slots != nil {
		select {
		case e.slots <- struct{}{}:
			defer func() { <-e.slots }()
		case <-ctx.Done():
			status, errorType, err := e.failure(fmt.Errorf("waiting for an evaluation slot: %w", ctx.Err()))
			return fail(status, errorType, err, warnings)
		}
	}
	budget := &global.Budget{
		MaxSeries: int64(e.settings.MaxFetchedSeries), MaxSamples: e.settings.MaxFetchedSamples,
		MaxResponseBytes: e.settings.MaxResponseBytes,
	}
	defer func() {
		series, samples := budget.Fetched()
		metrics.ALBGlobalFetchedSeries.WithLabelValues(e.settings.Name).Add(float64(series))
		metrics.ALBGlobalFetchedSamples.WithLabelValues(e.settings.Name).Add(float64(samples))
	}()
	ctx = global.WithBudget(global.WithRequest(ctx, r), budget)
	// the first member read reported failing on a fetch limit decides the answer, even when the deadline, the client, or another
	// read's failure, as for a selector the engine expands first, ends the evaluation
	var limited atomic.Pointer[error]
	queryable := global.NewQueryable(members, e.settings.ReplicaLabels, e.settings.Deduplication, partial,
		func(member string, err error) {
			if errors.Is(err, global.ErrFetchLimit) {
				limit := fmt.Errorf("%w: %s: %w", global.ErrMemberUnavailable, member, err)
				limited.CompareAndSwap(nil, &limit)
			}
			e.memberFailed(member, err)
		})
	if e.raw != nil {
		queryable.WithRawCache(e.raw)
		ctx = global.WithKeyHeaders(ctx, keyHeaders(r, req))
	}
	qry, err := e.newQuery(ctx, q, queryable)
	if err != nil {
		return fail(http.StatusBadRequest, global.ErrorTypeBadData, fmt.Errorf("%w: %w", errGlobalBadData, err),
			warnings)
	}
	defer qry.Close()
	res := qry.Exec(ctx)
	partialResult := len(warnings) > 0 || slices.ContainsFunc(res.Warnings.AsErrors(), func(err error) bool {
		return errors.Is(err, global.ErrMemberUnavailable)
	})
	resultWarnings, infos := res.Warnings.AsStrings(q.query, 0, 0)
	slices.Sort(resultWarnings)
	slices.Sort(infos)
	warnings = append(warnings, resultWarnings...)
	if res.Err != nil {
		status, errorType, err := e.failure(limitDecides(res.Err, limited.Load()))
		return fail(status, errorType, err, warnings)
	}
	if partialResult && e.retired.Err() != nil {
		// reads the retirement stopped are missing whether they saw the raw cache close or the evaluation end first
		return fail(http.StatusServiceUnavailable, global.ErrorTypeUnavailable, errGlobalRetired, warnings)
	}
	value := res.Value
	if q.limit > 0 {
		var truncated bool
		if value, truncated = limitSeries(value, q.limit); truncated {
			warnings = append(warnings, globalLimitWarning)
		}
	}
	result, outcome := globalResultHeader, globalOutcomeSuccess
	if partialResult {
		result, outcome = globalPartialResultHeader, globalOutcomePartial
		// a result missing a member's series is only true until the member returns
		w.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
	} else {
		w.Header().Set(headers.NameCacheControl, e.cacheControl(q, start))
	}
	w.Header().Set(headers.NameTricksterResult, result)
	if err := global.WriteResult(w, value, warnings, infos, e.settings.MaxResponseBytes); err != nil {
		// nothing of the result was written, so the answer is the failure alone
		w.Header().Del(headers.NameTricksterResult)
		return fail(http.StatusUnprocessableEntity, global.ErrorTypeExecution, err, warnings)
	}
	return outcome
}

// newQuery prepares q's instant or range query
func (e *globalEvaluator) newQuery(ctx context.Context, q globalQuery, queryable *global.Queryable,
) (promql.Query, error) {
	opts := promql.NewPrometheusQueryOpts(false, 0)
	if q.isRange {
		return e.engine.NewRangeQuery(ctx, queryable, opts, q.query, q.start, q.end, q.step)
	}
	at := q.at
	if at.IsZero() {
		at = time.Now()
	}
	return e.engine.NewInstantQuery(ctx, queryable, opts, q.query, at)
}

// keyHeaders returns the headers that partition r's cached raw series besides Authorization: the tenant
// headers, those r's route keys results by, and req's members' own
func keyHeaders(r *http.Request, req backends.TSMGlobalRequest) []string {
	names := appendHeaders(slices.Clone(globalTenantHeaders), req.KeyHeaders)
	if rsc := request.GetResources(r); rsc != nil && rsc.PathConfig != nil {
		names = appendHeaders(names, rsc.PathConfig.CacheKeyHeaders)
	}
	return names
}

// memberFailed counts and logs a member read that failed, unless the client canceled it
func (e *globalEvaluator) memberFailed(member string, err error) {
	metrics.ALBGlobalMemberFailures.WithLabelValues(e.settings.Name, member).Inc()
	if errors.Is(err, context.Canceled) {
		return
	}
	logger.Warn("tsm global evaluation member read failed",
		logging.Pairs{keys.ALB_Name: e.settings.Name, keys.Member: member, keys.Error: err.Error()})
}

// globalMembers returns the members an evaluation reads, warning of each it can't when partial responses
// are allowed and failing otherwise; why a member can't be read is only logged
func globalMembers(alb string, req backends.TSMGlobalRequest, partial bool) ([]global.Member, []string, error) {
	members := make([]global.Member, 0, len(req.Members))
	var unavailable []string
	unreadable := func(member string, reason error) {
		unavailable = append(unavailable, member)
		logger.WarnOnce("tsm.global.unreadable."+alb+"."+member+"."+reason.Error(),
			"tsm global evaluation member can't be read",
			logging.Pairs{keys.ALB_Name: alb, keys.Member: member, keys.Error: reason.Error()})
	}
	for _, m := range req.Members {
		source, ok := m.Source.(global.Source)
		if !ok {
			unreadable(m.Name, errSeriesAPIUnsupported)
			continue
		}
		members = append(members, global.Member{Name: m.Name, Source: source})
	}
	for _, u := range req.Unavailable {
		if u.Reason != nil {
			unreadable(u.Name, u.Reason)
			continue
		}
		unavailable = append(unavailable, u.Name)
	}
	warnings := make([]string, len(unavailable))
	for i, u := range unavailable {
		warnings[i] = fmt.Sprintf("%s: %s", global.ErrMemberUnavailable, u)
	}
	switch {
	case len(members) == 0:
		return nil, warnings, errGlobalNoMembers
	case len(unavailable) > 0 && !partial:
		return nil, nil, fmt.Errorf("%w: %s", global.ErrMemberUnavailable, strings.Join(unavailable, "; "))
	}
	return members, warnings, nil
}

// globalErrorType returns the status and Prometheus error type that answer a failed evaluation; a storage error is classified
// by the source error it carries too. A limit reached decides it, whatever deadline or cancellation the error also carries
func globalErrorType(err error) (int, string) {
	var storage promql.ErrStorage
	if errors.As(err, &storage) && storage.Err != nil {
		err = errors.Join(storage.Err, err)
	}
	var timeout promql.ErrQueryTimeout
	var canceled promql.ErrQueryCanceled
	var samples promql.ErrTooManySamples
	switch {
	case errors.As(err, &samples) || errors.Is(err, global.ErrFetchLimit):
		return http.StatusUnprocessableEntity, global.ErrorTypeExecution
	case errors.As(err, &timeout) || errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, global.ErrorTypeTimeout
	case errors.As(err, &canceled) || errors.Is(err, context.Canceled):
		return statusClientClosedRequest, global.ErrorTypeCanceled
	case errors.Is(err, global.ErrMemberUnavailable):
		return http.StatusServiceUnavailable, global.ErrorTypeUnavailable
	case storage.Err != nil:
		return http.StatusInternalServerError, global.ErrorTypeInternal
	}
	return http.StatusUnprocessableEntity, global.ErrorTypeExecution
}

// limitDecides returns err joined with limit, a member read's failure on a fetch limit, unless err already carries one: the engine
// reports the first failure it meets, as the deadline or another selector's read failing, which needn't be the limit
func limitDecides(err error, limit *error) error {
	var storage promql.ErrStorage
	if limit == nil || errors.Is(err, global.ErrFetchLimit) ||
		(errors.As(err, &storage) && errors.Is(storage.Err, global.ErrFetchLimit)) {
		return err
	}
	return errors.Join(*limit, err)
}

// limitSeries keeps the first limit series of a vector or matrix, reporting whether any were dropped
func limitSeries(value parser.Value, limit int) (parser.Value, bool) {
	switch v := value.(type) {
	case promql.Vector:
		if len(v) > limit {
			return v[:limit], true
		}
	case promql.Matrix:
		if len(v) > limit {
			return v[:limit], true
		}
	}
	return value, false
}

var (
	_ backends.TSMGlobalEvaluatorProvider = (*Client)(nil)
	_ backends.Retirer                    = (*globalEvaluator)(nil)
)
