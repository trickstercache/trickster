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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dspoints"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

const (
	presenceRegionLabel = "region"
	presencePodLabel    = "pod"
	presenceEast        = "east"
	presenceWest        = "west"
	presencePodA        = "a"
	presencePodB        = "b"
	presencePodC        = "c"
	presenceInner       = "count by (pod) (up)"
	presenceQuery       = "count(" + presenceInner + ")"
)

// presenceCountBody is the respond func's body for a merged count of n
func presenceCountBody(n int) string {
	return "{}=" + strconv.Itoa(n)
}

// prometheusLabeledBackend plans and finalizes as Prometheus does, with a region label injected
type prometheusLabeledBackend struct {
	*prometheus.Client
	cfg *bo.Options
}

func (b *prometheusLabeledBackend) Configuration() *bo.Options { return b.cfg }

type presenceMember struct {
	region string
	pods   []string
	status int
}

// presenceMemberHandler answers the inner count as a member with an injected region label would
func presenceMemberHandler(member presenceMember, qr *queryRecorder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, _, _ := params.GetRequestValues(r)
		query := values.Get("query")
		qr.Append(query)
		if member.status != http.StatusOK {
			w.WriteHeader(member.status)
			return
		}
		list := make(dataset.SeriesList, 0, len(member.pods))
		for _, pod := range member.pods {
			list = append(list, dataset.NewSeries(dataset.SeriesHeader{
				Tags:           dataset.Tags{presencePodLabel: pod, presenceRegionLabel: member.region},
				QueryStatement: query,
				ValueFieldsList: timeseries.FieldDefinitions{{
					Name: "value", DataType: timeseries.String,
				}},
			}, dataset.Points{{Epoch: epoch.Epoch(100), Values: []any{"2"}}}))
		}
		if rsc := request.GetResources(r); rsc != nil {
			rsc.TS = &dataset.DataSet{
				TimeRangeQuery: &timeseries.TimeRangeQuery{Statement: query},
				Results:        dataset.Results{{SeriesList: list}},
			}
			rsc.MergeFunc = merge.TimeseriesMergeFuncWithStrategy(nil, rsc.TSMergeStrategy)
			rsc.BatchMergeFunc = merge.TimeseriesBatchMergeFuncWithStrategy(rsc.TSMergeStrategy)
			rsc.MergeRespondFunc = presenceRespondFunc
		}
		w.WriteHeader(http.StatusOK)
	})
}

func presenceRespondFunc(w http.ResponseWriter, _ *http.Request, accum *merge.Accumulator, statusCode int) {
	headers.StripMergeHeaders(w.Header())
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
		for _, point := range dspoints.Of(series) {
			parts = append(parts, series.Header.Tags.JSON()+"="+formatAny(point.Values[0]))
		}
	}
	_, _ = w.Write([]byte(strings.Join(parts, ",")))
}

func servePresence(t *testing.T, query string, members ...presenceMember) (*httptest.ResponseRecorder, []string) {
	t.Helper()
	logger.SetLogger(testLogger)
	qr := &queryRecorder{}
	targets := make(pool.Targets, len(members))
	for i, member := range members {
		be := &prometheusLabeledBackend{Client: &prometheus.Client{}, cfg: &bo.Options{
			Name:       presenceRegionLabel + "-" + member.region,
			Prometheus: &prop.Options{Labels: map[string]string{presenceRegionLabel: member.region}},
		}}
		status := &healthcheck.Status{}
		status.Set(healthcheck.StatusPassing)
		targets[i] = pool.NewTarget(presenceMemberHandler(member, qr), status, be)
	}
	p := pool.New(targets, -1)
	t.Cleanup(p.Stop)
	p.RefreshHealthy()

	h := &handler{mergePaths: []string{"/"}}
	h.SetPool(p)
	req := newTestMergeRequest(t)
	req.URL.RawQuery = "query=" + url.QueryEscape(query)
	request.GetResources(req).TimeRangeQuery = &timeseries.TimeRangeQuery{Statement: query}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w, qr.Queries()
}

func TestServePresenceUnionsInnerLabelSets(t *testing.T) {
	east := presenceMember{region: presenceEast, pods: []string{presencePodA, presencePodB}, status: http.StatusOK}
	t.Run("injected labels are removed before the union", func(t *testing.T) {
		w, queries := servePresence(t, presenceQuery, east,
			presenceMember{region: presenceWest, pods: []string{presencePodB, presencePodC}, status: http.StatusOK},
		)
		if w.Code != http.StatusOK || w.Body.String() != presenceCountBody(3) {
			t.Fatalf("got %d %q, want 200 %s", w.Code, w.Body.String(), presenceCountBody(3))
		}
		for _, got := range queries {
			if got != presenceInner {
				t.Fatalf("fanout queries %v; want the inner count", queries)
			}
		}
	})
	t.Run("one member is still rewritten and finalized", func(t *testing.T) {
		w, queries := servePresence(t, presenceQuery, east)
		if w.Body.String() != presenceCountBody(2) || len(queries) != 1 || queries[0] != presenceInner {
			t.Fatalf("got %q with queries %v", w.Body.String(), queries)
		}
	})
	t.Run("a failed member leaves a partial union", func(t *testing.T) {
		w, _ := servePresence(t, presenceQuery, east,
			presenceMember{region: presenceWest, status: http.StatusServiceUnavailable},
		)
		if w.Body.String() != presenceCountBody(2) {
			t.Fatalf("body %q; want the union of the live member", w.Body.String())
		}
		if got := w.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "status=phit") {
			t.Fatalf("%s header %q; want the partial-hit marker", headers.NameTricksterResult, got)
		}
	})
}
