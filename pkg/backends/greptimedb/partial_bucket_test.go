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

package greptimedb

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

const partialBucketDB = "public"

func sqlRequest(t *testing.T, h *httpHarness, method, statement string) *http.Request {
	t.Helper()
	values := url.Values{"sql": {statement}, "db": {partialBucketDB}}
	var r *http.Request
	if method == http.MethodPost {
		r = httptest.NewRequest(method, "http://trickster/v1/sql", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, "http://trickster/v1/sql?"+values.Encode(), nil)
	}
	// the request as the handler sends it upstream
	r.URL = urls.BuildUpstreamURL(r, h.client.BaseUpstreamURL())
	res := h.resources
	return request.SetResources(r, request.NewResources(res.BackendOptions, res.PathConfig, res.CacheConfig,
		res.CacheClient, h.client, res.Tracer))
}

func TestFetchPartialBucket(t *testing.T) {
	start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
	statement := liveRange(start.Add(7*time.Minute), start.Add(67*time.Minute))
	pb := timeseries.PartialBucket{
		Label: start, Lower: start.Add(7 * time.Minute), Upper: start.Add(15 * time.Minute),
		Edge: timeseries.BucketEdgeStart,
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			origin := &httpOrigin{}
			h := newHTTPHarness(t, origin)
			r := sqlRequest(t, h, method, statement)
			trq, _, _, err := h.client.ParseTimeRangeQuery(r)
			if err != nil {
				t.Fatal(err)
			}
			// the bucket's own raw range is sent once, then answered by the object cache
			for _, want := range []status.LookupStatus{status.LookupStatusKeyMiss, status.LookupStatusHit} {
				rq, err := engines.PartialBucketRequest(r.Context(), r)
				if err != nil {
					t.Fatal(err)
				}
				ts, got, err := h.client.FetchPartialBucket(rq, trq, pb, false)
				if err != nil || got != want {
					t.Fatalf("partial bucket = %+v, %s, %v; want %s", ts, got, err, want)
				}
				// the model keeps its schema alongside the rows
				ds := ts.(dataset.Based).Base()
				if _, wrapped := ts.(*dataset.DataSet); wrapped || len(ds.Results) != 1 ||
					len(ds.Results[0].SeriesList) != 1 {
					t.Fatalf("partial bucket = %T %+v", ts, ds)
				}
				points := ds.Results[0].SeriesList[0].Points
				if len(points) != 1 || int64(points[0].Epoch) != start.UnixNano() {
					t.Fatalf("partial bucket points = %+v", points)
				}
			}
			sent := origin.snapshot()
			if len(sent) != 1 || !strings.Contains(sent[0], start.Add(7*time.Minute).Format(time.RFC3339Nano)) ||
				!strings.Contains(sent[0], start.Add(15*time.Minute).Format(time.RFC3339Nano)) {
				t.Fatalf("origin statements = %v", sent)
			}
			// the client's request is left as it was
			if got := sqlOf(t, r); got != statement {
				t.Fatalf("the request's statement changed to %q", got)
			}
		})
	}

	t.Run("failures", func(t *testing.T) {
		h := newHTTPHarness(t, &httpOrigin{})
		r := sqlRequest(t, h, http.MethodGet, statement)
		trq, _, _, err := h.client.ParseTimeRangeQuery(r)
		if err != nil {
			t.Fatal(err)
		}
		exclusive := pb
		exclusive.LowerExclusive = true
		for name, call := range map[string]func() error{
			"unrenderable range": func() error { _, _, err := h.client.FetchPartialBucket(r, trq, exclusive, false); return err },
			"no plan": func() error {
				_, _, err := h.client.FetchPartialBucket(r, &timeseries.TimeRangeQuery{}, pb, false)
				return err
			},
			"no query":   func() error { _, _, err := h.client.FetchPartialBucket(r, nil, pb, false); return err },
			"no request": func() error { _, _, err := h.client.FetchPartialBucket(nil, trq, pb, false); return err },
		} {
			if err := call(); err == nil {
				t.Errorf("%s: fetched", name)
			}
		}
		// a PromQL range query has only its live point, which it fetches as Fast Forward
		prom := httptest.NewRequest(http.MethodGet, "http://trickster"+promPath+"/api/v1/query_range", nil)
		if _, _, err := h.client.FetchPartialBucket(prom, trq, pb, false); err != backends.ErrPartialBucketsUnsupported {
			t.Errorf("a PromQL partial start bucket: %v", err)
		}
	})
}

func sqlOf(t *testing.T, r *http.Request) string {
	t.Helper()
	if statement := r.URL.Query().Get("sql"); statement != "" {
		return statement
	}
	body, err := request.GetBody(r)
	if err != nil {
		t.Fatal(err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		t.Fatal(err)
	}
	return values.Get("sql")
}

func TestHTTPSQLPartialBucketFlow(t *testing.T) {
	// under partial, the complete buckets are delta cached and each edge's partial bucket is its own
	// object, fetched over the client's raw range
	origin := &httpOrigin{}
	h := newHTTPHarness(t, origin)
	h.resources.BackendOptions.StepAlignment = timeseries.StepAlignmentPartial
	start := time.Now().UTC().Truncate(15 * time.Minute).Add(-3 * time.Hour)
	statement := liveRange(start.Add(7*time.Minute), start.Add(67*time.Minute))
	for _, want := range []string{"kmiss", "hit"} {
		w := h.query(t, http.MethodGet, statement, nil, nil)
		assertHTTPResult(t, w, "DeltaProxyCache", want, 5)
		result := w.Header().Get(headers.NameTricksterResult)
		if !strings.Contains(result, ":start:"+want) || !strings.Contains(result, ":end:"+want) {
			t.Fatalf("partial buckets = %s", result)
		}
	}
	if sent := origin.snapshot(); len(sent) != 3 {
		t.Fatalf("origin statements = %v, want the complete buckets and one per edge", sent)
	}
}

func TestPrometheusRangeQueriesApplyDrop(t *testing.T) {
	// a configured drop reaches PromQL range queries, as Prometheus's own
	h := newHTTPHarness(t, &httpOrigin{})
	r := httptest.NewRequest(http.MethodGet, "http://trickster"+promPath+"/api/v1/query_range?"+url.Values{
		"query": {"up"}, "start": {"0"}, "end": {"600"}, "step": {"60"},
	}.Encode(), nil)
	res := h.resources
	r = request.SetResources(r, request.NewResources(res.BackendOptions, res.PathConfig, res.CacheConfig,
		res.CacheClient, h.client, res.Tracer))
	trq, _, _, err := h.client.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	if trq.StepAlignments&timeseries.StepAlignmentDrop == 0 || trq.StepAlignments&timeseries.StepAlignmentPartialEnd == 0 {
		t.Fatalf("PromQL modes = %s", trq.StepAlignments)
	}
	if unsupported := trq.ResolveStepAlignment(0, timeseries.StepAlignmentDrop); unsupported != 0 ||
		trq.StepAlignment != timeseries.StepAlignmentDrop {
		t.Fatalf("a configured drop resolved to %s (unsupported %s)", trq.StepAlignment, unsupported)
	}
}
