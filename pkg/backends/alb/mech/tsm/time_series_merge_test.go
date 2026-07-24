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
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

var testLogger = logging.NoopLogger()

func TestHandleResponseMergeNilPool(t *testing.T) {
	h := &handler{}
	w := httptest.NewRecorder()
	r := albpool.NewParentGET(t)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
	}
}

func TestHandleResponseMerge(t *testing.T) {
	logger.SetLogger(testLogger)
	r := albpool.NewParentGET(t)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	r = request.SetResources(r, rsc)

	p, _, _ := albpool.New(0, nil)
	defer p.Stop()
	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	p, _, _ = albpool.NewHealthy(
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
	defer p.Stop()
	h.SetPool(p)
	albpool.WaitHealthy(t, p, 1)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	p, _, _ = albpool.NewHealthy(
		[]http.Handler{
			http.HandlerFunc(tu.BasicHTTPHandler),
			http.HandlerFunc(tu.BasicHTTPHandler),
		})
	defer p.Stop()
	h.SetPool(p)
	albpool.WaitHealthy(t, p, 2)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	w = httptest.NewRecorder()
	h.mergePaths = nil
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}
}

func TestTSMNonMergePathUsesFirstLiveMember(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{
		albpool.NamedHandler("first"),
		albpool.NamedHandler("second"),
	})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{mergePaths: []string{"/api/v1/query_range"}}
	h.SetPool(p)

	for range 3 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "https://trickstercache.org/api/v1/query?query=up", nil)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if got := w.Body.String(); got != "first" {
			t.Fatalf("body = %q, want first", got)
		}
	}
}

// A panicking pool member must not crash the request. RecoverFanoutPanic("tsm",
// ...) at time_series_merge.go must catch it and mark the slot failed so the
// merge surfaces the partial-failure (phit) signal.
func TestTSMPanicMemberDoesNotCrashRequest(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{
		http.HandlerFunc(tu.BasicHTTPHandler),
		albpool.PanicHandler(),
	})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	r := request.SetResources(albpool.NewParentGET(t), rsc)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	albpool.ServeAndWait(t, h, w, r)
}

func TestTSMPanicAllMembersDoesNotCrashRequest(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{albpool.PanicHandler(), albpool.PanicHandler()})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	r := request.SetResources(albpool.NewParentGET(t), rsc)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	albpool.RequireFanoutFailureDelta(t, "tsm", "", "panic", 2, func() {
		albpool.ServeAndWait(t, h, w, r)
	})
	if w.Code < 500 {
		t.Errorf("expected 5xx, got %d", w.Code)
	}
}

type mockTimeseriesBackend struct {
	backends.TimeseriesBackend
	parseTRQFunc func(*http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error)
}

func (m *mockTimeseriesBackend) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
	if m.parseTRQFunc != nil {
		return m.parseTRQFunc(r)
	}
	return nil, nil, false, nil
}

func TestLimitQueryRangeALB(t *testing.T) {
	logger.SetLogger(testLogger)

	// Create mock timeseries backend for member 0
	now := time.Now()
	mockMemberBackend := &mockTimeseriesBackend{
		parseTRQFunc: func(r *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
			// default range is 10 days
			days := 10
			if r.Header.Get("X-Test-Range") == "exceed" {
				days = 15
			}
			return &timeseries.TimeRangeQuery{
				Statement: "up",
				Extent: timeseries.Extent{
					Start: now.Add(-time.Duration(days) * 24 * time.Hour),
					End:   now,
				},
			}, nil, false, nil
		},
	}

	// Manually construct target and pool to inject the mock backend client
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	target := pool.NewTarget(http.HandlerFunc(tu.BasicHTTPHandler), status, mockMemberBackend)
	p := pool.New([]*pool.Target{target}, 1)
	defer p.Stop()
	albpool.WaitHealthy(t, p, 1)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)

	t.Run("within limit", func(t *testing.T) {
		r := albpool.NewParentGET(t)
		rsc := request.NewResources(&bo.Options{
			MaxQueryRange:         "14d",
			MaxQueryRangeDuration: 14 * 24 * time.Hour,
		}, nil, nil, nil, nil, nil)
		rsc.IsMergeMember = true
		r = request.SetResources(r, rsc)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		metrics.ProxyQueryRangeRejections.Reset()
		r := albpool.NewParentGET(t)
		r.Header.Set("X-Test-Range", "exceed")
		rsc := request.NewResources(&bo.Options{
			Name:                  "alb-test",
			MaxQueryRange:         "14d",
			MaxQueryRangeDuration: 14 * 24 * time.Hour,
		}, nil, nil, nil, nil, nil)
		rsc.IsMergeMember = true
		r = request.SetResources(r, rsc)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
		assertMsg := "query time range exceeds the allowed limit of 14d"
		if w.Body.String() != assertMsg+"\n" && w.Body.String() != assertMsg {
			t.Errorf("expected error message to contain %q, got %q", assertMsg, w.Body.String())
		}

		// Verify metric is incremented
		val := testutil.ToFloat64(metrics.ProxyQueryRangeRejections.WithLabelValues("alb-test"))
		if val != 1.0 {
			t.Errorf("expected metric value to be 1.0, got %f", val)
		}
	})
}
