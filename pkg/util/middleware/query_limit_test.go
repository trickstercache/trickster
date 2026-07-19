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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

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

func TestLimitQueryRange(t *testing.T) {
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	t.Run("no limit configured", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/query", nil)
		rec := httptest.NewRecorder()

		backendOpts := &bo.Options{
			MaxQueryRange:         "",
			MaxQueryRangeDuration: 0,
		}
		resources := request.NewResources(backendOpts, nil, nil, nil, nil, nil)
		r = r.WithContext(tctx.WithResources(r.Context(), resources))

		h := LimitQueryRange(nextHandler)
		h.ServeHTTP(rec, r)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
	})

	t.Run("within allowed limit", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/query", nil)
		rec := httptest.NewRecorder()

		backendOpts := &bo.Options{
			MaxQueryRange:         "14d",
			MaxQueryRangeDuration: 14 * 24 * time.Hour,
		}

		now := time.Now()
		mockBackend := &mockTimeseriesBackend{
			parseTRQFunc: func(req *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
				return &timeseries.TimeRangeQuery{
					Extent: timeseries.Extent{
						Start: now.Add(-10 * 24 * time.Hour),
						End:   now,
					},
				}, nil, false, nil
			},
		}

		resources := request.NewResources(backendOpts, nil, nil, nil, mockBackend, nil)
		r = r.WithContext(tctx.WithResources(r.Context(), resources))

		h := LimitQueryRange(nextHandler)
		h.ServeHTTP(rec, r)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "OK", rec.Body.String())
	})

	t.Run("exceeds allowed limit", func(t *testing.T) {
		metrics.ProxyQueryRangeRejections.Reset()
		r := httptest.NewRequest(http.MethodGet, "/query", nil)
		rec := httptest.NewRecorder()

		backendOpts := &bo.Options{
			Name:                  "test",
			MaxQueryRange:         "14d",
			MaxQueryRangeDuration: 14 * 24 * time.Hour,
		}

		now := time.Now()
		mockBackend := &mockTimeseriesBackend{
			parseTRQFunc: func(req *http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error) {
				return &timeseries.TimeRangeQuery{
					Extent: timeseries.Extent{
						Start: now.Add(-15 * 24 * time.Hour),
						End:   now,
					},
				}, nil, false, nil
			},
		}

		resources := request.NewResources(backendOpts, nil, nil, nil, mockBackend, nil)
		r = r.WithContext(tctx.WithResources(r.Context(), resources))

		h := LimitQueryRange(nextHandler)
		h.ServeHTTP(rec, r)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "query time range exceeds the allowed limit of 14d")

		// Verify metric is incremented
		val := testutil.ToFloat64(metrics.ProxyQueryRangeRejections.WithLabelValues("test"))
		assert.Equal(t, float64(1), val)
	})
}
