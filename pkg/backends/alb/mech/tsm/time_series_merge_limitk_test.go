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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	responsemerge "github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type limitKMemberSpec struct {
	replica    string
	values     map[string]string
	histograms map[string]string
	delay      time.Duration
	fail       bool
	capture    bool
}

func limitKMemberHandler(spec limitKMemberSpec, recorder *queryRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, _, _ := params.GetRequestValues(r)
		query := values.Get("query")
		recorder.Append(query)
		if spec.delay > 0 {
			time.Sleep(spec.delay)
		}
		if spec.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if spec.capture {
			rsc := request.GetResources(r)
			if rsc != nil {
				rsc.MergeFunc = responsemerge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
				rsc.BatchMergeFunc = responsemerge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
				rsc.MergeRespondFunc = limitKRespondFunc
			}
			_, _ = w.Write([]byte(strings.Repeat("x", 256)))
			return
		}

		seriesList := make(dataset.SeriesList, 0, len(spec.values)+len(spec.histograms))
		for instance, value := range spec.values {
			seriesList = append(seriesList, &dataset.Series{
				Header: dataset.SeriesHeader{
					Name:           "up",
					Tags:           dataset.Tags{"__name__": "up", "instance": instance, "replica": spec.replica},
					QueryStatement: query,
					ValueFieldsList: timeseries.FieldDefinitions{{
						Name: "value", DataType: timeseries.String,
					}},
				},
				Points: dataset.Points{{
					Epoch: epoch.Epoch(100), Values: []any{value},
				}},
			})
		}
		for instance, value := range spec.histograms {
			seriesList = append(seriesList, &dataset.Series{
				Header: dataset.SeriesHeader{
					Name:           "up",
					Tags:           dataset.Tags{"__name__": "up", "instance": instance, "replica": spec.replica},
					QueryStatement: query,
					ValueFieldsList: timeseries.FieldDefinitions{{
						Name: "histogram", DataType: timeseries.String,
					}},
				},
				Points: dataset.Points{{
					Epoch: epoch.Epoch(100), Values: []any{value},
				}},
			})
		}
		seriesList.SortByTags()

		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = &dataset.DataSet{
				Status:         "success",
				TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: query},
				Results:        dataset.Results{{StatementID: 1, SeriesList: seriesList}},
			}
			rsc.MergeFunc = responsemerge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = responsemerge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = limitKRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
}

func limitKRespondFunc(w http.ResponseWriter, _ *http.Request, accum *responsemerge.Accumulator,
	statusCode int,
) {
	headers.StripMergeHeaders(w.Header())
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ds, _ := accum.GetTSData().(*dataset.DataSet)
	w.WriteHeader(statusCode)
	if ds == nil || len(ds.Results) == 0 || ds.Results[0] == nil {
		_, _ = w.Write([]byte("MERGED:empty|warnings=" + strings.Join(dsWarnings(ds), ",")))
		return
	}
	parts := make([]string, 0, len(ds.Results[0].SeriesList))
	for _, series := range ds.Results[0].SeriesList {
		if series == nil || len(series.Points) == 0 || len(series.Points[0].Values) == 0 {
			continue
		}
		name := series.Header.Tags["instance"]
		if replica := series.Header.Tags["replica"]; replica != "" {
			name += "[" + replica + "]"
		}
		parts = append(parts, name+"="+formatAny(series.Points[0].Values[0]))
	}
	_, _ = w.Write([]byte("MERGED:" + strings.Join(parts, ",") +
		"|warnings=" + strings.Join(dsWarnings(ds), ",")))
}

func newLimitKPool(specs []limitKMemberSpec, recorder *queryRecorder) pool.Pool {
	targets := make(pool.Targets, len(specs))
	for i, spec := range specs {
		status := &healthcheck.Status{}
		status.Set(healthcheck.StatusPassing)
		backend := &pooledVarianceBackend{
			stripKeysStubBackend: stripKeysStubBackend{
				cfg: &bo.Options{Prometheus: &prop.Options{
					Labels: map[string]string{"replica": spec.replica},
				}},
			},
		}
		targets[i] = pool.NewTarget(limitKMemberHandler(spec, recorder), status, backend)
	}
	p := pool.New(targets, -1)
	p.RefreshHealthy()
	return p
}

func TestServeLimitKGloballySelectsInStableOrder(t *testing.T) {
	logger.SetLogger(testLogger)
	tests := []struct {
		name  string
		specs []limitKMemberSpec
	}{
		{
			name: "first pool member completes last",
			specs: []limitKMemberSpec{
				{replica: "a", values: map[string]string{"d": "4", "a": "1"}, delay: 30 * time.Millisecond},
				{replica: "b", values: map[string]string{"c": "3", "b": "2"}},
				// This faster HA copy must not consume either global slot twice.
				{replica: "c", values: map[string]string{"d": "4", "a": "1"}},
			},
		},
		{
			name: "pool and completion order change",
			specs: []limitKMemberSpec{
				{replica: "c", values: map[string]string{"d": "4", "a": "1"}},
				{replica: "a", values: map[string]string{"d": "4", "a": "1"}},
				{replica: "b", values: map[string]string{"c": "3", "b": "2"}, delay: 30 * time.Millisecond},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &queryRecorder{}
			p := newLimitKPool(tt.specs, recorder)
			defer p.Stop()
			albpool.WaitHealthy(t, p, 3)

			h := &handler{mergePaths: []string{"/"}}
			h.SetPool(p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, newWeightedAvgRequest(t, "limitk(2, up)"))

			if w.Code != http.StatusOK || w.Body.String() != "MERGED:a=1,b=2|warnings=" {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			queries := recorder.Queries()
			if len(queries) != 3 {
				t.Fatalf("fanout queries got %v want three", queries)
			}
			for _, got := range queries {
				if got != "up" {
					t.Fatalf("fanout query got %q want %q (all: %v)", got, "up", queries)
				}
			}
		})
	}
}

func TestServeLimitKPreservesNativeHistograms(t *testing.T) {
	logger.SetLogger(testLogger)
	const histogram = `{"count":"2","sum":"3"}`
	p := newLimitKPool([]limitKMemberSpec{
		{replica: "a", values: map[string]string{"a": "1", "d": "4"}},
		{replica: "b", histograms: map[string]string{"b": histogram}, values: map[string]string{"c": "3"}},
	}, &queryRecorder{})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWeightedAvgRequest(t, "limitk(2, up)"))

	want := "MERGED:a=1,b=" + histogram + "|warnings="
	if w.Code != http.StatusOK || w.Body.String() != want {
		t.Fatalf("status=%d body=%q want=%q", w.Code, w.Body.String(), want)
	}
}

func TestServeLimitKMergesInnerCountBeforeSelection(t *testing.T) {
	logger.SetLogger(testLogger)
	const (
		query = "limitk(1, count by (instance) (requests))"
		inner = "count by (instance) (requests)"
	)
	recorder := &queryRecorder{}
	p := newLimitKPool([]limitKMemberSpec{
		{replica: "a", values: map[string]string{"a": "2", "b": "10"}},
		{replica: "b", values: map[string]string{"a": "3", "b": "1"}},
	}, recorder)
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWeightedAvgRequest(t, query))

	if w.Code != http.StatusOK || w.Body.String() != "MERGED:a=5|warnings=" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	queries := recorder.Queries()
	if len(queries) != 2 || queries[0] != inner || queries[1] != inner {
		t.Fatalf("fanout queries got %v want two %q queries", queries, inner)
	}
}

func TestServeLimitKPartialFanoutIsMarked(t *testing.T) {
	logger.SetLogger(testLogger)
	tests := []struct {
		name      string
		failed    limitKMemberSpec
		configure func(*handler)
	}{
		{name: "status", failed: limitKMemberSpec{replica: "b", fail: true}},
		{name: "capture", failed: limitKMemberSpec{replica: "b", capture: true},
			configure: func(h *handler) { h.maxCaptureBytes = 32 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &queryRecorder{}
			p := newLimitKPool([]limitKMemberSpec{
				{replica: "a", values: map[string]string{"a": "1"}}, tt.failed,
			}, recorder)
			defer p.Stop()
			albpool.WaitHealthy(t, p, 2)

			h := &handler{mergePaths: []string{"/"}}
			if tt.configure != nil {
				tt.configure(h)
			}
			h.SetPool(p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, newWeightedAvgRequest(t, "limitk(1, up)"))

			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.Contains(body, "MERGED:a=1") ||
				!strings.Contains(body, "partial failure") {
				t.Fatalf("status=%d body=%q", w.Code, body)
			}
			if got := w.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "phit") {
				t.Fatalf("result header got %q want phit", got)
			}
		})
	}
}
