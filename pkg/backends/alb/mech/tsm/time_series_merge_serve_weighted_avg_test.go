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
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type weightedAvgStubBackend struct {
	stripKeysStubBackend
}

func (b *weightedAvgStubBackend) ClassifyMerge(query string) (int, bool, string) {
	if strings.HasPrefix(query, "avg(") {
		return int(dataset.MergeStrategySum), true, ""
	}
	return int(dataset.MergeStrategyDedup), false, ""
}

func (b *weightedAvgStubBackend) RewriteForWeightedAvg(r *http.Request, query string) (*http.Request, *http.Request) {
	sumReq, err := request.Clone(r)
	if err != nil {
		return r, r
	}
	countReq, err := request.Clone(r)
	if err != nil {
		return sumReq, sumReq
	}
	sumQP := url.Values{"query": {strings.Replace(query, "avg(", "sum(", 1)}}
	countQP := url.Values{"query": {strings.Replace(query, "avg(", "count(", 1)}}
	params.SetRequestValues(sumReq, sumQP)
	params.SetRequestValues(countReq, countQP)
	return sumReq, countReq
}

type weightedAvgRankStubBackend struct {
	weightedAvgStubBackend
	mu    sync.Mutex
	query string
}

func (b *weightedAvgRankStubBackend) ClassifyMerge(query string) (int, bool, string) {
	if strings.HasPrefix(query, "topk(") {
		return int(dataset.MergeStrategySum), true, ""
	}
	return b.weightedAvgStubBackend.ClassifyMerge(query)
}

func (b *weightedAvgRankStubBackend) RewriteForWeightedAvg(r *http.Request, query string) (*http.Request, *http.Request) {
	if strings.HasPrefix(query, "topk(") {
		query = "avg(requests)"
	}
	return b.weightedAvgStubBackend.RewriteForWeightedAvg(r, query)
}

func (b *weightedAvgRankStubBackend) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	b.mu.Lock()
	b.query = query
	b.mu.Unlock()

	if ds, ok := ts.(*dataset.DataSet); ok && ds != nil {
		ds.Warnings = append(ds.Warnings, "rank-finalized")
	}
}

func (b *weightedAvgRankStubBackend) Query() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.query
}

type weightedAvgMemberSpec struct {
	sumValue   string
	countValue string
}

func weightedAvgDataSet(value string) *dataset.DataSet {
	return &dataset.DataSet{
		Results: dataset.Results{{
			SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{Name: "requests", Tags: dataset.Tags{}},
				Points: dataset.Points{{
					Epoch:  epoch.Epoch(100),
					Size:   32,
					Values: []any{value},
				}},
			}},
		}},
	}
}

func weightedAvgRespondFunc(w http.ResponseWriter, _ *http.Request, accum *merge.Accumulator, statusCode int) {
	headers.StripMergeHeaders(w.Header())
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ds, _ := accum.GetTSData().(*dataset.DataSet)
	w.WriteHeader(statusCode)
	if ds == nil || len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
		_, _ = w.Write([]byte("MERGED:empty|warnings=" + strings.Join(dsWarnings(ds), ",")))
		return
	}
	var sb strings.Builder
	sb.WriteString("MERGED:")
	for _, p := range ds.Results[0].SeriesList[0].Points {
		sb.WriteString(strconv.FormatInt(int64(p.Epoch), 10))
		sb.WriteByte('=')
		if len(p.Values) > 0 {
			sb.WriteString(formatAny(p.Values[0]))
		}
		sb.WriteByte(';')
	}
	sb.WriteString("|warnings=")
	sb.WriteString(strings.Join(dsWarnings(ds), ","))
	_, _ = w.Write([]byte(sb.String()))
}

func dsWarnings(ds *dataset.DataSet) []string {
	if ds == nil {
		return nil
	}
	return ds.Warnings
}

func formatAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func weightedAvgMemberHandler(spec weightedAvgMemberSpec) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		query := qp.Get("query")
		var val string
		switch {
		case strings.HasPrefix(query, "sum("):
			val = spec.sumValue
		case strings.HasPrefix(query, "count("):
			val = spec.countValue
		default:
			http.Error(w, "unexpected query", http.StatusInternalServerError)
			return
		}
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = weightedAvgDataSet(val)
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = weightedAvgRespondFunc
		}
		w.Header().Set(headers.NameTricksterResult, "engine=none")
		w.WriteHeader(http.StatusOK)
	})
}

func weightedAvgEmptySideHandler(failCount bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		query := qp.Get("query")
		isCount := strings.HasPrefix(query, "count(")
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.MergeRespondFunc = weightedAvgRespondFunc
			if failCount == isCount {
				w.WriteHeader(http.StatusOK)
				return
			}
			val := "10"
			if isCount {
				val = "2"
			}
			rsc.TS = weightedAvgDataSet(val)
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func newWeightedAvgTarget(h http.Handler) *pool.Target {
	st := &healthcheck.Status{}
	st.Set(healthcheck.StatusPassing)
	be := &weightedAvgStubBackend{}
	return pool.NewTarget(h, st, be)
}

func newWeightedAvgPool(handlers []http.Handler) pool.Pool {
	targets := make(pool.Targets, len(handlers))
	for i, h := range handlers {
		targets[i] = newWeightedAvgTarget(h)
	}
	p := pool.New(targets, -1)
	p.RefreshHealthy()
	return p
}

func newWeightedAvgRankPool(handlers []http.Handler, be *weightedAvgRankStubBackend) pool.Pool {
	targets := make(pool.Targets, len(handlers))
	for i, h := range handlers {
		st := &healthcheck.Status{}
		st.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(h, st, be)
	}
	p := pool.New(targets, -1)
	p.RefreshHealthy()
	return p
}

func newWeightedAvgRequest(t *testing.T, query string) *http.Request {
	t.Helper()
	r := albpool.NewParentGET(t)
	r.URL.RawQuery = "query=" + url.QueryEscape(query)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: query}
	return request.SetResources(r, rsc)
}

func TestServeWeightedAvg(t *testing.T) {
	logger.SetLogger(testLogger)

	t.Run("two_members_finalize_weighted_average", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"}),
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "1"}),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "100=25") {
			t.Fatalf("body: want weighted avg 100=25, got %q", body)
		}
	})

	t.Run("count_fanout_empty_warns_unfinalized_sums", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgEmptySideHandler(true),
			weightedAvgEmptySideHandler(true),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "100=20") {
			t.Fatalf("body: want unfinalized merged sum 100=20, got %q", body)
		}
		if !strings.Contains(body, "count fanout returned no usable responses") {
			t.Fatalf("body: want count-fanout warning, got %q", body)
		}
	})

	t.Run("sum_fanout_empty_promotes_count_with_warning", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgEmptySideHandler(false),
			weightedAvgEmptySideHandler(false),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "100=4") {
			t.Fatalf("body: want promoted merged count 100=4, got %q", body)
		}
		if !strings.Contains(body, "sum fanout returned no usable responses") {
			t.Fatalf("body: want sum-fanout warning, got %q", body)
		}
	})

	t.Run("mixed_sum_status_marks_phit", func(t *testing.T) {
		okHandler := weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "10", countValue: "2"})
		errHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			qp, _, _ := params.GetRequestValues(r)
			if strings.HasPrefix(qp.Get("query"), "sum(") {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "0", countValue: "0"}).
				ServeHTTP(w, r)
		})

		p := newWeightedAvgPool([]http.Handler{okHandler, errHandler})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))

		if status := w.Header().Get(headers.NameTricksterResult); !strings.Contains(status, "phit") {
			t.Fatalf("%s: want phit marker, got %q", headers.NameTricksterResult, status)
		}
	})

	t.Run("instant_query_from_form_param", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "20", countValue: "2"}),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 1)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)

		r := albpool.NewParentGET(t)
		r.URL.RawQuery = "query=" + url.QueryEscape("avg(requests)")
		rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
		r = request.SetResources(r, rsc)

		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "100=10") {
			t.Fatalf("body: want weighted avg 100=10, got %q", w.Body.String())
		}
	})

	t.Run("rank_wrapper_finalizes_after_weighted_average", func(t *testing.T) {
		be := &weightedAvgRankStubBackend{}
		p := newWeightedAvgRankPool([]http.Handler{
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"}),
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "1"}),
		}, be)
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)

		const query = "topk(1, avg(requests))"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, query))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "100=25") {
			t.Fatalf("body: want weighted avg 100=25, got %q", body)
		}
		if !strings.Contains(body, "rank-finalized") {
			t.Fatalf("body: want finalizer warning, got %q", body)
		}
		if got := be.Query(); got != query {
			t.Fatalf("finalizer query got %q want %q", got, query)
		}
	})
}

// Compile-time check that the stub satisfies TSMMergeProvider.
var _ backends.TSMMergeProvider = (*weightedAvgStubBackend)(nil)
