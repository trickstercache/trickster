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
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus/global"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunks"
	"github.com/prometheus/prometheus/util/annotations"
	"github.com/stretchr/testify/require"
)

const (
	resultLimitALB     = "result-limit-alb"
	resultLimitLabel   = "blob"
	resultLimitClients = 8
	resultLimitBuckets = 40
	// resultLimitPoints is how many minutes of samples, from a cache test's base, each series holds
	resultLimitPoints = 60
	// resultLimitWarning is the limit the failure tests set, and how long their member's warning is before escaping
	resultLimitWarning = 16 << 10
)

// resultLimitStores holds member data whose results are large in different ways, sampled each minute from base
func resultLimitStores(base int64) map[string]presenceStore {
	series := func(lset labels.Labels, hist bool) storage.Series {
		samples := make([]chunks.Sample, resultLimitPoints)
		for i := range samples {
			s := presenceSample{t: base + int64(i)*presenceMinute, f: float64(i) / 7}
			if hist {
				counts := make([]float64, resultLimitBuckets)
				for j := range counts {
					counts[j] = float64(i + j)
				}
				s.fh = &histogram.FloatHistogram{
					Count: float64(i*resultLimitBuckets) + 780, Sum: float64(i) / 3,
					PositiveSpans: []histogram.Span{{Offset: -3, Length: resultLimitBuckets}}, PositiveBuckets: counts,
				}
			}
			samples[i] = s
		}
		return storage.NewListSeries(lset, samples)
	}
	return map[string]presenceStore{
		"samples": recentStore(base),
		"long label": {series(labels.FromStrings(labels.MetricName, presenceMetric,
			resultLimitLabel, strings.Repeat("<long>", 2000)), false)},
		"histograms": {series(labels.FromStrings(labels.MetricName, presenceMetric,
			presencePodLabel, presencePodA), true)},
	}
}

// resultLimitCacheTest returns a cache test of settings reading what src reads, at src's time
func resultLimitCacheTest(t *testing.T, src *globalCacheTest, settings merge.TSMGlobalSettings) *globalCacheTest {
	t.Helper()
	gt := newGlobalCacheTest(t, settings, nil)
	gt.baseMillis, gt.source.store = src.baseMillis, src.source.store
	return gt
}

// globalTestError is a decoded Prometheus API error response
type globalTestError struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Warnings  []string        `json:"warnings"`
	Data      json.RawMessage `json:"data"`
}

// decodeGlobalError decodes an error response, requiring it to be complete
func decodeGlobalError(t *testing.T, body []byte) globalTestError {
	t.Helper()
	var resp globalTestError
	require.NoError(t, json.Unmarshal(body, &resp), "a complete error, never part of a result: %s", body)
	require.Equal(t, "error", resp.Status)
	require.Nil(t, resp.Data)
	return resp
}

// requireResultTooLarge requires w to be the complete, uncacheable error of a result exceeding limit bytes
func requireResultTooLarge(t *testing.T, w *httptest.ResponseRecorder, limit int64) {
	t.Helper()
	require.Equal(t, http.StatusUnprocessableEntity, w.Code, w.Body.String())
	require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl))
	resp := decodeGlobalError(t, w.Body.Bytes())
	require.Equal(t, global.ErrorTypeExecution, resp.ErrorType)
	require.Equal(t, global.ResultLimitError(limit).Error(), resp.Error)
}

func TestGlobalEvaluationResultLimit(t *testing.T) {
	// a result as large as max_capture_bytes is answered and cached; one byte larger fails whole, uncached, whether
	// it is evaluated directly or through the cache
	executions := func() float64 {
		return testutil.ToFloat64(metrics.ALBGlobalEvaluations.WithLabelValues(resultLimitALB, global.ErrorTypeExecution))
	}
	for name := range resultLimitStores(0) {
		t.Run(name, func(t *testing.T) {
			settings := globalTestSettings()
			settings.Name = resultLimitALB
			gt := newGlobalCacheTest(t, settings, nil)
			gt.source.store = resultLimitStores(gt.baseMillis)[name]
			values := gt.rangeValues(presenceMetric, 0, resultLimitPoints-1)
			full := gt.uncached(t, settings, globalTestRangePath, values)
			require.Equal(t, http.StatusOK, full.Code, full.Body.String())
			size := int64(full.Body.Len())
			at, below := settings, settings
			at.MaxResponseBytes, below.MaxResponseBytes = size, size-1

			w := gt.uncached(t, at, globalTestRangePath, values)
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, full.Body.String(), w.Body.String())
			before := executions()
			w = gt.uncached(t, below, globalTestRangePath, values)
			requireResultTooLarge(t, w, below.MaxResponseBytes)
			require.Empty(t, w.Header().Get(headers.NameTricksterResult), "no evaluation result is reported")
			require.InDelta(t, before+1, executions(), 0, "the evaluation fails as for any limit")

			cached := resultLimitCacheTest(t, gt, below)
			for range 2 {
				w = cached.do(t, http.MethodGet, globalTestRangePath, values, nil)
				requireResultTooLarge(t, w, below.MaxResponseBytes)
				requireCacheStatus(t, w, status.StatusKeyMiss)
			}
			cached = resultLimitCacheTest(t, gt, at)
			w = cached.do(t, http.MethodGet, globalTestRangePath, values, nil)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, full.Body.String(), w.Body.String())
			requireCacheStatus(t, w, status.StatusKeyMiss)
			w = cached.do(t, http.MethodGet, globalTestRangePath, values, nil)
			requireCacheStatus(t, w, status.StatusHit)
			require.Equal(t, full.Body.String(), w.Body.String())
		})
	}
}

func TestGlobalEvaluationResultLimitConcurrentMisses(t *testing.T) {
	settings := globalTestSettings()
	gt := newGlobalCacheTest(t, settings, nil)
	gt.source.store = resultLimitStores(gt.baseMillis)["histograms"]
	values := gt.rangeValues(presenceMetric, 0, resultLimitPoints-1)
	full := gt.uncached(t, settings, globalTestRangePath, values)
	require.Equal(t, http.StatusOK, full.Code, full.Body.String())
	size := int64(full.Body.Len())
	for _, limit := range []int64{size - 1, size} {
		limited := settings
		limited.MaxResponseBytes = limit
		cached := resultLimitCacheTest(t, gt, limited)
		responses := make([]*httptest.ResponseRecorder, resultLimitClients)
		var wg sync.WaitGroup
		for i := range responses {
			wg.Go(func() { responses[i] = cached.do(t, http.MethodGet, globalTestRangePath, values, nil) })
		}
		wg.Wait()
		for _, w := range responses {
			if limit < size {
				requireResultTooLarge(t, w, limit)
				continue
			}
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.Equal(t, full.Body.String(), w.Body.String())
		}
		want := status.StatusHit
		if limit < size {
			want = status.StatusKeyMiss
		}
		requireCacheStatus(t, cached.do(t, http.MethodGet, globalTestRangePath, values, nil), want)
	}
}

func TestGlobalCachedResultsFollowTheResultLimit(t *testing.T) {
	// a result kept under one limit is never served under a lower one, as after a reload lowers it, or by another ALB sharing
	// the cache and namespace; an equal limit is served it
	settings := globalTestSettings()
	shared := globalTestCache(t)
	gt := newGlobalCacheTest(t, settings, shared)
	gt.source.store = resultLimitStores(gt.baseMillis)["samples"]
	// limited returns a cache test of the result limit sharing gt's cache, namespace and member data
	limited := func(limit int64) *globalCacheTest {
		s := settings
		s.MaxResponseBytes = limit
		lt := newGlobalCacheTest(t, s, shared)
		lt.baseMillis, lt.source.store = gt.baseMillis, gt.source.store
		return lt
	}
	for name, query := range map[string]struct {
		path   string
		values url.Values
	}{
		"range":   {globalTestRangePath, gt.rangeValues(presenceMetric, 0, resultLimitPoints-1)},
		"instant": {globalTestQueryPath, gt.instantValues(presenceMetric, resultLimitPoints-1)},
	} {
		t.Run(name, func(t *testing.T) {
			full := gt.uncached(t, settings, query.path, query.values)
			require.Equal(t, http.StatusOK, full.Code, full.Body.String())
			size := int64(full.Body.Len())
			do := func(lt *globalCacheTest) *httptest.ResponseRecorder {
				return lt.do(t, http.MethodGet, query.path, query.values, nil)
			}
			requireResult := func(w *httptest.ResponseRecorder, want status.Label) {
				t.Helper()
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Equal(t, full.Body.String(), w.Body.String())
				requireCacheStatus(t, w, want)
			}
			higher := limited(size + 1)
			requireResult(do(higher), status.StatusKeyMiss)
			requireResult(do(higher), status.StatusHit)
			lower := limited(size - 1)
			for range 2 {
				w := do(lower)
				requireResultTooLarge(t, w, size-1)
				requireCacheStatus(t, w, status.StatusKeyMiss)
			}
			require.Equal(t, 1, higher.source.reads())
			require.Equal(t, 2, lower.source.reads(), "the result kept under the higher limit was never served")
			requireResult(do(limited(size+1)), status.StatusHit)
			at := limited(size)
			requireResult(do(at), status.StatusKeyMiss)
			requireResult(do(at), status.StatusHit)
		})
	}
}

func roundTripBody(t *testing.T, tr inProcessTransport) (*http.Response, string) {
	t.Helper()
	resp, err := tr.RoundTrip(httptest.NewRequest(http.MethodGet, "/", nil))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, int64(len(body)), resp.ContentLength)
	return resp, string(body)
}

func TestInProcessTransportLimit(t *testing.T) {
	const limit = 8
	writes := func(parts ...string) inProcessTransport {
		return inProcessTransport{maxBytes: limit, handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headers.NameCacheControl, sharedMaxAge(instantSharedMaxAge))
			for _, p := range parts {
				_, _ = w.Write([]byte(p))
			}
		})}
	}
	resp, body := roundTripBody(t, writes("1234", "5678"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "12345678", body)

	// the handler's answer, headers and all, is replaced by the failure
	resp, body = roundTripBody(t, writes("1234", "56789", "0"))
	w := httptest.NewRecorder()
	maps.Copy(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.WriteString(body)
	requireResultTooLarge(t, w, limit)

	// a failure bounds itself, so it keeps its status and body, whatever their size
	fails := inProcessTransport{maxBytes: limit, handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("1234"))
		_, _ = w.Write([]byte("56789"))
	})}
	resp, body = roundTripBody(t, fails)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, "123456789", body)
}

// trackingBody reports whether it was closed
type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

func TestBufferedResponseKeepsBody(t *testing.T) {
	value := promql.Vector{{Metric: labels.FromStrings(presencePodLabel, presencePodA), T: 1000, F: 1}}
	want := httptest.NewRecorder()
	require.NoError(t, global.WriteResult(want, value, nil, nil, 0))

	b := &bufferedResponse{header: make(http.Header)}
	require.NoError(t, global.WriteResult(b, value, nil, nil, 0))
	require.NotNil(t, b.kept, "a result is kept, not copied")
	require.Zero(t, b.body.Len())
	require.Equal(t, want.Body.Len(), b.keptSize)
	require.Equal(t, http.StatusOK, b.status)
	_, err := b.Write([]byte("!"))
	require.NoError(t, err)
	require.Nil(t, b.kept)
	require.Equal(t, want.Body.String()+"!", b.body.String(), "what follows a kept body is written after it")

	b = &bufferedResponse{header: make(http.Header)}
	b.KeepBody(&trackingBody{Reader: strings.NewReader("ok")}, 2)
	require.Equal(t, http.StatusOK, b.status, "a body kept without a status is a success's")
	require.Equal(t, 2, b.keptSize)

	b = &bufferedResponse{header: make(http.Header)}
	_, _ = b.Write([]byte("a"))
	tb := &trackingBody{Reader: strings.NewReader("bc")}
	b.KeepBody(tb, 2)
	require.Equal(t, "abc", b.body.String(), "a body kept after a write is written after it")
	require.True(t, tb.closed)
	require.Nil(t, b.kept)

	b = &bufferedResponse{header: make(http.Header), limit: 3}
	tb = &trackingBody{Reader: strings.NewReader("abcd")}
	b.KeepBody(tb, 4)
	require.True(t, b.overflow)
	require.True(t, tb.closed)
	require.Nil(t, b.kept)
	n, err := b.Write([]byte("a"))
	require.Zero(t, n)
	require.ErrorIs(t, err, global.ErrResultTooLarge, "an answer past its limit stays failed")

	// through the transport, a kept result is the response's body, and one past its limit fails whole
	keeps := inProcessTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = global.WriteResult(w, value, nil, nil, 0)
	})}
	resp, body := roundTripBody(t, keeps)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, want.Body.String(), body)
	keeps.maxBytes = int64(want.Body.Len() - 1)
	resp, body = roundTripBody(t, keeps)
	require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(t, body, global.ErrResultTooLarge.Error())
}

func TestGlobalEvaluationFailuresAreNoStore(t *testing.T) {
	// every failed evaluation answers no-store, directly or through the cache, which passes failures on without one
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	invalid := gt.rangeValues(globalTestQuery, 0, 20)
	invalid.Set(upStep, "0s")
	// a query that doesn't parse is found invalid only as it is evaluated, behind the cache
	malformed := gt.rangeValues(globalTestQuery, 0, 20)
	malformed.Set(upQuery, "sum(")
	timeout := gt.rangeValues(globalTestQuery, 0, 20)
	timeout.Set(timeoutParam, "0.000000001")
	for name, tc := range map[string]struct {
		values    url.Values
		status    int
		errorType string
	}{
		"invalid":   {invalid, http.StatusBadRequest, global.ErrorTypeBadData},
		"malformed": {malformed, http.StatusBadRequest, global.ErrorTypeBadData},
		"timeout":   {timeout, http.StatusServiceUnavailable, global.ErrorTypeTimeout},
	} {
		for _, w := range []*httptest.ResponseRecorder{
			gt.uncached(t, globalTestSettings(), globalTestRangePath, tc.values),
			gt.do(t, http.MethodGet, globalTestRangePath, tc.values, nil),
		} {
			require.Equal(t, tc.status, w.Code, name)
			require.Equal(t, tc.errorType, decodeGlobalError(t, w.Body.Bytes()).ErrorType, name)
			require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl), name)
		}
	}
	w := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEqual(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl))
}

func TestGlobalEvaluationFailureFitsResultLimit(t *testing.T) {
	// a failure keeps within the result's limit, leaving out the warnings that don't fit and cutting its message short,
	// so even one answering a result too large for its warnings fits, and the cache passes on its own status and type
	warning := strings.Repeat("&", resultLimitWarning)
	for _, partial := range []bool{true, false} {
		settings := globalTestSettings()
		settings.Name, settings.PartialResponse, settings.MaxResponseBytes = resultLimitALB, partial, resultLimitWarning
		gt := newGlobalCacheTest(t, settings, nil)
		gt.source.warnings = annotations.New().Add(errors.New(warning))
		values := gt.rangeValues(presenceMetric, 0, resultLimitPoints-1)
		unlimited := settings
		unlimited.MaxResponseBytes = 0
		w := gt.uncached(t, unlimited, globalTestRangePath, values)
		require.Greater(t, w.Body.Len(), resultLimitWarning, "unlimited, the answer carries the whole warning")
		require.Contains(t, w.Body.String(), "incomplete answer")
		// the member's warning alone takes a partial result past the limit, and fails a strict evaluation
		status, errorType := http.StatusUnprocessableEntity, global.ErrorTypeExecution
		if !partial {
			status, errorType = http.StatusServiceUnavailable, global.ErrorTypeUnavailable
		}
		require.Equal(t, map[bool]int{true: http.StatusOK, false: status}[partial], w.Code, w.Body.String())
		recorded := func() float64 {
			return testutil.ToFloat64(metrics.ALBGlobalEvaluations.WithLabelValues(resultLimitALB, errorType))
		}
		for path, answer := range map[string]func() *httptest.ResponseRecorder{
			"direct": func() *httptest.ResponseRecorder { return gt.uncached(t, settings, globalTestRangePath, values) },
			"cached": func() *httptest.ResponseRecorder {
				return gt.do(t, http.MethodGet, globalTestRangePath, values, nil)
			},
		} {
			before := recorded()
			w := answer()
			require.Equal(t, status, w.Code, "%s: %s", path, w.Body.String())
			require.LessOrEqual(t, w.Body.Len(), resultLimitWarning, path)
			require.Equal(t, headers.ValueNoStore, w.Header().Get(headers.NameCacheControl), path)
			resp := decodeGlobalError(t, w.Body.Bytes())
			require.Equal(t, errorType, resp.ErrorType, path)
			require.InDelta(t, before+1, recorded(), 0, "%s: the answer is the failure recorded", path)
			if partial {
				require.Equal(t, global.ResultLimitError(resultLimitWarning).Error(), resp.Error, path)
				require.Empty(t, resp.Warnings, "%s: the warning that doesn't fit is left out", path)
			} else {
				require.Contains(t, resp.Error, "incomplete answer: &&&", path)
			}
		}
	}
}

// endTrackingBody records reading its body to its end
type endTrackingBody struct {
	io.ReadCloser
	ended *atomic.Bool
}

func (b endTrackingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.ended.Store(true)
	}
	return n, err
}

// endTrackingTransport counts its responses and records whether their bodies were read to their ends
type endTrackingTransport struct {
	next      http.RoundTripper
	responses atomic.Int32
	ended     atomic.Bool
}

func (tr *endTrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := tr.next.RoundTrip(r)
	if resp != nil {
		tr.responses.Add(1)
		resp.Body = endTrackingBody{ReadCloser: resp.Body, ended: &tr.ended}
	}
	return resp, err
}

func TestGlobalCachedMissReadsResultToEnd(t *testing.T) {
	// the cache reads an evaluated result to its end, which returns the result's buffer for reuse, though it never
	// closes the result when it forwards it as it does by default
	gt := newGlobalCacheTest(t, globalTestSettings(), nil)
	client := gt.evaluator.cached.base.HTTPClient
	tr := &endTrackingTransport{next: client.Transport}
	client.Transport = tr
	w := gt.do(t, http.MethodGet, globalTestRangePath, gt.rangeValues(globalTestQuery, 0, 20), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	requireCacheStatus(t, w, status.StatusKeyMiss)
	require.EqualValues(t, 1, tr.responses.Load())
	require.True(t, tr.ended.Load(), "a miss reads its result to the end")
}
