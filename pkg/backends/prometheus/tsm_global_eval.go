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
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	tsmql "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

const (
	// partialResponseParam is the Thanos query parameter that overrides an ALB's partial_response setting
	partialResponseParam = "partial_response"
	timeoutParam         = "timeout"
	// globalSubqueryStep is the step of a subquery that names none, Prometheus's default evaluation interval
	globalSubqueryStep = time.Minute
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
)

type globalEvaluator struct {
	settings merge.TSMGlobalSettings
	engine   *promql.Engine
	// slots holds a token for each evaluation running, so no more than MaxConcurrentQueries run at once
	slots chan struct{}
	// cached answers from the ALB's cache when it keeps one, and members lists what its misses read
	cached  *cachedGlobal
	members func() backends.TSMGlobalRequest
	// rangeCacheControl lets shared caches keep a complete range result for the ALB's timeseries TTL
	rangeCacheControl string
}

// NewTSMGlobalEvaluator returns an evaluator running PromQL over a TSM ALB's raw member series, keeping
// results in the ALB's cache when results names one.
func (c *Client) NewTSMGlobalEvaluator(settings merge.TSMGlobalSettings,
	results backends.TSMGlobalCache,
) (backends.TSMGlobalEvaluator, error) {
	e := newGlobalEvaluator(settings)
	if results.Backend != nil {
		e.rangeCacheControl = sharedMaxAge(time.Duration(results.Backend.TimeseriesTTL))
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

func newGlobalEvaluator(settings merge.TSMGlobalSettings) *globalEvaluator {
	e := &globalEvaluator{settings: settings, engine: promql.NewEngine(promql.EngineOpts{
		MaxSamples:               settings.MaxSamples,
		Timeout:                  settings.QueryTimeout,
		NoStepSubqueryIntervalFn: func(int64) int64 { return globalSubqueryStep.Milliseconds() },
		EnableAtModifier:         true,
		EnableNegativeOffset:     true,
		Parser:                   parser.NewParser(tsmql.ParserOptions()),
	})}
	if settings.MaxConcurrentQueries > 0 {
		e.slots = make(chan struct{}, settings.MaxConcurrentQueries)
	}
	return e
}

// Handles reports whether r is an instant or range query, which the evaluator answers.
func (e *globalEvaluator) Handles(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, APIPath+mnQuery) || strings.HasSuffix(r.URL.Path, APIPath+mnQueryRange)
}

// ServeTSMGlobal answers a query from the ALB's cache, evaluating over every member's deduplicated raw
// series only what the cache lacks; without a cache, every query is evaluated.
func (e *globalEvaluator) ServeTSMGlobal(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) {
	if e.cached != nil {
		e.cached.ServeHTTP(w, r)
		return
	}
	e.evaluate(w, r, req)
}

// serveOrigin evaluates a cache miss over the ALB's current members
func (e *globalEvaluator) serveOrigin(w http.ResponseWriter, r *http.Request) {
	e.evaluate(w, r, e.members())
}

// evaluate answers r by evaluating its query over req's members, recording the evaluation's metrics
func (e *globalEvaluator) evaluate(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) {
	start := time.Now()
	outcome := e.serve(w, r, req)
	metrics.ALBGlobalEvaluations.WithLabelValues(e.settings.Name, outcome).Inc()
	metrics.ALBGlobalEvaluationDuration.WithLabelValues(e.settings.Name).Observe(time.Since(start).Seconds())
}

// serve answers r and returns the outcome its metrics record: success, partial, or the error type
func (e *globalEvaluator) serve(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) string {
	fail := func(status int, errorType string, err error, warnings []string) string {
		global.WriteError(w, status, errorType, err, warnings)
		return errorType
	}
	qp, _, _ := params.GetRequestValues(r)
	query := qp.Get(upQuery)
	if query == "" {
		return fail(http.StatusBadRequest, global.ErrorTypeBadData,
			fmt.Errorf("%w: %s is required", errGlobalBadData, upQuery), nil)
	}
	partial := e.settings.PartialResponse
	if v, err := strconv.ParseBool(qp.Get(partialResponseParam)); err == nil {
		partial = v
	}
	members, warnings, err := globalMembers(req, partial)
	if err != nil {
		return fail(http.StatusServiceUnavailable, global.ErrorTypeUnavailable, err, warnings)
	}
	// the query timeout includes the wait for a free evaluation slot
	timeout := e.settings.QueryTimeout
	if v := qp.Get(timeoutParam); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return fail(http.StatusBadRequest, global.ErrorTypeBadData, fmt.Errorf("%w: %w", errGlobalBadData, err), nil)
		}
		if timeout <= 0 || d < timeout {
			timeout = d
		}
	}
	ctx := r.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if e.slots != nil {
		select {
		case e.slots <- struct{}{}:
			defer func() { <-e.slots }()
		case <-ctx.Done():
			status, errorType := globalErrorType(ctx.Err())
			return fail(status, errorType, fmt.Errorf("waiting for an evaluation slot: %w", ctx.Err()), warnings)
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
	queryable := global.NewQueryable(members, e.settings.ReplicaLabels, e.settings.Deduplication, partial,
		func(member string) { metrics.ALBGlobalMemberFailures.WithLabelValues(e.settings.Name, member).Inc() })
	qry, err := e.newQuery(ctx, r, qp.Get, queryable, query)
	if err != nil {
		return fail(http.StatusBadRequest, global.ErrorTypeBadData, err, warnings)
	}
	defer qry.Close()
	res := qry.Exec(ctx)
	partialResult := len(warnings) > 0 || slices.ContainsFunc(res.Warnings.AsErrors(), func(err error) bool {
		return errors.Is(err, global.ErrMemberUnavailable)
	})
	resultWarnings, infos := res.Warnings.AsStrings(query, 0, 0)
	slices.Sort(resultWarnings)
	slices.Sort(infos)
	warnings = append(warnings, resultWarnings...)
	if res.Err != nil {
		status, errorType := globalErrorType(res.Err)
		return fail(status, errorType, res.Err, warnings)
	}
	value := res.Value
	if limit, err := strconv.Atoi(qp.Get(upLimit)); err == nil && limit > 0 {
		var truncated bool
		if value, truncated = limitSeries(value, limit); truncated {
			warnings = append(warnings, globalLimitWarning)
		}
	}
	result, outcome := globalResultHeader, globalOutcomeSuccess
	switch {
	case partialResult:
		result, outcome = globalPartialResultHeader, globalOutcomePartial
		// a result missing a member's series is only true until the member returns
		w.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
	case !strings.HasSuffix(r.URL.Path, APIPath+mnQueryRange):
		w.Header().Set(headers.NameCacheControl, sharedMaxAge(instantSharedMaxAge))
	case e.rangeCacheControl != "":
		w.Header().Set(headers.NameCacheControl, e.rangeCacheControl)
	}
	w.Header().Set(headers.NameTricksterResult, result)
	global.WriteResult(w, value, warnings, infos)
	return outcome
}

// newQuery prepares the instant or range query r names
func (e *globalEvaluator) newQuery(ctx context.Context, r *http.Request, get func(string) string,
	queryable *global.Queryable, query string,
) (promql.Query, error) {
	opts := promql.NewPrometheusQueryOpts(false, 0)
	if !strings.HasSuffix(r.URL.Path, APIPath+mnQueryRange) {
		ts := time.Now()
		if v := get(upTime); v != "" {
			var err error
			if ts, err = parseTime(v); err != nil {
				return nil, fmt.Errorf("%w: %w", errGlobalBadData, err)
			}
		}
		return e.engine.NewInstantQuery(ctx, queryable, opts, query, ts)
	}
	start, err := parseTime(get(upStart))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errGlobalBadData, upStart, err)
	}
	end, err := parseTime(get(upEnd))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errGlobalBadData, upEnd, err)
	}
	step, err := parseDuration(get(upStep))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errGlobalBadData, upStep, err)
	}
	if end.Before(start) {
		return nil, fmt.Errorf("%w: end timestamp must not be before start time", errGlobalBadData)
	}
	if end.Sub(start)/step > maxGlobalRangePoints {
		return nil, fmt.Errorf("%w: exceeded maximum resolution of %d points per timeseries", errGlobalBadData,
			maxGlobalRangePoints)
	}
	return e.engine.NewRangeQuery(ctx, queryable, opts, query, start, end, step)
}

// globalMembers returns the members an evaluation reads, warning of each member it can't when partial
// responses are allowed, and failing otherwise
func globalMembers(req backends.TSMGlobalRequest, partial bool) ([]global.Member, []string, error) {
	members := make([]global.Member, 0, len(req.Members))
	var unavailable []string
	for _, m := range req.Members {
		source, ok := m.Source.(global.Source)
		if !ok {
			unavailable = append(unavailable, fmt.Sprintf("%s: %s", m.Name, errSeriesAPIUnsupported))
			continue
		}
		members = append(members, global.Member{Name: m.Name, Source: source})
	}
	for _, u := range req.Unavailable {
		if u.Reason != nil {
			unavailable = append(unavailable, fmt.Sprintf("%s: %s", u.Name, u.Reason))
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

// globalErrorType returns the status and Prometheus error type that answer a failed evaluation; a storage
// error is classified by the source error it carries
func globalErrorType(err error) (int, string) {
	var storage promql.ErrStorage
	if errors.As(err, &storage) && storage.Err != nil {
		err = storage.Err
	}
	var timeout promql.ErrQueryTimeout
	var canceled promql.ErrQueryCanceled
	var samples promql.ErrTooManySamples
	switch {
	case errors.As(err, &timeout) || errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, global.ErrorTypeTimeout
	case errors.As(err, &canceled) || errors.Is(err, context.Canceled):
		return statusClientClosedRequest, global.ErrorTypeCanceled
	case errors.As(err, &samples) || errors.Is(err, global.ErrFetchLimit):
		return http.StatusUnprocessableEntity, global.ErrorTypeExecution
	case errors.Is(err, global.ErrMemberUnavailable):
		return http.StatusServiceUnavailable, global.ErrorTypeUnavailable
	case storage.Err != nil:
		return http.StatusInternalServerError, global.ErrorTypeInternal
	}
	return http.StatusUnprocessableEntity, global.ErrorTypeExecution
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

var _ backends.TSMGlobalEvaluatorProvider = (*Client)(nil)
