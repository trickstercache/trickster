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
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prometheusbackend "github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	responsemerge "github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

type pooledVarianceBackend struct {
	stripKeysStubBackend
	client prometheusbackend.Client
}

func (b *pooledVarianceBackend) PlanTSMMerge(r *http.Request, query string) (*tsmerge.TSMMergePlan, error) {
	return b.client.PlanTSMMerge(r, query)
}

func (b *pooledVarianceBackend) FinalizeTSMMerge(query string, ts timeseries.Timeseries) {
	b.client.FinalizeTSMMerge(query, ts)
}

type pooledVarianceMemberSpec struct {
	count          string
	mean           string
	variance       string
	tags           dataset.Tags
	failVariant    string
	missingMeanAt2 bool
}

func pooledVarianceMemberHandler(spec pooledVarianceMemberSpec) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, _, _ := params.GetRequestValues(r)
		query := values.Get("query")
		variant := tsmerge.TSMVariantPooledVarianceCount
		value := spec.count
		switch {
		case strings.HasPrefix(query, "avg") || strings.Contains(query, "(avg "):
			variant = tsmerge.TSMVariantPooledVarianceMean
			value = spec.mean
		case strings.HasPrefix(query, "stdvar") || strings.Contains(query, "(stdvar "):
			variant = tsmerge.TSMVariantPooledVarianceValue
			value = spec.variance
		}
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.MergeRespondFunc = weightedAvgRespondFunc
		}
		if variant == spec.failVariant {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if rsc != nil {
			pointValues := []any{int64(100), value}
			if !spec.missingMeanAt2 || variant != tsmerge.TSMVariantPooledVarianceMean {
				pointValues = append(pointValues, int64(200), value)
			}
			rsc.TS = pooledVarianceTestDataSet(query, spec.tags.Clone(), pointValues...)
			rsc.MergeFunc = responsemerge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = responsemerge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
		}
		w.WriteHeader(http.StatusOK)
	})
}

func newPooledVariancePool(specs []pooledVarianceMemberSpec, stripLabel string) pool.Pool {
	targets := make(pool.Targets, len(specs))
	for i, spec := range specs {
		status := &healthcheck.Status{}
		status.Set(healthcheck.StatusPassing)
		labels := map[string]string(nil)
		if stripLabel != "" {
			labels = map[string]string{stripLabel: spec.tags[stripLabel]}
		}
		backend := &pooledVarianceBackend{
			stripKeysStubBackend: stripKeysStubBackend{
				cfg: &bo.Options{Prometheus: &prop.Options{Labels: labels}},
			},
		}
		targets[i] = pool.NewTarget(pooledVarianceMemberHandler(spec), status, backend)
	}
	p := pool.New(targets, -1)
	p.RefreshHealthy()
	return p
}

func TestServePooledVariance(t *testing.T) {
	logger.SetLogger(testLogger)

	t.Run("unequal members and HA replica", func(t *testing.T) {
		specs := []pooledVarianceMemberSpec{
			{count: "2", mean: "2", variance: "1", tags: dataset.Tags{"job": "api", "replica": "a"}},
			{count: "3", mean: "7", variance: "2.6666666666666665", tags: dataset.Tags{"job": "api", "replica": "b"}},
			{count: "2", mean: "2", variance: "1", tags: dataset.Tags{"job": "api", "replica": "c"}},
		}
		p := newPooledVariancePool(specs, "replica")
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(specs))
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()

		albpool.RequireFanoutAttemptDelta(t, names.MechanismTSM,
			tsmerge.TSMVariantPooledVarianceCount, 1, func() {
				albpool.RequireFanoutAttemptDelta(t, names.MechanismTSM,
					tsmerge.TSMVariantPooledVarianceMean, 1, func() {
						albpool.RequireFanoutAttemptDelta(t, names.MechanismTSM,
							tsmerge.TSMVariantPooledVarianceValue, 1, func() {
								h.ServeHTTP(w, newWeightedAvgRequest(t, "stdvar by (job) (requests)"))
							})
					})
			})

		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "100=8;") ||
			!strings.Contains(w.Body.String(), "200=8;") {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("failed variant excludes the whole member", func(t *testing.T) {
		specs := []pooledVarianceMemberSpec{
			{count: "2", mean: "2", variance: "1", tags: dataset.Tags{"job": "api"},
				failVariant: tsmerge.TSMVariantPooledVarianceValue},
			{count: "2", mean: "10", variance: "4", tags: dataset.Tags{"job": "api"}},
		}
		p := newPooledVariancePool(specs, "")
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(specs))
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "stdvar by (job) (requests)"))

		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "100=4;") ||
			!strings.Contains(body, tsmerge.TSMVariantPooledVarianceValue) {
			t.Fatalf("status=%d body=%q", w.Code, body)
		}
	})

	t.Run("missing timestamp is visible", func(t *testing.T) {
		specs := []pooledVarianceMemberSpec{{
			count: "2", mean: "3", variance: "1", tags: dataset.Tags{"job": "api"},
			missingMeanAt2: true,
		}}
		p := newPooledVariancePool(specs, "")
		defer p.Stop()
		albpool.WaitHealthy(t, p, 1)
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(t, "stddev by (job) (requests)"))

		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "100=1;") ||
			strings.Contains(body, "200=") || !strings.Contains(body, "1 incomplete") {
			t.Fatalf("status=%d body=%q", w.Code, body)
		}
		if got := w.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "status=phit") {
			t.Fatalf("result header: %q", got)
		}
	})
}

func BenchmarkServePooledVariance(b *testing.B) {
	specs := make([]pooledVarianceMemberSpec, 32)
	for i := range specs {
		specs[i] = pooledVarianceMemberSpec{
			count: "100", mean: strconv.Itoa(i), variance: "4",
			tags: dataset.Tags{"job": "api", "shard": strconv.Itoa(i)},
		}
	}
	p := newPooledVariancePool(specs, "shard")
	defer p.Stop()
	albpool.WaitHealthy(b, p, len(specs))
	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)

	for b.Loop() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, newWeightedAvgRequest(b, "stdvar by (job) (requests)"))
		if w.Code != http.StatusOK {
			b.Fatalf("status=%d", w.Code)
		}
	}
}
