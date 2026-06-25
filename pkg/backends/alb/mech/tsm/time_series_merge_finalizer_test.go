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
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

type finalizerStubBackend struct {
	stripKeysStubBackend
	mu    sync.Mutex
	query string
}

func (b *finalizerStubBackend) ClassifyMerge(string) (int, bool, string) {
	return int(dataset.MergeStrategyDedup), false, ""
}

func (b *finalizerStubBackend) RewriteForWeightedAvg(r *http.Request, _ string) (*http.Request, *http.Request) {
	return r, r
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

func (b *rankRewriteFinalizerStubBackend) ClassifyMerge(string) (int, bool, string) {
	return int(dataset.MergeStrategySum), false, ""
}

func (b *rankRewriteFinalizerStubBackend) RewriteForTSMMerge(
	r *http.Request, _ string,
) (*http.Request, string) {
	next, err := request.Clone(r)
	if err != nil {
		return r, b.innerQuery
	}
	params.SetRequestValues(next, url.Values{"query": {b.innerQuery}})
	return next, b.innerQuery
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
