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
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

type weightedAvgStubBackend struct {
	stripKeysStubBackend
}

func (b *weightedAvgStubBackend) PlanTSMMerge(r *http.Request, query string) (*tsmerge.TSMMergePlan, error) {
	if !strings.HasPrefix(query, "avg(") {
		plan := defaultTSMMergePlan(r, query)
		if strings.HasPrefix(query, "stddev(") {
			plan.UnsupportedWarning = "unsupported stddev merge"
			plan.AllowSingleMemberBypass = false
		}
		return plan, nil
	}
	sumReq, err := request.Clone(r)
	if err != nil {
		return nil, err
	}
	countReq, err := request.Clone(r)
	if err != nil {
		return nil, err
	}
	sumQP := url.Values{"query": {strings.Replace(query, "avg(", "sum(", 1)}}
	countQP := url.Values{"query": {strings.Replace(query, "avg(", "count(", 1)}}
	params.SetRequestValues(sumReq, sumQP)
	params.SetRequestValues(countReq, countQP)
	return &tsmerge.TSMMergePlan{
		OriginalQuery: query,
		Variants: []tsmerge.TSMQueryVariant{
			{
				Name:              tsmerge.TSMVariantWeightedAverageSum,
				Request:           sumReq,
				MergeStrategy:     int(tsmerge.StrategySum),
				ResponseAuthority: true,
			},
			{
				Name:          tsmerge.TSMVariantWeightedAverageCount,
				Request:       countReq,
				MergeStrategy: int(tsmerge.StrategySum),
			},
		},
		Reduction: tsmerge.TSMReductionSpec{
			Kind:          tsmerge.TSMReductionWeightedAverage,
			InputVariants: tsmerge.TSMReductionWeightedAverageVariants(),
		},
		Completeness: tsmerge.TSMCompletenessAllVariants,
	}, nil
}

type weightedAvgRankStubBackend struct {
	weightedAvgStubBackend
	mu    sync.Mutex
	query string
	calls int
}

func (b *weightedAvgRankStubBackend) PlanTSMMerge(r *http.Request, query string) (*tsmerge.TSMMergePlan, error) {
	originalQuery := query
	finalize := false
	if strings.HasPrefix(query, "topk(") {
		query = "avg(requests)"
		finalize = true
	}
	plan, err := b.weightedAvgStubBackend.PlanTSMMerge(r, query)
	if err != nil {
		return nil, err
	}
	plan.OriginalQuery = originalQuery
	if finalize {
		plan.Finalizer = tsmerge.TSMFinalizerSpec{Enabled: true, Query: originalQuery}
	}
	return plan, nil
}

func (b *weightedAvgRankStubBackend) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	b.mu.Lock()
	b.query = query
	b.calls++
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

func (b *weightedAvgRankStubBackend) FinalizerCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type weightedAvgMemberSpec struct {
	sumValue   string
	countValue string
}

func weightedAvgDataSet(value string) *dataset.DataSet {
	return weightedAvgDataSetWithTags(value, dataset.Tags{})
}

func weightedAvgDataSetWithTags(value string, tags dataset.Tags) *dataset.DataSet {
	return &dataset.DataSet{
		Results: dataset.Results{{
			SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{Name: "requests", Tags: maps.Clone(tags)},
				Points: dataset.Points{{
					Epoch:  epoch.Epoch(100),
					Size:   32,
					Values: []any{value},
				}},
			}},
		}},
	}
}

func taggedWeightedAvgRespondFunc(
	w http.ResponseWriter,
	_ *http.Request,
	accum *merge.Accumulator,
	statusCode int,
) {
	ds, _ := accum.GetTSData().(*dataset.DataSet)
	seriesCount := 0
	value := ""
	if ds != nil && len(ds.Results) > 0 && ds.Results[0] != nil {
		seriesCount = len(ds.Results[0].SeriesList)
		if seriesCount > 0 && len(ds.Results[0].SeriesList[0].Points) > 0 {
			point := ds.Results[0].SeriesList[0].Points[0]
			if len(point.Values) > 0 {
				value = formatAny(point.Values[0])
			}
		}
	}
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte("series=" + strconv.Itoa(seriesCount) + ";value=" + value))
}

func taggedWeightedAvgMemberHandler(spec weightedAvgMemberSpec, tags dataset.Tags) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		query := qp.Get("query")
		value := spec.sumValue
		if strings.HasPrefix(query, "count(") {
			value = spec.countValue
		}
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = weightedAvgDataSetWithTags(value, tags)
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = taggedWeightedAvgRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
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
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(
				rsc.TSMergeStrategy)
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
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(
				rsc.TSMergeStrategy)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func weightedAvgFailVariantHandler(spec weightedAvgMemberSpec, variant, mode string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		query := qp.Get("query")
		isCount := strings.HasPrefix(query, "count(")
		currentVariant := tsmerge.TSMVariantWeightedAverageSum
		value := spec.sumValue
		if isCount {
			currentVariant = tsmerge.TSMVariantWeightedAverageCount
			value = spec.countValue
		}
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.MergeRespondFunc = weightedAvgRespondFunc
		}
		if currentVariant == variant {
			switch mode {
			case "status":
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case "status-data":
				if rsc != nil {
					rsc.TS = weightedAvgDataSet(value)
					rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
					rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(
						rsc.TSMergeStrategy)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			case "parse":
				if rsc != nil {
					rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
					rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: query}
					rsc.TSUnmarshaler = func([]byte, *timeseries.TimeRangeQuery) (timeseries.Timeseries, error) {
						return nil, errors.New("parse failure")
					}
				}
				_, _ = w.Write([]byte("malformed"))
				return
			case "panic":
				panic("variant panic")
			case "transport":
				if capture := request.GetUpstreamShortReadCapture(r.Context()); capture != nil {
					capture.Mark()
				}
				w.WriteHeader(http.StatusOK)
				return
			case "capture":
				if rsc != nil {
					rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
				}
				_, _ = w.Write([]byte(strings.Repeat("x", 256)))
				return
			}
		}
		if rsc != nil {
			rsc.TS = weightedAvgDataSet(value)
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func delayedWeightedAvgMemberHandler(spec weightedAvgMemberSpec, sumDelay, countDelay time.Duration) http.Handler {
	base := weightedAvgMemberHandler(spec)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qp, _, _ := params.GetRequestValues(r)
		delay := sumDelay
		if strings.HasPrefix(qp.Get("query"), "count(") {
			delay = countDelay
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		base.ServeHTTP(w, r)
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

func newWeightedAvgRequest(t testing.TB, query string) *http.Request {
	t.Helper()
	r := albpool.NewParentGET(t)
	r.URL.RawQuery = "query=" + url.QueryEscape(query)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: query}
	return request.SetResources(r, rsc)
}

func standardPlanMemberHandler(value string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = weightedAvgDataSet(value)
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = weightedAvgRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
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
		albpool.RequireFanoutAttemptDelta(t, names.MechanismTSM,
			tsmerge.TSMVariantWeightedAverageSum, 1, func() {
				albpool.RequireFanoutAttemptDelta(t, names.MechanismTSM,
					tsmerge.TSMVariantWeightedAverageCount, 1, func() {
						h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
					})
			})

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "100=25") {
			t.Fatalf("body: want weighted avg 100=25, got %q", body)
		}
	})

	t.Run("unsupported_single_member_plan_preserves_warning", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{standardPlanMemberHandler("10")})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 1)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "stddev(requests)"))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "unsupported stddev merge") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("degraded_pool_preserves_warning", func(t *testing.T) {
		healthy := newWeightedAvgTarget(
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"}),
		)
		failingStatus := &healthcheck.Status{}
		failingStatus.Set(healthcheck.StatusInitializing)
		failing := pool.NewTarget(
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "1"}),
			failingStatus,
			&weightedAvgStubBackend{},
		)
		p := pool.New(pool.Targets{healthy, failing}, -1)
		p.RefreshHealthy()
		defer p.Stop()
		albpool.WaitHealthy(t, p, 1)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "100=20") ||
			!strings.Contains(w.Body.String(), "served from 1 of 2 replica groups") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("multi_variant_plan_strips_injected_labels", func(t *testing.T) {
		specs := []weightedAvgMemberSpec{
			{sumValue: "60", countValue: "3"},
			{sumValue: "40", countValue: "1"},
		}
		targets := make(pool.Targets, len(specs))
		for i, spec := range specs {
			region := "region-" + strconv.Itoa(i)
			status := &healthcheck.Status{}
			status.Set(healthcheck.StatusPassing)
			backend := &weightedAvgStubBackend{stripKeysStubBackend: stripKeysStubBackend{
				cfg: &bo.Options{Prometheus: &prop.Options{Labels: map[string]string{
					"region": region,
				}}},
			}}
			targets[i] = pool.NewTarget(
				taggedWeightedAvgMemberHandler(spec, dataset.Tags{"region": region}),
				status,
				backend,
			)
		}
		p := pool.New(targets, -1)
		p.RefreshHealthy()
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(specs))
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		if w.Code != http.StatusOK || w.Body.String() != "series=1;value=25" {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})

	for _, test := range []struct {
		name           string
		missingVariant string
		mode           string
	}{
		{"count status failure", tsmerge.TSMVariantWeightedAverageCount, "status"},
		{"count status with data failure", tsmerge.TSMVariantWeightedAverageCount, "status-data"},
		{"count parse failure", tsmerge.TSMVariantWeightedAverageCount, "parse"},
		{"count panic", tsmerge.TSMVariantWeightedAverageCount, "panic"},
		{"count transport failure", tsmerge.TSMVariantWeightedAverageCount, "transport"},
		{"count capture failure", tsmerge.TSMVariantWeightedAverageCount, "capture"},
		{"sum status failure", tsmerge.TSMVariantWeightedAverageSum, "status"},
		{"sum status with data failure", tsmerge.TSMVariantWeightedAverageSum, "status-data"},
	} {
		t.Run("member_completeness_"+strings.ReplaceAll(test.name, " ", "_"), func(t *testing.T) {
			p := newWeightedAvgPool([]http.Handler{
				weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"}),
				weightedAvgFailVariantHandler(
					weightedAvgMemberSpec{sumValue: "40", countValue: "1"},
					test.missingVariant, test.mode,
				),
			})
			defer p.Stop()
			albpool.WaitHealthy(t, p, 2)

			h := &handler{mergePaths: []string{"/"}, maxCaptureBytes: 32}
			h.SetPool(p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))

			if w.Code != http.StatusOK {
				t.Fatalf("status: want 200 got %d body=%q", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, "100=20") {
				t.Fatalf("body: incomplete member affected result: %q", body)
			}
			if strings.Contains(body, "100=25") {
				t.Fatalf("body: incomplete member was included: %q", body)
			}
			if !strings.Contains(body, "pool member 1") ||
				!strings.Contains(body, `variant "`+test.missingVariant+`"`) {
				t.Fatalf("body: missing member/variant warning: %q", body)
			}
			if got := w.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "status=phit") {
				t.Fatalf("result header: want phit, got %q", got)
			}
		})
	}

	t.Run("non_2xx_dataset_seeds_empty_error_response", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgFailVariantHandler(
				weightedAvgMemberSpec{sumValue: "60", countValue: "3"},
				tsmerge.TSMVariantWeightedAverageSum, "status-data",
			),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 1)

		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status: want 503 got %d body=%q", w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "MERGED:empty") || strings.Contains(body, "100=") {
			t.Fatalf("body: error dataset entered reduction: %q", body)
		}
		if !strings.Contains(body, `variant "`+tsmerge.TSMVariantWeightedAverageSum+`"`) {
			t.Fatalf("body: missing failed-variant warning: %q", body)
		}
	})

	t.Run("completion_order_does_not_change_reduction", func(t *testing.T) {
		orders := []struct {
			sum0, count0 time.Duration
			sum1, count1 time.Duration
		}{
			{sum0: 30 * time.Millisecond, count1: 20 * time.Millisecond},
			{count0: 30 * time.Millisecond, sum1: 20 * time.Millisecond},
		}
		for i, order := range orders {
			p := newWeightedAvgPool([]http.Handler{
				delayedWeightedAvgMemberHandler(
					weightedAvgMemberSpec{sumValue: "60", countValue: "3"},
					order.sum0, order.count0,
				),
				delayedWeightedAvgMemberHandler(
					weightedAvgMemberSpec{sumValue: "40", countValue: "1"},
					order.sum1, order.count1,
				),
			})
			albpool.WaitHealthy(t, p, 2)
			h := &handler{mergePaths: []string{"/"}}
			h.SetPool(p)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
			p.Stop()
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "100=25") {
				t.Fatalf("order %d: status=%d body=%q", i, w.Code, w.Body.String())
			}
		}
	})

	t.Run("variants_share_query_concurrency_limit", func(t *testing.T) {
		var current, maximum atomic.Int32
		wrap := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := current.Add(1)
				for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
				}
				defer current.Add(-1)
				time.Sleep(10 * time.Millisecond)
				next.ServeHTTP(w, r)
			})
		}
		p := newWeightedAvgPool([]http.Handler{
			wrap(weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"})),
			wrap(weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "1"})),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)
		limit := 1
		h := &handler{
			mergePaths: []string{"/"},
			tsmOptions: options.TimeSeriesMergeOptions{
				ConcurrencyOptions: options.ConcurrencyOptions{QueryConcurrencyLimit: &limit},
			},
		}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "100=25") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
		if got := maximum.Load(); got != 1 {
			t.Fatalf("maximum in-flight requests: got %d want 1", got)
		}
	})

	t.Run("fanout_capture_budget_is_independent_per_variant", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"}),
			weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "1"}),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)
		h := &handler{
			mergePaths:            []string{"/"},
			maxCaptureBytes:       64,
			maxFanoutCaptureBytes: 64,
		}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "100=20") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "pool member 1") {
			t.Fatalf("body: want excluded second member warning, got %q", w.Body.String())
		}
	})

	t.Run("response_authority_controls_headers", func(t *testing.T) {
		member := func(id string, spec weightedAvgMemberSpec) http.Handler {
			base := weightedAvgMemberHandler(spec)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				qp, _, _ := params.GetRequestValues(r)
				variant := tsmerge.Sum
				if strings.HasPrefix(qp.Get("query"), "count(") {
					variant = tsmerge.Count
				}
				w.Header().Set("X-Plan-Authority", variant+"-"+id)
				w.Header().Add(headers.NameSetCookie, variant+"-"+id+"=1")
				if id == "a" && variant == tsmerge.Sum {
					w.Header().Add(headers.NameSetCookie, "sum-a-extra=1")
				}
				base.ServeHTTP(w, r)
			})
		}
		p := newWeightedAvgPool([]http.Handler{
			member("a", weightedAvgMemberSpec{sumValue: "60", countValue: "3"}),
			member("b", weightedAvgMemberSpec{sumValue: "40", countValue: "1"}),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		if got := w.Header().Get("X-Plan-Authority"); got != "sum-a" {
			t.Fatalf("authority header: got %q want %q", got, "sum-a")
		}
		cookies := w.Header().Values(headers.NameSetCookie)
		if strings.Join(cookies, ",") != "sum-a=1,sum-a-extra=1" {
			t.Fatalf("authority cookies: got %v", cookies)
		}
	})

	t.Run("all_authority_fanout_failures_return_bad_gateway", func(t *testing.T) {
		p := newWeightedAvgPool([]http.Handler{
			weightedAvgFailVariantHandler(weightedAvgMemberSpec{sumValue: "60", countValue: "3"},
				tsmerge.TSMVariantWeightedAverageSum, "panic"),
			weightedAvgFailVariantHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "1"},
				tsmerge.TSMVariantWeightedAverageSum, "panic"),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		if w.Code != http.StatusBadGateway {
			t.Fatalf("status: got %d want 502 body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("cancellation_stops_all_variants", func(t *testing.T) {
		started := make(chan struct{}, 1)
		blocking := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		})
		p := newWeightedAvgPool([]http.Handler{blocking, blocking})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		r := newWeightedAvgRequest(t, "avg(requests)")
		ctx, cancel := context.WithCancel(r.Context())
		r = r.WithContext(ctx)
		done := make(chan struct{})
		go func() {
			h.ServeHTTP(httptest.NewRecorder(), r)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("fanout did not start")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("ServeHTTP did not return after cancellation")
		}
	})

	t.Run("count_fanout_empty_excludes_unpaired_members", func(t *testing.T) {
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
		if !strings.Contains(body, "MERGED:empty") {
			t.Fatalf("body: want empty paired result, got %q", body)
		}
		if !strings.Contains(body, `variant "`+tsmerge.TSMVariantWeightedAverageCount+
			`" returned no usable response`) {
			t.Fatalf("body: want missing avg-count warning, got %q", body)
		}
	})

	t.Run("sum_fanout_empty_excludes_unpaired_members", func(t *testing.T) {
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
		if !strings.Contains(body, "MERGED:empty") {
			t.Fatalf("body: want empty paired result, got %q", body)
		}
		if !strings.Contains(body, `variant "`+tsmerge.TSMVariantWeightedAverageSum+
			`" returned no usable response`) {
			t.Fatalf("body: want missing avg-sum warning, got %q", body)
		}
	})

	t.Run("all_variants_empty_preserves_completeness_warnings", func(t *testing.T) {
		empty := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rsc := request.GetResources(r); rsc != nil {
				rsc.MergeRespondFunc = weightedAvgRespondFunc
			}
			w.WriteHeader(http.StatusOK)
		})
		p := newWeightedAvgPool([]http.Handler{empty, empty})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "avg(requests)"))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "MERGED:empty") {
			t.Fatalf("status=%d body=%q", w.Code, body)
		}
		if !strings.Contains(body, `variant "`+tsmerge.TSMVariantWeightedAverageSum+`"`) ||
			!strings.Contains(body, `variant "`+tsmerge.TSMVariantWeightedAverageCount+`"`) {
			t.Fatalf("body: missing completeness warnings: %q", body)
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
		if got := be.FinalizerCalls(); got != 1 {
			t.Fatalf("finalizer calls got %d want 1", got)
		}
	})
}

func BenchmarkServeMergePlan(b *testing.B) {
	benchmarks := []struct {
		name     string
		query    string
		handlers []http.Handler
	}{
		{
			name:  "standard_one_variant",
			query: "up",
			handlers: []http.Handler{
				standardPlanMemberHandler("10"), standardPlanMemberHandler("20"),
				standardPlanMemberHandler("30"), standardPlanMemberHandler("40"),
			},
		},
		{
			name:  "weighted_average_two_variants",
			query: "avg(requests)",
			handlers: []http.Handler{
				weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "10", countValue: "1"}),
				weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "20", countValue: "2"}),
				weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "30", countValue: "3"}),
				weightedAvgMemberHandler(weightedAvgMemberSpec{sumValue: "40", countValue: "4"}),
			},
		},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			p := newWeightedAvgPool(benchmark.handlers)
			defer p.Stop()
			albpool.WaitHealthy(b, p, len(benchmark.handlers))
			h := &handler{mergePaths: []string{"/"}}
			h.SetPool(p)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, newWeightedAvgRequest(b, benchmark.query))
				if w.Code != http.StatusOK {
					b.Fatalf("status=%d body=%q", w.Code, w.Body.String())
				}
			}
		})
	}
}

// Compile-time check that the stub satisfies TSMMergeProvider.
var _ backends.TSMMergeProvider = (*weightedAvgStubBackend)(nil)
