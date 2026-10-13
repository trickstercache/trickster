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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/labels"
	promengine "github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	limitTestMember  = "m0"
	limitTestHeld    = "held"
	limitTestLimited = "limited"
	limitTestDown    = "down"
	// limitTestALB names the ALB whose member failures the tests count
	limitTestALB = "limit-test"
	// engineEvalEnv is where the engine reports the deadline or cancellation that ends an evaluation
	engineEvalEnv = "expression evaluation"
	// limitTestHost is the host a member's transport error names, which the client never learns
	limitTestHost = "10.0.0.1:9090"
	// limitTestDeadlineName names a subtest by whether the deadline, else the client, ends the evaluation
	limitTestDeadlineName = "/deadline="
	// limitTestDownMetric and limitTestLimitedMetric name the selectors whose reads fail on a member's transport and on a limit
	limitTestDownMetric    = "down_metric"
	limitTestLimitedMetric = "limited_metric"
	limitTestPlus          = " + "
)

var (
	errLimitTest        = fmt.Errorf("%w: more than 1 series", global.ErrFetchLimit)
	errLimitTestStorage = errors.New("block unreadable")
	errLimitTestDown    = errors.New("dial tcp " + limitTestHost + ": connection refused")
	engineTimeout       = promengine.ErrQueryTimeout(engineEvalEnv)
	engineCanceled      = promengine.ErrQueryCanceled(engineEvalEnv)
)

// unavailableFor returns err as the engine reports member's read failing with it
func unavailableFor(member string, err error) error {
	return fmt.Errorf("%w: %s: %w", global.ErrMemberUnavailable, member, err)
}

// limitTestBody is the error a failed evaluation answers with
type limitTestBody struct {
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
}

func decodeLimitTestBody(t *testing.T, w *httptest.ResponseRecorder) limitTestBody {
	t.Helper()
	var body limitTestBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

// quietLimitTest logs nothing while t runs: a synchronous logger starts no goroutine in a test's bubble for the member
// failures it logs
func quietLimitTest(t *testing.T) {
	prev := logger.Logger()
	logger.SetLogger(logging.NoopLogger())
	t.Cleanup(func() { logger.SetLogger(prev) })
}

func limitTestInstant() *http.Request {
	return limitTestInstantQuery(globalTestQuery)
}

func limitTestInstantQuery(query string) *http.Request {
	return globalTestRequest(globalTestQueryPath, url.Values{upQuery: {query}, upTime: {globalTestSeconds(10)}})
}

func memberFailures(member string) float64 {
	return testutil.ToFloat64(metrics.ALBGlobalMemberFailures.WithLabelValues(limitTestALB, member))
}

func TestGlobalErrorTypeLimitOutranksContextEnd(t *testing.T) {
	limitReached := [2]any{http.StatusUnprocessableEntity, global.ErrorTypeExecution}
	timedOut := [2]any{http.StatusServiceUnavailable, global.ErrorTypeTimeout}
	canceled := [2]any{statusClientClosedRequest, global.ErrorTypeCanceled}
	for name, tc := range map[string]struct {
		err  error
		want [2]any
	}{
		"a limit with the deadline":              {errors.Join(context.DeadlineExceeded, errLimitTest), limitReached},
		"a limit with the client's cancellation": {errors.Join(context.Canceled, errLimitTest), limitReached},
		"a member's limit with the engine's timeout": {
			errors.Join(engineTimeout, unavailableFor(limitTestMember, errLimitTest)), limitReached,
		},
		"a member's limit with the engine's cancellation": {
			errors.Join(engineCanceled, unavailableFor(limitTestMember, errLimitTest)), limitReached,
		},
		"a member's limit and deadline": {
			unavailableFor(limitTestMember, errors.Join(errLimitTest, context.DeadlineExceeded)), limitReached,
		},
		"the engine's sample limit with its timeout": {errors.Join(engineTimeout, promengine.ErrTooManySamples("x")), limitReached},
		"a storage deadline beside a limit": {
			errors.Join(promengine.ErrStorage{Err: context.DeadlineExceeded}, errLimitTest), limitReached,
		},
		"a storage limit beside a deadline": {
			errors.Join(promengine.ErrStorage{Err: errLimitTest}, context.DeadlineExceeded), limitReached,
		},
		"the deadline alone":              {context.DeadlineExceeded, timedOut},
		"the engine's timeout alone":      {engineTimeout, timedOut},
		"a member's deadline":             {unavailableFor(limitTestMember, context.DeadlineExceeded), timedOut},
		"a storage deadline":              {promengine.ErrStorage{Err: context.DeadlineExceeded}, timedOut},
		"the client's cancellation alone": {context.Canceled, canceled},
		"the engine's cancellation alone": {engineCanceled, canceled},
		"a storage failure": {
			promengine.ErrStorage{Err: errLimitTestStorage},
			[2]any{http.StatusInternalServerError, global.ErrorTypeInternal},
		},
	} {
		status, errorType := globalErrorType(tc.err)
		require.Equal(t, tc.want, [2]any{status, errorType}, name)
	}
}

func TestGlobalLimitDecides(t *testing.T) {
	limit := unavailableFor(limitTestMember, errLimitTest)
	// with no member read failing on a limit, the evaluation's error stands
	require.Equal(t, engineTimeout, limitDecides(engineTimeout, nil))
	// whatever failure the engine reports first, as the deadline, a cancellation, or another member's or selector's failure,
	// joins the limit a member read reached, which then decides
	for _, err := range []error{
		engineTimeout, engineCanceled, context.DeadlineExceeded, context.Canceled,
		unavailableFor(limitTestDown, errLimitTestStorage), fmt.Errorf("expanding series: %w: %s", global.ErrMemberUnavailable, limitTestDown),
		promengine.ErrStorage{Err: errLimitTestStorage},
		promengine.ErrTooManySamples(engineEvalEnv), errLimitTestStorage,
	} {
		decided := limitDecides(err, &limit)
		require.ErrorIs(t, decided, global.ErrFetchLimit)
		require.ErrorIs(t, decided, err)
		require.Contains(t, decided.Error(), limit.Error())
		status, errorType := globalErrorType(decided)
		require.Equal(t, [2]any{http.StatusUnprocessableEntity, global.ErrorTypeExecution}, [2]any{status, errorType}, err.Error())
	}
	// an error already carrying a limit, as the one observed or another member's, stands as it is
	for _, err := range []error{
		limit, fmt.Errorf("expanding series: %w", unavailableFor(limitTestLimited, errLimitTest)),
		errors.Join(engineTimeout, limit),
		promengine.ErrStorage{Err: errLimitTest},
	} {
		require.Equal(t, err, limitDecides(err, &limit))
	}
}

// contextEndSource holds each read until its context ends, then fails it on a fetch limit when limit is set, as a read
// reaching its limit just as the deadline passes or the client cancels does, or else with its context's end
type contextEndSource struct {
	memberSource
	limit bool
}

func (s contextEndSource) Select(ctx context.Context, _, _ int64, _ *storage.SelectHints, _ ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	<-ctx.Done()
	if s.limit {
		return nil, nil, errLimitTest
	}
	return nil, nil, ctx.Err()
}

func TestGlobalEvaluationLimitOutranksContextEnd(t *testing.T) {
	quietLimitTest(t)
	// each member's read holds until the evaluation's context ends, then fails on a limit when its entry is set
	for name, limits := range map[string][]bool{
		"a limit":                            {true},
		"no limit":                           {false},
		"a later member's limit":             {false, true},
		"an earlier member's limit":          {true, false},
		"no limit on any of several members": {false, false},
	} {
		for _, deadline := range []bool{false, true} {
			t.Run(name+limitTestDeadlineName+strconv.FormatBool(deadline), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var req backends.TSMGlobalRequest
					limited := ""
					for i, limit := range limits {
						member := "m" + strconv.Itoa(i)
						req.Members = append(req.Members, backends.TSMGlobalMember{Name: member, Source: contextEndSource{limit: limit}})
						if limit && limited == "" {
							limited = member
						}
					}
					done := make(chan *httptest.ResponseRecorder, 1)
					go func() {
						done <- serveGlobal(newGlobalEvaluator(globalTestSettings()), limitTestInstant().WithContext(ctx), req)
					}()
					if !deadline {
						// once the reads are held, the client cancels; otherwise the bubble's clock reaches the query timeout
						synctest.Wait()
						cancel()
					}
					w := <-done
					body := decodeLimitTestBody(t, w)
					if limited != "" {
						require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
						require.Equal(t, global.ErrorTypeExecution, body.ErrorType)
						// the answer names the member whose read reached the limit, as it does when nothing else ends the evaluation
						require.Contains(t, body.Error, unavailableFor(limited, errLimitTest).Error())
						return
					}
					status, errorType, engineErr := statusClientClosedRequest, global.ErrorTypeCanceled, error(engineCanceled)
					if deadline {
						status, errorType, engineErr = http.StatusServiceUnavailable, global.ErrorTypeTimeout, engineTimeout
					}
					require.Equal(t, status, w.Code, w.Body.String())
					require.Equal(t, errorType, body.ErrorType)
					require.Equal(t, engineErr.Error(), body.Error, "no member's failure joins the engine's")
				})
			})
		}
	}
}

func TestGlobalEvaluationLimitStopsOtherReads(t *testing.T) {
	quietLimitTest(t)
	for _, partial := range []bool{true, false} {
		for _, limitFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial=%t/limit first=%t", partial, limitFirst), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					settings := globalTestSettings()
					settings.Name, settings.PartialResponse = limitTestALB, partial
					req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{
						{Name: limitTestHeld, Source: contextEndSource{}},
						{Name: limitTestLimited, Source: memberSource{err: errLimitTest}},
					}}
					if limitFirst {
						slices.Reverse(req.Members)
					}
					held, limited := memberFailures(limitTestHeld), memberFailures(limitTestLimited)
					start := time.Now()
					w := serveGlobal(newGlobalEvaluator(settings), limitTestInstant(), req)
					require.Less(t, time.Since(start), settings.QueryTimeout, "the held read stops once the limit decides")
					require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
					require.Contains(t, decodeLimitTestBody(t, w).Error, unavailableFor(limitTestLimited, errLimitTest).Error())
					require.Equal(t, held, memberFailures(limitTestHeld), "the read the limit stopped is no failure of its member")
					require.Equal(t, limited+1, memberFailures(limitTestLimited))
				})
			})
		}
	}
}

func TestGlobalEvaluationLimitOutranksEarlierFailure(t *testing.T) {
	quietLimitTest(t)
	settings := globalTestSettings()
	settings.PartialResponse = false
	for _, limitFirst := range []bool{false, true} {
		// without partial responses, either member's failure fails the query, but the limit decides how
		req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{
			{Name: limitTestDown, Source: memberSource{err: errLimitTestDown}},
			{Name: limitTestLimited, Source: memberSource{err: errLimitTest}},
		}}
		if limitFirst {
			slices.Reverse(req.Members)
		}
		w := serveGlobal(newGlobalEvaluator(settings), limitTestInstant(), req)
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
		body := decodeLimitTestBody(t, w)
		require.Equal(t, global.ErrorTypeExecution, body.ErrorType)
		require.Contains(t, body.Error, unavailableFor(limitTestLimited, errLimitTest).Error())
		require.NotContains(t, w.Body.String(), limitTestHost)
	}
}

func TestGlobalEvaluationDeadlineHidesMemberFailure(t *testing.T) {
	quietLimitTest(t)
	synctest.Test(t, func(t *testing.T) {
		// a partial response leaves out the member that's down, until the deadline ends the other's read
		req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{
			{Name: limitTestDown, Source: memberSource{err: errLimitTestDown}},
			{Name: limitTestHeld, Source: contextEndSource{}},
		}}
		w := serveGlobal(newGlobalEvaluator(globalTestSettings()), limitTestInstant(), req)
		require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		body := decodeLimitTestBody(t, w)
		require.Equal(t, global.ErrorTypeTimeout, body.ErrorType)
		require.Equal(t, engineTimeout.Error(), body.Error)
		require.NotContains(t, w.Body.String(), limitTestHost, "a transport error can name internal hosts")
	})
}

func TestGlobalEvaluationLimitStopsOtherCachedReads(t *testing.T) {
	quietLimitTest(t)
	settings := globalTestSettings()
	settings.QueryTimeout = time.Hour
	c := globalTestCache(t)
	for _, limitFirst := range []bool{false, true} {
		e := newGlobalEvaluator(settings)
		e.raw = global.NewRawCache(c, global.RawCacheOptions{Name: limitTestALB, TTL: time.Hour})
		// members with identities read through the raw cache, held until the limit stops the evaluation's reads
		held := &cancelingSource{ID: limitTestHeld, started: make(chan struct{}, 1)}
		req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{
			{Name: limitTestHeld, Source: held},
			{Name: limitTestLimited, Source: memberSource{
				SourceIdentity: global.SourceIdentity{ID: limitTestLimited}, err: errLimitTest,
			}},
		}}
		if limitFirst {
			slices.Reverse(req.Members)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- serveGlobal(e, limitTestInstant(), req) }()
		var w *httptest.ResponseRecorder
		select {
		case w = <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the held read kept the evaluation waiting")
		}
		require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
		require.Contains(t, decodeLimitTestBody(t, w).Error, unavailableFor(limitTestLimited, errLimitTest).Error())
		e.raw.Wait()
		require.Equal(t, int32(len(held.started)), held.canceled.Load(), "a held read that started was canceled")
	}
}

// selectorSource fails its reads of the selectors whose metric names errs holds, and holds each other read until its
// context ends, as when another member's failure stops it
type selectorSource struct {
	memberSource
	errs map[string]error
}

func (s selectorSource) Select(ctx context.Context, _, _ int64, _ *storage.SelectHints, matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	for _, m := range matchers {
		if err := s.errs[m.Value]; m.Name == labels.MetricName && err != nil {
			return nil, nil, err
		}
	}
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func TestGlobalEvaluationLimitOutranksOtherSelectorFailure(t *testing.T) {
	quietLimitTest(t)
	settings := globalTestSettings()
	settings.Name, settings.PartialResponse = limitTestALB, false
	failures := map[string]error{limitTestDownMetric: errLimitTestDown, limitTestLimitedMetric: errLimitTest}
	for _, query := range []string{
		limitTestDownMetric + limitTestPlus + limitTestLimitedMetric, limitTestLimitedMetric + limitTestPlus + limitTestDownMetric,
	} {
		for _, sameMember := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same member=%t", query, sameMember), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					// one selector's read fails on a member's transport, and another's reaches a limit on the same member or another,
					// so whichever selector the engine expands first, the limit decides
					req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{
						{Name: limitTestDown, Source: selectorSource{errs: map[string]error{limitTestDownMetric: errLimitTestDown}}},
						{Name: limitTestLimited, Source: selectorSource{errs: map[string]error{limitTestLimitedMetric: errLimitTest}}},
					}}
					limited := limitTestLimited
					if sameMember {
						req.Members[0] = backends.TSMGlobalMember{Name: limitTestLimited, Source: selectorSource{errs: failures}}
						req.Members[1] = backends.TSMGlobalMember{Name: limitTestHeld, Source: selectorSource{}}
					}
					down, limits := memberFailures(req.Members[0].Name), memberFailures(limited)
					w := serveGlobal(newGlobalEvaluator(settings), limitTestInstantQuery(query), req)
					require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
					body := decodeLimitTestBody(t, w)
					require.Equal(t, global.ErrorTypeExecution, body.ErrorType)
					require.Contains(t, body.Error, unavailableFor(limited, errLimitTest).Error())
					require.NotContains(t, w.Body.String(), limitTestHost, "a transport error can name internal hosts")
					// both reads that failed count as their members' failures, while the reads they stopped don't
					if sameMember {
						require.Equal(t, limits+2, memberFailures(limited))
						require.Zero(t, memberFailures(limitTestHeld))
						return
					}
					require.Equal(t, down+1, memberFailures(limitTestDown))
					require.Equal(t, limits+1, memberFailures(limitTestLimited))
				})
			})
		}
	}
}
