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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

type finalizerStubBackend struct {
	stripKeysStubBackend
	mu    sync.Mutex
	query string
}

func (b *finalizerStubBackend) PlanTSMMerge(r *http.Request, query string) (*tsmerge.TSMMergePlan, error) {
	plan := defaultTSMMergePlan(r, query)
	plan.Finalizer = tsmerge.TSMFinalizerSpec{Enabled: true, Query: query}
	plan.AllowSingleMemberBypass = false
	return plan, nil
}

func (b *finalizerStubBackend) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	b.mu.Lock()
	b.query = query
	b.mu.Unlock()

	ds, ok := ts.(*dataset.DataSet)
	if !ok || ds == nil || len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		return
	}
	ds.Results[0].SeriesList = ds.Results[0].SeriesList[:1]
	ds.Warnings = append(ds.Warnings, "finalized")
}

func (b *finalizerStubBackend) Query() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.query
}

type rankRewriteFinalizerStubBackend struct {
	finalizerStubBackend
	innerQuery string
}

type prometheusSortBackend struct {
	*prometheus.Client
}

func (b *prometheusSortBackend) Configuration() *bo.Options { return nil }

func (b *rankRewriteFinalizerStubBackend) PlanTSMMerge(
	r *http.Request, query string,
) (*tsmerge.TSMMergePlan, error) {
	next, err := request.Clone(r)
	if err != nil {
		return nil, err
	}
	params.SetRequestValues(next, url.Values{"query": {b.innerQuery}})
	return &tsmerge.TSMMergePlan{
		OriginalQuery: query,
		Variants: []tsmerge.TSMQueryVariant{{
			Name:              tsmerge.TSMVariantPrimary,
			Request:           next,
			MergeStrategy:     int(tsmerge.StrategySum),
			ResponseAuthority: true,
		}},
		Reduction: tsmerge.TSMReductionSpec{
			Kind:          tsmerge.TSMReductionStandard,
			InputVariants: tsmerge.TSMReductionPrimaryVariant(),
		},
		Finalizer:    tsmerge.TSMFinalizerSpec{Enabled: true, Query: query},
		Completeness: tsmerge.TSMCompletenessResponseAuthority,
	}, nil
}

type queryRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (qr *queryRecorder) Append(query string) {
	qr.mu.Lock()
	defer qr.mu.Unlock()
	qr.queries = append(qr.queries, query)
}

func (qr *queryRecorder) Queries() []string {
	qr.mu.Lock()
	defer qr.mu.Unlock()
	return append([]string(nil), qr.queries...)
}

func recordingMergeHandler(marker string, qr *queryRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		qr.Append(qp.Get("query"))
		stubMergeHandler(marker, http.StatusOK).ServeHTTP(w, r)
	})
}

func aggregationMergeHandler(values map[string]string, qr *queryRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		query := qp.Get("query")
		qr.Append(query)

		seriesList := make(dataset.SeriesList, 0, len(values))
		for service, value := range values {
			seriesList = append(seriesList, &dataset.Series{
				Header: dataset.SeriesHeader{
					Name:           "count",
					Tags:           dataset.Tags{"service": service},
					QueryStatement: query,
				},
				Points: dataset.Points{{
					Epoch:  epoch.Epoch(100),
					Values: []any{value},
				}},
			})
		}

		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = &dataset.DataSet{Results: dataset.Results{{SeriesList: seriesList}}}
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = aggregationRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
}

func aggregationRespondFunc(w http.ResponseWriter, _ *http.Request,
	accum *merge.Accumulator, statusCode int,
) {
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	ds, _ := accum.GetTSData().(*dataset.DataSet)
	if ds == nil || len(ds.Results) == 0 || ds.Results[0] == nil {
		return
	}
	parts := make([]string, 0, len(ds.Results[0].SeriesList))
	for _, series := range ds.Results[0].SeriesList {
		if series == nil || len(series.Points) == 0 || len(series.Points[0].Values) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", series.Header.Tags["service"],
			series.Points[0].Values[0]))
	}
	_, _ = w.Write([]byte(strings.Join(parts, ",")))
}

func TestServeStandardCallsMergeFinalizer(t *testing.T) {
	logger.SetLogger(testLogger)

	be := &finalizerStubBackend{}
	targets := make(pool.Targets, 2)
	for i, h := range []http.Handler{
		stubMergeHandler("alpha", http.StatusOK),
		stubMergeHandler("beta", http.StatusOK),
	} {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(h, st, be)
	}
	p := pool.New(targets, -1)
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)

	req := newTestMergeRequest(t)
	rsc := request.GetResources(req)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: "topk(1, up)"}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := be.Query(); got != "topk(1, up)" {
		t.Fatalf("finalizer query got %q want %q", got, "topk(1, up)")
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "MERGED:series=alpha|warnings=") {
		t.Fatalf("body got %q; expected only alpha series after finalization", body)
	}
	if strings.Contains(body, "series=alpha,beta") || strings.Contains(body, "series=beta") {
		t.Fatalf("body got %q; beta should have been removed by finalizer", body)
	}
	for _, warning := range []string{"alpha", "beta", "finalized"} {
		if !strings.Contains(body, warning) {
			t.Fatalf("body got %q; missing warning %q", body, warning)
		}
	}
}

func TestServeStandardRewritesRankFanoutToInnerQuery(t *testing.T) {
	logger.SetLogger(testLogger)

	const (
		outerQuery = "topk(1, sum by (service) (requests))"
		innerQuery = "sum by (service) (requests)"
	)
	be := &rankRewriteFinalizerStubBackend{innerQuery: innerQuery}
	qr := &queryRecorder{}
	targets := make(pool.Targets, 2)
	for i, h := range []http.Handler{
		recordingMergeHandler("alpha", qr),
		recordingMergeHandler("beta", qr),
	} {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(h, st, be)
	}
	p := pool.New(targets, -1)
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)

	req := newTestMergeRequest(t)
	req.URL.RawQuery = "query=" + url.QueryEscape(outerQuery)
	rsc := request.GetResources(req)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: outerQuery}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if got := be.Query(); got != outerQuery {
		t.Fatalf("finalizer query got %q want %q", got, outerQuery)
	}
	queries := qr.Queries()
	if len(queries) != 2 {
		t.Fatalf("recorded queries got %v want two entries", queries)
	}
	for _, got := range queries {
		if got != innerQuery {
			t.Fatalf("fanout query got %q want %q (all queries: %v)", got, innerQuery, queries)
		}
	}
}

func TestServeStandardRankRewritePropagatesCancellationAndSkipsMerge(t *testing.T) {
	logger.SetLogger(testLogger)

	const (
		outerQuery = "topk(1, sum by (service) (requests))"
		innerQuery = "sum by (service) (requests)"
	)
	be := &rankRewriteFinalizerStubBackend{innerQuery: innerQuery}
	started := make(chan struct{}, 2)
	var canceled atomic.Int32
	var merged atomic.Int32

	blockingHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		canceled.Add(1)

		// Populate a valid contribution after cancellation. Fanout/TSM must
		// discard it instead of doing result or finalizer work for a caller
		// that is no longer present.
		rsc := request.GetResources(r)
		rsc.TS = newMarkerDataSet("late")
		rsc.MergeFunc = func(*merge.Accumulator, any, int) error {
			merged.Add(1)
			return nil
		}
		rsc.MergeRespondFunc = markerRespondFunc
		w.WriteHeader(http.StatusOK)
	})

	targets := make(pool.Targets, 2)
	for i := range targets {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(blockingHandler, st, be)
	}
	p := pool.New(targets, -1)
	defer p.Stop()
	p.RefreshHealthy()

	limit := 2
	h := &handler{mergePaths: []string{"/"}}
	h.tsmOptions.ConcurrencyOptions.QueryConcurrencyLimit = &limit
	h.SetPool(p)

	req := newTestMergeRequest(t)
	req.URL.RawQuery = "query=" + url.QueryEscape(outerQuery)
	rsc := request.GetResources(req)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: outerQuery}
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("fanout member did not start")
		}
	}
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("TSM request did not return after caller cancellation")
	}
	if got := canceled.Load(); got != 2 {
		t.Fatalf("canceled fanout members = %d, want 2", got)
	}
	if got := merged.Load(); got != 0 {
		t.Fatalf("merge calls after cancellation = %d, want 0", got)
	}
	if got := be.Query(); got != "" {
		t.Fatalf("finalizer ran after cancellation with query %q", got)
	}
}

func TestServeStandardMergesThroughSortWrapper(t *testing.T) {
	logger.SetLogger(testLogger)

	const (
		outerQuery = "sort_desc(count by (service) (requests))"
		innerQuery = "count by (service) (requests)"
	)
	be := &prometheusSortBackend{Client: &prometheus.Client{}}
	qr := &queryRecorder{}
	targets := make(pool.Targets, 2)
	for i, h := range []http.Handler{
		aggregationMergeHandler(map[string]string{"api": "2", "worker": "10"}, qr),
		aggregationMergeHandler(map[string]string{"api": "3", "worker": "1"}, qr),
	} {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(h, st, be)
	}
	p := pool.New(targets, -1)
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)

	req := newTestMergeRequest(t)
	req.URL.RawQuery = "query=" + url.QueryEscape(outerQuery)
	rsc := request.GetResources(req)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: outerQuery}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status got %d want %d body=%q", w.Code, http.StatusOK, w.Body.String())
	}
	if got, want := w.Body.String(), "worker=11,api=5"; got != want {
		t.Fatalf("body got %q want %q", got, want)
	}
	queries := qr.Queries()
	if len(queries) != 2 {
		t.Fatalf("recorded queries got %v want two entries", queries)
	}
	for _, got := range queries {
		if got != innerQuery {
			t.Fatalf("fanout query got %q want %q (all queries: %v)", got, innerQuery, queries)
		}
	}
}
