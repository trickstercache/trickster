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
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	prommodel "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

type prometheusLimitRatioBackend struct {
	*prometheus.Client
	cfg *bo.Options
}

func (b *prometheusLimitRatioBackend) Configuration() *bo.Options { return b.cfg }

func limitRatioBackend(labels map[string]string) *prometheusLimitRatioBackend {
	var cfg *bo.Options
	if labels != nil {
		cfg = &bo.Options{Prometheus: &prop.Options{Labels: labels}}
	}
	return &prometheusLimitRatioBackend{Client: &prometheus.Client{}, cfg: cfg}
}

func limitRatioMemberHandler(values map[string]string, extraTags dataset.Tags,
	qr *queryRecorder,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryValues, _, _ := params.GetRequestValues(r)
		query := queryValues.Get("query")
		if qr != nil {
			qr.Append(query)
		}

		seriesList := make(dataset.SeriesList, 0, len(values))
		for service, value := range values {
			tags := dataset.Tags{"service": service}
			tags.Merge(extraTags.Clone())
			seriesList = append(seriesList, &dataset.Series{
				Header: dataset.SeriesHeader{
					Name:           "requests",
					Tags:           tags,
					QueryStatement: query,
				},
				Points: dataset.Points{{
					Epoch:  epoch.Epoch(100),
					Size:   32,
					Values: []any{value},
				}},
			})
		}

		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = &dataset.DataSet{Results: dataset.Results{{SeriesList: seriesList}}}
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = limitRatioRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
}

func limitRatioHistogramMemberHandler(histogram string, qr *queryRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryValues, _, _ := params.GetRequestValues(r)
		query := queryValues.Get("query")
		if qr != nil {
			qr.Append(query)
		}

		resultValue := `"histogram":[100,` + histogram + `]`
		if strings.HasPrefix(strings.TrimSpace(query), "count") {
			resultValue = `"value":[100,"1"]`
		}
		body := `{"status":"success","data":{"resultType":"vector","result":[{` +
			`"metric":{"service":"api"},` + resultValue + `}]}}`
		ts, err := prommodel.UnmarshalTimeseries([]byte(body), &timeseries.TimeRangeQuery{
			Statement: query,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = ts
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = limitRatioRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
}

func limitRatioCaptureFailureHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = limitRatioRespondFunc
		}
		_, _ = w.Write([]byte(strings.Repeat("x", 256)))
	})
}

func limitRatioRespondFunc(w http.ResponseWriter, _ *http.Request,
	accum *merge.Accumulator, statusCode int,
) {
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ds, _ := accum.GetTSData().(*dataset.DataSet)
	parts := make([]string, 0)
	if ds != nil {
		for _, result := range ds.Results {
			if result == nil {
				continue
			}
			for _, series := range result.SeriesList {
				if series == nil || len(series.Points) == 0 || len(series.Points[0].Values) == 0 {
					continue
				}
				name := series.Header.Tags["service"]
				if replica := series.Header.Tags["replica"]; replica != "" {
					name += "[" + replica + "]"
				}
				parts = append(parts, fmt.Sprintf("%s=%v", name,
					series.Points[0].Values[0]))
			}
		}
	}
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(strings.Join(parts, ",") + "|warnings=" +
		strings.Join(dsWarnings(ds), ",")))
}

func newLimitRatioTarget(h http.Handler, backend *prometheusLimitRatioBackend) *pool.Target {
	status := &healthcheck.Status{}
	status.Set(healthcheck.StatusPassing)
	return pool.NewTarget(h, status, backend)
}

func serveLimitRatio(t *testing.T, targets pool.Targets, query string,
	configure ...func(*handler),
) *httptest.ResponseRecorder {
	t.Helper()
	p := pool.New(targets, -1)
	p.RefreshHealthy()
	t.Cleanup(p.Stop)

	h := &handler{mergePaths: []string{"/"}}
	for _, apply := range configure {
		apply(h)
	}
	h.SetPool(p)
	req := newTestMergeRequest(t)
	req.URL.RawQuery = "query=" + url.QueryEscape(query)
	rsc := request.GetResources(req)
	rsc.TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: query}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestTSMLimitRatioMergesInnerAggregationBeforeSampling(t *testing.T) {
	logger.SetLogger(testLogger)
	const (
		query = "sort_desc(limit_ratio(0.5, count by (service) (requests)))"
		inner = "count by (service) (requests)"
	)
	qr := &queryRecorder{}
	targets := pool.Targets{
		newLimitRatioTarget(limitRatioMemberHandler(
			map[string]string{"c": "2", "d": "10"},
			dataset.Tags{"region": "a"}, qr),
			limitRatioBackend(map[string]string{"region": "a"})),
		newLimitRatioTarget(limitRatioMemberHandler(
			map[string]string{"c": "3", "d": "1"},
			dataset.Tags{"region": "b"}, qr),
			limitRatioBackend(map[string]string{"region": "b"})),
	}

	w := serveLimitRatio(t, targets, query)

	if w.Code != http.StatusOK {
		t.Fatalf("status got %d want 200 body=%q", w.Code, w.Body.String())
	}
	if got, want := w.Body.String(), "d=11|warnings="; got != want {
		t.Fatalf("body got %q want %q", got, want)
	}
	queries := qr.Queries()
	if len(queries) != 2 {
		t.Fatalf("fanout queries got %v want two entries", queries)
	}
	for _, got := range queries {
		if got != inner {
			t.Fatalf("fanout query got %q want %q", got, inner)
		}
	}
}

func TestTSMLimitRatioMergesInnerFloatSumBeforeSampling(t *testing.T) {
	logger.SetLogger(testLogger)
	const (
		query = "limit_ratio(-1, sum by (service) (requests))"
		inner = "sum by (service) (requests)"
	)
	qr := &queryRecorder{}
	targets := pool.Targets{
		newLimitRatioTarget(limitRatioMemberHandler(
			map[string]string{"c": "2", "d": "10"}, nil, qr),
			limitRatioBackend(nil)),
		newLimitRatioTarget(limitRatioMemberHandler(
			map[string]string{"c": "3", "d": "1"}, nil, qr),
			limitRatioBackend(nil)),
	}

	w := serveLimitRatio(t, targets, query)

	if got, want := w.Body.String(), "c=5,d=11|warnings="; got != want {
		t.Fatalf("body got %q want %q", got, want)
	}
	queries := qr.Queries()
	if len(queries) != 2 {
		t.Fatalf("fanout queries got %v want two entries", queries)
	}
	for _, got := range queries {
		if got != inner {
			t.Fatalf("fanout query got %q want %q", got, inner)
		}
	}
}

func TestTSMLimitRatioHistogramInnerAggregationsUseExactReduction(t *testing.T) {
	logger.SetLogger(testLogger)
	histograms := []string{
		`{"count":"10","sum":"3.14","schema":3,"zero_threshold":0.001,"zero_count":"2",` +
			`"positive_spans":[{"offset":0,"length":1}],"positive_deltas":[1]}`,
		`{"count":"20","sum":"6.28","schema":3,"zero_threshold":0.001,"zero_count":"4",` +
			`"positive_spans":[{"offset":0,"length":1}],"positive_deltas":[2]}`,
	}
	for _, operator := range []string{"sum", "avg"} {
		t.Run(operator, func(t *testing.T) {
			query := "limit_ratio(-1, " + operator + " by (service) (native_histogram))"
			qr := &queryRecorder{}
			targets := pool.Targets{
				newLimitRatioTarget(limitRatioHistogramMemberHandler(histograms[0], qr), limitRatioBackend(nil)),
				newLimitRatioTarget(limitRatioHistogramMemberHandler(histograms[1], qr), limitRatioBackend(nil)),
			}

			w := serveLimitRatio(t, targets, query)
			body := w.Body.String()
			if w.Code != http.StatusOK || strings.Contains(body, "NaN") {
				t.Fatalf("status=%d body=%q", w.Code, body)
			}
			if strings.Contains(body, "warnings=PromQL") ||
				strings.Contains(body, "native-histogram-aware") {
				t.Fatalf("body has unexpected warning: %q", body)
			}
			value := strings.TrimSuffix(strings.TrimPrefix(body, "api="), "|warnings=")
			var histogram struct {
				Count   string  `json:"count"`
				Sum     string  `json:"sum"`
				Buckets [][]any `json:"buckets"`
			}
			if err := json.Unmarshal([]byte(value), &histogram); err != nil {
				t.Fatalf("decode merged histogram %q: %v", value, err)
			}
			wantCount, wantSum := "30", "9.42"
			wantZero, wantPositive := "6", "3"
			if operator == "avg" {
				wantCount, wantSum = "15", "4.71"
				wantZero, wantPositive = "3", "1.5"
			}
			if histogram.Count != wantCount || histogram.Sum != wantSum ||
				len(histogram.Buckets) != 2 ||
				histogram.Buckets[0][3] != wantZero ||
				histogram.Buckets[1][3] != wantPositive {
				t.Fatalf("merged histogram got %#v", histogram)
			}

			queries := qr.Queries()
			wantQueries := map[string]int{
				"sum by (service) (native_histogram)": 2,
			}
			if operator == "avg" {
				wantQueries["count by (service) (native_histogram)"] = 2
			}
			gotQueries := make(map[string]int, len(wantQueries))
			for _, got := range queries {
				gotQueries[got]++
			}
			if !maps.Equal(gotQueries, wantQueries) {
				t.Fatalf("fanout queries got %v want %v", gotQueries, wantQueries)
			}
		})
	}
}

func TestTSMLimitRatioStripsInjectedLabelsBeforeHADedup(t *testing.T) {
	logger.SetLogger(testLogger)
	const query = "limit_ratio(-1, up)"
	qr := &queryRecorder{}
	targets := pool.Targets{
		newLimitRatioTarget(
			limitRatioMemberHandler(map[string]string{"api": "1"},
				dataset.Tags{"replica": "a"}, qr),
			limitRatioBackend(map[string]string{"replica": "a"}),
		),
		newLimitRatioTarget(
			limitRatioMemberHandler(map[string]string{"api": "1"},
				dataset.Tags{"replica": "b"}, qr),
			limitRatioBackend(map[string]string{"replica": "b"}),
		),
	}

	w := serveLimitRatio(t, targets, query)

	if got, want := w.Body.String(), "api=1|warnings="; got != want {
		t.Fatalf("body got %q want %q", got, want)
	}
	queries := qr.Queries()
	if len(queries) != 2 || queries[0] != query || queries[1] != query {
		t.Fatalf("fanout queries got %v want two unchanged queries", queries)
	}
}

func TestTSMLimitRatioSingleMemberBypassRespectsLabelCleanup(t *testing.T) {
	logger.SetLogger(testLogger)
	const query = "limit_ratio(-1, up)"

	t.Run("no configured labels uses direct proxy", func(t *testing.T) {
		direct := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("direct"))
		})
		w := serveLimitRatio(t, pool.Targets{
			newLimitRatioTarget(direct, limitRatioBackend(nil)),
		}, query)
		if got, want := w.Body.String(), "direct"; got != want {
			t.Fatalf("body got %q want %q", got, want)
		}
	})

	t.Run("configured labels force merge cleanup", func(t *testing.T) {
		w := serveLimitRatio(t, pool.Targets{
			newLimitRatioTarget(
				limitRatioMemberHandler(map[string]string{"api": "1"},
					dataset.Tags{"replica": "a"}, nil),
				limitRatioBackend(map[string]string{"replica": "a"}),
			),
		}, query)
		if got, want := w.Body.String(), "api=1|warnings="; got != want {
			t.Fatalf("body got %q want %q", got, want)
		}
	})
}

func TestTSMLimitRatioIsIndependentOfPoolOrder(t *testing.T) {
	logger.SetLogger(testLogger)
	const query = "sort(limit_ratio(-1, up))"
	backend := limitRatioBackend(nil)
	handlers := []http.Handler{
		limitRatioMemberHandler(map[string]string{"api": "2"}, nil, nil),
		limitRatioMemberHandler(map[string]string{"worker": "1"}, nil, nil),
	}
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		targets := pool.Targets{
			newLimitRatioTarget(handlers[order[0]], backend),
			newLimitRatioTarget(handlers[order[1]], backend),
		}
		w := serveLimitRatio(t, targets, query)
		if got, want := w.Body.String(), "worker=1,api=2|warnings="; got != want {
			t.Fatalf("order %v body got %q want %q", order, got, want)
		}
	}
}

func TestTSMLimitRatioSurfacesPartialAndCaptureFailures(t *testing.T) {
	logger.SetLogger(testLogger)
	const query = "limit_ratio(-1, up)"
	backend := limitRatioBackend(nil)
	tests := []struct {
		name      string
		failed    http.Handler
		configure func(*handler)
	}{
		{"status", stubFailHandler(http.StatusInternalServerError), nil},
		{"capture", limitRatioCaptureFailureHandler(), func(h *handler) {
			h.maxCaptureBytes = 32
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targets := pool.Targets{
				newLimitRatioTarget(limitRatioMemberHandler(
					map[string]string{"api": "1"}, nil, nil), backend),
				newLimitRatioTarget(tt.failed, backend),
			}
			var configure []func(*handler)
			if tt.configure != nil {
				configure = append(configure, tt.configure)
			}
			w := serveLimitRatio(t, targets, query, configure...)

			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "api=1") {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "tsm partial failure") {
				t.Fatalf("body missing partial warning: %q", w.Body.String())
			}
			if got := w.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "phit") {
				t.Fatalf("result header got %q want phit", got)
			}
		})
	}
}
