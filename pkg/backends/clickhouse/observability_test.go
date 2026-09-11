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

package clickhouse

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type failingExtentRenderer struct{}

func (failingExtentRenderer) RenderExtent(timeseries.Extent) (string, error) {
	return "", errors.New("test renderer failure")
}

func TestParseTimeRangeQueryRecordsEligibility(t *testing.T) {
	client := &Client{}
	tests := []struct {
		name   string
		query  string
		mode   string
		reason sqlanalyzer.AnalysisReason
	}{
		{
			name: "delta eligible",
			query: "SELECT toStartOfMinute(ts) AS t, count() FROM events " +
				"WHERE ts >= 120 AND ts < 240 GROUP BY t",
			mode: "delta", reason: sqlanalyzer.ReasonDeltaCacheable,
		},
		{
			name:  "parser failure falls back to object cache",
			query: "SELECT !!!",
			mode:  "object", reason: sqlanalyzer.ReasonInvalidSQL,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			counter := metrics.SQLQueryAnalysis.WithLabelValues("", clickHouseDialect, test.mode, string(test.reason))
			before := testutil.ToFloat64(counter)
			r := httptest.NewRequest(http.MethodGet, "http://trickster.example.com/?"+url.Values{
				upQuery: {test.query},
			}.Encode(), nil)
			_, _, _, _ = client.ParseTimeRangeQuery(r)
			if got := testutil.ToFloat64(counter); got != before+1 {
				t.Errorf("analysis counter = %f, want %f", got, before+1)
			}
		})
	}
}

func TestSetExtentRecordsRewriteFailures(t *testing.T) {
	originalLogger := logger.Logger()
	logger.SetLogger(logging.NoopLogger())
	defer logger.SetLogger(originalLogger)

	client := &Client{}
	extent := &timeseries.Extent{Start: time.Unix(120, 0), End: time.Unix(180, 0)}
	tests := []struct {
		name   string
		reason string
		trq    *timeseries.TimeRangeQuery
	}{
		{name: "missing plan", reason: "missing_plan", trq: &timeseries.TimeRangeQuery{}},
		{
			name: "renderer error", reason: "render_error",
			trq: &timeseries.TimeRangeQuery{ParsedQuery: &sqlanalyzer.QueryPlan{Renderer: failingExtentRenderer{}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			counter := metrics.SQLQueryRewriteFailures.WithLabelValues("", clickHouseDialect, test.reason)
			before := testutil.ToFloat64(counter)
			r := httptest.NewRequest(http.MethodGet, "http://trickster.example.com/?query=original", nil)
			if err := client.SetExtent(r, test.trq, extent); err == nil {
				t.Fatal("SetExtent() error = nil, want rewrite failure")
			}
			if got := testutil.ToFloat64(counter); got != before+1 {
				t.Errorf("rewrite-failure counter = %f, want %f", got, before+1)
			}
			if got := r.URL.Query().Get(upQuery); got != "original" {
				t.Errorf("failed rewrite changed request to %q", got)
			}
		})
	}

	t.Run("invalid input", func(t *testing.T) {
		counter := metrics.SQLQueryRewriteFailures.WithLabelValues("", clickHouseDialect, "invalid_input")
		before := testutil.ToFloat64(counter)
		r := httptest.NewRequest(http.MethodGet, "http://trickster.example.com/?query=original", nil)
		if err := client.SetExtent(r, nil, extent); err == nil {
			t.Fatal("SetExtent() error = nil, want invalid-input failure")
		}
		if got := testutil.ToFloat64(counter); got != before+1 {
			t.Errorf("rewrite-failure counter = %f, want %f", got, before+1)
		}
	})

	t.Run("invalid request", func(t *testing.T) {
		trq, _, _, err := parse(tq02, nil)
		if err != nil {
			t.Fatal(err)
		}
		counter := metrics.SQLQueryRewriteFailures.WithLabelValues("", clickHouseDialect, "invalid_request")
		before := testutil.ToFloat64(counter)
		r := &http.Request{Method: http.MethodGet}
		if err := client.SetExtent(r, trq, extent); err == nil {
			t.Fatal("SetExtent() error = nil, want invalid-request failure")
		}
		if got := testutil.ToFloat64(counter); got != before+1 {
			t.Errorf("rewrite-failure counter = %f, want %f", got, before+1)
		}
	})
}
