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
	globalLimitWarning        = "results truncated due to limit"
)

var (
	errGlobalNoMembers = errors.New("no pool member can be read")
	errGlobalBadData   = errors.New("invalid query parameter")
)

type globalEvaluator struct {
	settings merge.TSMGlobalSettings
	engine   *promql.Engine
}

// NewTSMGlobalEvaluator returns an evaluator running PromQL over a TSM ALB's raw member series.
func (c *Client) NewTSMGlobalEvaluator(settings merge.TSMGlobalSettings) (backends.TSMGlobalEvaluator, error) {
	return newGlobalEvaluator(settings), nil
}

func newGlobalEvaluator(settings merge.TSMGlobalSettings) *globalEvaluator {
	return &globalEvaluator{settings: settings, engine: promql.NewEngine(promql.EngineOpts{
		MaxSamples:               settings.MaxSamples,
		Timeout:                  settings.QueryTimeout,
		NoStepSubqueryIntervalFn: func(int64) int64 { return globalSubqueryStep.Milliseconds() },
		EnableAtModifier:         true,
		EnableNegativeOffset:     true,
		Parser:                   parser.NewParser(tsmql.ParserOptions()),
	})}
}

// Handles reports whether r is an instant or range query, which the evaluator answers.
func (e *globalEvaluator) Handles(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, APIPath+mnQuery) || strings.HasSuffix(r.URL.Path, APIPath+mnQueryRange)
}

// ServeTSMGlobal evaluates the requested query once over the deduplicated raw series of every member.
func (e *globalEvaluator) ServeTSMGlobal(w http.ResponseWriter, r *http.Request, req backends.TSMGlobalRequest) {
	qp, _, _ := params.GetRequestValues(r)
	query := qp.Get(upQuery)
	if query == "" {
		global.WriteError(w, http.StatusBadRequest, global.ErrorTypeBadData,
			fmt.Errorf("%w: %s is required", errGlobalBadData, upQuery), nil)
		return
	}
	partial := e.settings.PartialResponse
	if v, err := strconv.ParseBool(qp.Get(partialResponseParam)); err == nil {
		partial = v
	}
	members, warnings, err := globalMembers(req, partial)
	if err != nil {
		global.WriteError(w, http.StatusServiceUnavailable, global.ErrorTypeUnavailable, err, warnings)
		return
	}
	ctx := r.Context()
	if v := qp.Get(timeoutParam); v != "" {
		timeout, err := parseDuration(v)
		if err != nil {
			global.WriteError(w, http.StatusBadRequest, global.ErrorTypeBadData, fmt.Errorf("%w: %w", errGlobalBadData, err), nil)
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ctx = global.WithBudget(global.WithRequest(ctx, r), &global.Budget{
		MaxSeries: int64(e.settings.MaxFetchedSeries), MaxSamples: e.settings.MaxFetchedSamples,
	})
	queryable := global.NewQueryable(members, e.settings.ReplicaLabels, e.settings.Deduplication, partial)
	qry, err := e.newQuery(ctx, r, qp.Get, queryable, query)
	if err != nil {
		global.WriteError(w, http.StatusBadRequest, global.ErrorTypeBadData, err, warnings)
		return
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
		global.WriteError(w, status, errorType, res.Err, warnings)
		return
	}
	value := res.Value
	if limit, err := strconv.Atoi(qp.Get(upLimit)); err == nil && limit > 0 {
		var truncated bool
		if value, truncated = limitSeries(value, limit); truncated {
			warnings = append(warnings, globalLimitWarning)
		}
	}
	result := globalResultHeader
	if partialResult {
		result = globalPartialResultHeader
	}
	w.Header().Set(headers.NameTricksterResult, result)
	global.WriteResult(w, value, warnings, infos)
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
