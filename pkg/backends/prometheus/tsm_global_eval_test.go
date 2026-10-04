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
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	tsmql "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/promql"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/labels"
	promengine "github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	globalTestQueryPath = APIPath + mnQuery
	globalTestRangePath = APIPath + mnQueryRange
	globalTestQuery     = "sum(x)"
)

var errGlobalTestMember = errors.New("member down")

// memberSource serves a member's in-memory series as a raw series source
type memberSource struct {
	store    presenceStore
	err      error
	warnings annotations.Annotations
}

func (memberSource) SeriesAPI() string { return po.SeriesAPIRemoteRead }

func (m memberSource) Select(_ context.Context, _, _ int64, _ *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	if m.err != nil {
		return nil, nil, m.err
	}
	var out []storage.Series
	for _, s := range m.store {
		lset := s.Labels()
		if !slices.ContainsFunc(matchers, func(m *labels.Matcher) bool { return !m.Matches(lset.Get(m.Name)) }) {
			out = append(out, s)
		}
	}
	return out, m.warnings, nil
}

func globalTestSettings() merge.TSMGlobalSettings {
	return merge.TSMGlobalSettings{
		Deduplication: merge.TSMDeduplicationChain, PartialResponse: true,
		MaxSamples: 50_000_000, QueryTimeout: time.Minute,
	}
}

func globalTestMembers(stores ...storage.Queryable) backends.TSMGlobalRequest {
	var req backends.TSMGlobalRequest
	for i, s := range stores {
		req.Members = append(req.Members, backends.TSMGlobalMember{
			Name: "m" + strconv.Itoa(i), Source: memberSource{store: s.(presenceStore)},
		})
	}
	return req
}

func globalTestSeconds(minute int) string {
	return strconv.FormatFloat(float64(presenceMillis(minute))/1000, 'f', -1, 64)
}

func globalTestRequest(path string, values url.Values) *http.Request {
	return httptest.NewRequest(http.MethodGet, path+"?"+values.Encode(), nil)
}

func serveGlobal(e *globalEvaluator, r *http.Request, req backends.TSMGlobalRequest) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	e.ServeTSMGlobal(w, r, req)
	return w
}

// globalOracle evaluates query over the union store with the evaluator's engine settings, writing
// the API response the evaluator should write
func globalOracle(t *testing.T, union storage.Queryable, query string, ev presenceEval) string {
	t.Helper()
	eng := promengine.NewEngine(promengine.EngineOpts{
		MaxSamples: 50_000_000, Timeout: time.Minute, EnableAtModifier: true, EnableNegativeOffset: true,
		NoStepSubqueryIntervalFn: func(int64) int64 { return globalSubqueryStep.Milliseconds() },
		Parser:                   parser.NewParser(tsmql.ParserOptions()),
	})
	ctx := context.Background()
	var qry promengine.Query
	var err error
	if ev.step == 0 {
		qry, err = eng.NewInstantQuery(ctx, union, nil, query, time.UnixMilli(presenceMillis(ev.at)))
	} else {
		qry, err = eng.NewRangeQuery(ctx, union, nil, query, time.UnixMilli(presenceMillis(ev.start)),
			time.UnixMilli(presenceMillis(ev.end)), time.Duration(ev.step)*time.Minute)
	}
	require.NoError(t, err, query)
	defer qry.Close()
	res := qry.Exec(ctx)
	require.NoError(t, res.Err, query)
	warnings, infos := res.Warnings.AsStrings(query, 0, 0)
	slices.Sort(warnings)
	slices.Sort(infos)
	w := httptest.NewRecorder()
	global.WriteResult(w, res.Value, warnings, infos)
	return w.Body.String()
}

// sortedResult re-encodes an API response with an instant vector's samples in metric order, which
// Prometheus doesn't guarantee for aggregations
func sortedResult(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
		Warnings []string `json:"warnings"`
		Infos    []string `json:"infos"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	if resp.Data.ResultType == string(parser.ValueTypeVector) {
		slices.SortFunc(resp.Data.Result, func(a, b json.RawMessage) int { return strings.Compare(string(a), string(b)) })
	}
	out, err := json.Marshal(resp)
	require.NoError(t, err)
	return string(out)
}

var globalOracleQueries = []string{
	"x",
	globalTestQuery,
	"count by (ns) (x)",
	"count(count by (pod) (x))",
	"rate(x[5m])",
	"sum(rate(x[5m]))",
	"max_over_time(x[10m])",
	"x offset 5m",
	"max_over_time(sum(x)[15m:2m])",
	"topk(1, count by (pod) (x))",
	"scalar(count(x))",
	"histogram_count(x)",
	`{__name__=~"x|y"}`,
	`count_values("v", count by (ns) (x))`,
}

func TestGlobalEvaluationMatchesPrometheus(t *testing.T) {
	e := newGlobalEvaluator(globalTestSettings())
	for _, topology := range presenceTopologies {
		t.Run(topology.name, func(t *testing.T) {
			members, union := presenceStorages(t, topology)
			req := globalTestMembers(members...)
			for _, query := range globalOracleQueries {
				for _, ev := range presenceEvals {
					values := url.Values{upQuery: {query}}
					path := globalTestQueryPath
					if ev.step == 0 {
						values.Set(upTime, globalTestSeconds(ev.at))
					} else {
						path = globalTestRangePath
						values.Set(upStart, globalTestSeconds(ev.start))
						values.Set(upEnd, globalTestSeconds(ev.end))
						values.Set(upStep, strconv.Itoa(ev.step*60))
					}
					w := serveGlobal(e, globalTestRequest(path, values), req)
					require.Equal(t, http.StatusOK, w.Code, "%s at %+v: %s", query, ev, w.Body.String())
					require.Equal(t, sortedResult(t, globalOracle(t, union, query, ev)), sortedResult(t, w.Body.String()),
						"%s at %+v", query, ev)
					require.Equal(t, globalResultHeader, w.Header().Get(headers.NameTricksterResult))
				}
			}
		})
	}
}

func TestGlobalEvaluationErrors(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	healthy := globalTestMembers(members...)
	instant := url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}
	with := func(values url.Values, key, value string) url.Values {
		out := url.Values{}
		for k, v := range values {
			out[k] = slices.Clone(v)
		}
		out.Set(key, value)
		return out
	}
	unavailable := healthy
	unavailable.Unavailable = []backends.TSMGlobalUnavailable{{Name: "down"}, {Name: "broken", Reason: errGlobalTestMember}}
	failing := globalTestMembers(members...)
	failing.Members = append(failing.Members, backends.TSMGlobalMember{
		Name:   "failing",
		Source: memberSource{err: errGlobalTestMember},
	})
	strict := globalTestSettings()
	strict.PartialResponse = false
	tests := []struct {
		name     string
		settings merge.TSMGlobalSettings
		path     string
		values   url.Values
		req      backends.TSMGlobalRequest
		status   int
		contains string
	}{
		{
			name: "missing query", path: globalTestQueryPath, values: url.Values{}, req: healthy,
			status: http.StatusBadRequest, contains: global.ErrorTypeBadData,
		},
		{
			name: "parse error", path: globalTestQueryPath, values: with(instant, upQuery, "sum("), req: healthy,
			status: http.StatusBadRequest, contains: global.ErrorTypeBadData,
		},
		{
			name: "bad time", path: globalTestQueryPath, values: with(instant, upTime, "soon"), req: healthy,
			status: http.StatusBadRequest,
		},
		{
			name: "bad timeout", path: globalTestQueryPath, values: with(instant, timeoutParam, "never"), req: healthy,
			status: http.StatusBadRequest,
		},
		{
			name: "bad start", path: globalTestRangePath, values: url.Values{upQuery: {globalTestQuery}, upStart: {"x"}},
			req: healthy, status: http.StatusBadRequest,
		},
		{name: "bad end", path: globalTestRangePath, values: url.Values{
			upQuery: {globalTestQuery},
			upStart: {globalTestSeconds(0)}, upEnd: {"x"},
		}, req: healthy, status: http.StatusBadRequest},
		{
			name: "bad step", path: globalTestRangePath, values: url.Values{
				upQuery: {globalTestQuery},
				upStart: {globalTestSeconds(0)}, upEnd: {globalTestSeconds(1)}, upStep: {"0"},
			}, req: healthy,
			status: http.StatusBadRequest,
		},
		{
			name: "end before start", path: globalTestRangePath, values: url.Values{
				upQuery: {globalTestQuery},
				upStart: {globalTestSeconds(1)}, upEnd: {globalTestSeconds(0)}, upStep: {"60"},
			}, req: healthy,
			status: http.StatusBadRequest, contains: "before start",
		},
		{
			name: "too many points", path: globalTestRangePath, values: url.Values{
				upQuery: {globalTestQuery},
				upStart: {globalTestSeconds(0)}, upEnd: {globalTestSeconds(1000)}, upStep: {"1"},
			}, req: healthy,
			status: http.StatusBadRequest, contains: "maximum resolution",
		},
		{
			name: "strict unavailable", settings: strict, path: globalTestQueryPath, values: instant, req: unavailable,
			// the reason a configured member can't be read is logged, not returned
			status: http.StatusServiceUnavailable, contains: `"error":"tsm global evaluation member unavailable: down; broken"}`,
		},
		{
			name: "partial override", settings: strict, path: globalTestQueryPath,
			values: with(instant, partialResponseParam, "true"), req: unavailable, status: http.StatusOK,
			contains: `"warnings":["tsm global evaluation member unavailable: down"`,
		},
		{
			name: "strict failing member", settings: strict, path: globalTestQueryPath, values: instant, req: failing,
			status: http.StatusServiceUnavailable, contains: global.ErrorTypeUnavailable,
		},
		{
			name: "partial failing member", path: globalTestQueryPath, values: instant, req: failing,
			status: http.StatusOK, contains: `"warnings":["tsm global evaluation member unavailable: failing"]`,
		},
		{
			name: "no members", path: globalTestQueryPath, values: instant, req: backends.TSMGlobalRequest{},
			status: http.StatusServiceUnavailable,
		},
		{
			name: "unreadable source", path: globalTestQueryPath, values: instant,
			req:    backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: "plain"}}},
			status: http.StatusServiceUnavailable, contains: "plain",
		},
		{
			name: "too many samples", settings: merge.TSMGlobalSettings{MaxSamples: 1, QueryTimeout: time.Minute},
			path: globalTestRangePath, values: url.Values{
				upQuery: {"x"}, upStart: {globalTestSeconds(0)},
				upEnd: {globalTestSeconds(30)}, upStep: {"60"},
			}, req: healthy,
			status: http.StatusUnprocessableEntity, contains: global.ErrorTypeExecution,
		},
		{name: "fetch limit", path: globalTestQueryPath, values: instant, req: backends.TSMGlobalRequest{
			Members: []backends.TSMGlobalMember{{Name: "limited", Source: memberSource{err: global.ErrFetchLimit}}},
		}, status: http.StatusUnprocessableEntity, contains: "raw data limit"},
		{
			name: "timeout", path: globalTestQueryPath, values: with(instant, timeoutParam, "0.000000001"), req: healthy,
			status: http.StatusServiceUnavailable, contains: global.ErrorTypeTimeout,
		},
		{
			name: "limit", path: globalTestQueryPath, values: with(with(instant, upQuery, "x"), upLimit, "1"),
			req: healthy, status: http.StatusOK, contains: globalLimitWarning,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := test.settings
			if settings.QueryTimeout == 0 {
				settings = globalTestSettings()
			}
			w := serveGlobal(newGlobalEvaluator(settings), globalTestRequest(test.path, test.values), test.req)
			require.Equal(t, test.status, w.Code, w.Body.String())
			require.Contains(t, w.Body.String(), test.contains)
		})
	}
}

func TestGlobalEvaluationPartialHeader(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	req := globalTestMembers(members...)
	req.Members = append(req.Members, backends.TSMGlobalMember{Name: "failing", Source: memberSource{err: errGlobalTestMember}})
	w := serveGlobal(newGlobalEvaluator(globalTestSettings()), globalTestRequest(globalTestQueryPath,
		url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}), req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, globalPartialResultHeader, w.Header().Get(headers.NameTricksterResult))
}

func TestGlobalEvaluatorHandles(t *testing.T) {
	e, err := (&Client{}).NewTSMGlobalEvaluator(globalTestSettings(), backends.TSMGlobalCache{})
	require.NoError(t, err)
	for path, want := range map[string]bool{
		globalTestQueryPath: true, globalTestRangePath: true, "/prefix" + globalTestRangePath: true,
		APIPath + mnSeries: false, APIPath + mnLabels: false,
	} {
		require.Equal(t, want, e.Handles(httptest.NewRequest(http.MethodGet, path, nil)), path)
	}
}

func TestGlobalErrorType(t *testing.T) {
	for err, want := range map[error]string{
		context.Canceled: global.ErrorTypeCanceled,
		promengine.ErrStorage{Err: errGlobalTestMember}:  global.ErrorTypeInternal,
		promengine.ErrStorage{Err: global.ErrFetchLimit}: global.ErrorTypeExecution,
		errGlobalTestMember:                              global.ErrorTypeExecution,
	} {
		_, got := globalErrorType(err)
		require.Equal(t, want, got, err.Error())
	}
	value, truncated := limitSeries(promengine.Scalar{}, 1)
	require.False(t, truncated)
	require.Equal(t, promengine.Scalar{}, value)
	vector, truncated := limitSeries(promengine.Vector{{}, {}}, 1)
	require.True(t, truncated)
	require.Len(t, vector, 1)
	_, truncated = limitSeries(promengine.Matrix{{}}, 1)
	require.False(t, truncated)
	require.True(t, strings.HasPrefix(globalPartialResultHeader, "engine=ALB"))
}

// blockingSource holds every select until released, signaling each one it holds
type blockingSource struct {
	memberSource
	held    chan struct{}
	release chan struct{}
}

func (b blockingSource) Select(ctx context.Context, mint, maxt int64, hints *storage.SelectHints,
	matchers ...*labels.Matcher,
) ([]storage.Series, annotations.Annotations, error) {
	b.held <- struct{}{}
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return b.memberSource.Select(ctx, mint, maxt, hints, matchers...)
}

func TestGlobalEvaluationConcurrency(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	source := blockingSource{
		store: members[0].(presenceStore),
		held:  make(chan struct{}, 1), release: make(chan struct{}),
	}
	req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: "slow", Source: source}}}
	settings := globalTestSettings()
	settings.MaxConcurrentQueries = 1
	e := newGlobalEvaluator(settings)
	instant := url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}

	first := make(chan *httptest.ResponseRecorder)
	go func() { first <- serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), req) }()
	<-source.held
	waiting := url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}, timeoutParam: {"0.05"}}
	w := serveGlobal(e, globalTestRequest(globalTestQueryPath, waiting), req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Body.String(), "evaluation slot")
	close(source.release)
	require.Equal(t, http.StatusOK, (<-first).Code)
}

func TestGlobalEvaluationMetrics(t *testing.T) {
	const name = "metrics-alb"
	members, _ := presenceStorages(t, presenceTopologies[0])
	req := globalTestMembers(members...)
	failing := globalTestMembers(members...)
	failing.Members = append(failing.Members, backends.TSMGlobalMember{
		Name:   "failing",
		Source: memberSource{err: errGlobalTestMember},
	})
	settings := globalTestSettings()
	settings.Name = name
	e := newGlobalEvaluator(settings)
	instant := url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}
	count := func(outcome string) float64 {
		return testutil.ToFloat64(metrics.ALBGlobalEvaluations.WithLabelValues(name, outcome))
	}
	before := map[string]float64{
		globalOutcomeSuccess: count(globalOutcomeSuccess), globalOutcomePartial: count(globalOutcomePartial),
		global.ErrorTypeBadData: count(global.ErrorTypeBadData),
	}
	failures := testutil.ToFloat64(metrics.ALBGlobalMemberFailures.WithLabelValues(name, "failing"))
	serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), req)
	serveGlobal(e, globalTestRequest(globalTestQueryPath, instant), failing)
	serveGlobal(e, globalTestRequest(globalTestQueryPath, url.Values{}), req)
	for outcome, n := range before {
		require.InDelta(t, n+1, count(outcome), 0, outcome)
	}
	require.InDelta(t, failures+1, testutil.ToFloat64(metrics.ALBGlobalMemberFailures.WithLabelValues(name, "failing")), 0)
	require.Positive(t, testutil.CollectAndCount(metrics.ALBGlobalEvaluationDuration))
}

// globalLogBuffer is a log destination safe for the logger's concurrent writes
type globalLogBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *globalLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *globalLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestGlobalMemberFailureLogs(t *testing.T) {
	const (
		name     = "logs-alb"
		detail   = "dial tcp internal-host:9090: connection refused"
		canceled = "canceled-member"
	)
	logs := &globalLogBuffer{}
	prev := logger.Logger()
	logger.SetLogger(logging.StreamLogger(logs, level.Warn))
	t.Cleanup(func() { logger.SetLogger(prev) })
	settings := globalTestSettings()
	settings.Name = name
	e := newGlobalEvaluator(settings)
	e.memberFailed(canceled, context.Canceled)
	e.memberFailed("down", errors.New(detail))
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), detail) }, 5*time.Second, 10*time.Millisecond)
	require.NotContains(t, logs.String(), canceled, "a client's cancellation isn't the member's failure to log")
	require.InDelta(t, 1, testutil.ToFloat64(metrics.ALBGlobalMemberFailures.WithLabelValues(name, canceled)), 0)

	unreadable := backends.TSMGlobalRequest{Unavailable: []backends.TSMGlobalUnavailable{
		{Name: "misconfigured", Reason: errors.New("open /etc/certs/member.pem: no such file")},
	}}
	_, warnings, err := globalMembers(name, unreadable, true)
	require.ErrorIs(t, err, errGlobalNoMembers)
	require.Equal(t, []string{global.ErrMemberUnavailable.Error() + ": misconfigured"}, warnings)
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "member.pem") }, 5*time.Second,
		10*time.Millisecond)
}

func TestGlobalEvaluationClientCancel(t *testing.T) {
	members, _ := presenceStorages(t, presenceTopologies[0])
	source := blockingSource{
		store: members[0].(presenceStore),
		held:  make(chan struct{}, 1), release: make(chan struct{}),
	}
	req := backends.TSMGlobalRequest{Members: []backends.TSMGlobalMember{{Name: "slow", Source: source}}}
	ctx, cancel := context.WithCancel(context.Background())
	r := globalTestRequest(globalTestQueryPath, url.Values{upQuery: {globalTestQuery}, upTime: {globalTestSeconds(10)}}).
		WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- serveGlobal(newGlobalEvaluator(globalTestSettings()), r, req) }()
	<-source.held
	cancel()
	w := <-done
	require.Equal(t, statusClientClosedRequest, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), global.ErrorTypeCanceled)
}
