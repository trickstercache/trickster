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

type quantileMemberSpec struct {
	replica string
	values  map[string]string
	fail    bool
}

func quantileMemberHandler(spec quantileMemberSpec, recorder *queryRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, _, _ := params.GetRequestValues(r)
		query := values.Get("query")
		recorder.Append(query)
		if spec.fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		seriesList := make(dataset.SeriesList, 0, len(spec.values))
		for instance, value := range spec.values {
			seriesList = append(seriesList, &dataset.Series{
				Header: dataset.SeriesHeader{
					Name:           "up",
					Tags:           dataset.Tags{"__name__": "up", "instance": instance, "job": "api", "replica": spec.replica},
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
		seriesList.SortByTags()

		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = &dataset.DataSet{
				Status:         "success",
				TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: query},
				Results: dataset.Results{{
					StatementID: 1,
					SeriesList:  seriesList,
				}},
			}
			rsc.MergeFunc = responsemerge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = responsemerge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = quantileRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
}

func quantileRespondFunc(w http.ResponseWriter, _ *http.Request, accum *responsemerge.Accumulator,
	statusCode int,
) {
	headers.StripMergeHeaders(w.Header())
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ds, _ := accum.GetTSData().(*dataset.DataSet)
	w.WriteHeader(statusCode)
	if ds == nil || len(ds.Results) == 0 || ds.Results[0] == nil ||
		len(ds.Results[0].SeriesList) == 0 {
		_, _ = w.Write([]byte("MERGED:empty|warnings=" + strings.Join(dsWarnings(ds), ",")))
		return
	}
	series := ds.Results[0].SeriesList[0]
	value := ""
	if series != nil && len(series.Points) > 0 && len(series.Points[0].Values) > 0 {
		value = formatAny(series.Points[0].Values[0])
	}
	_, _ = w.Write([]byte("MERGED:tags=" + series.Header.Tags.JSON() + "|value=" + value +
		"|warnings=" + strings.Join(dsWarnings(ds), ",")))
}

func newQuantilePool(specs []quantileMemberSpec, recorder *queryRecorder) pool.Pool {
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
		targets[i] = pool.NewTarget(quantileMemberHandler(spec, recorder), status, backend)
	}
	p := pool.New(targets, -1)
	p.RefreshHealthy()
	return p
}

func TestServeQuantileGloballyFinalizesMergedInnerVector(t *testing.T) {
	logger.SetLogger(testLogger)
	const query = "quantile by (job) (0.5, up)"
	recorder := &queryRecorder{}
	p := newQuantilePool([]quantileMemberSpec{
		{replica: "a", values: map[string]string{"one": "1", "three": "3"}},
		{replica: "b", values: map[string]string{"hundred": "100"}},
		// This exact HA copy must be deduplicated after its routing label is stripped.
		{replica: "c", values: map[string]string{"one": "1", "three": "3"}},
	}, recorder)
	defer p.Stop()
	albpool.WaitHealthy(t, p, 3)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWeightedAvgRequest(t, query))

	if w.Code != http.StatusOK || w.Body.String() !=
		`MERGED:tags={"job":"api"}|value=3|warnings=` {
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
}

func TestServeQuantilePartialFanoutIsMarkedIncomplete(t *testing.T) {
	logger.SetLogger(testLogger)
	recorder := &queryRecorder{}
	p := newQuantilePool([]quantileMemberSpec{
		{replica: "a", values: map[string]string{"one": "1"}},
		{replica: "b", fail: true},
	}, recorder)
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newWeightedAvgRequest(t, "quantile(0.5, up)"))

	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "value=1") ||
		!strings.Contains(body, "partial failure") {
		t.Fatalf("status=%d body=%q", w.Code, body)
	}
}
