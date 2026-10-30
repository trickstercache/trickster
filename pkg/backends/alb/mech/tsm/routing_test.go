/*
 * Copyright 2026 The Trickster Authors
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
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	prommodel "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func testMergePaths(paths ...string) []mergePath {
	out := make([]mergePath, len(paths))
	for i, path := range paths {
		out[i] = mergePath{path: path, prefix: strings.HasSuffix(path, "/")}
	}
	return out
}

func newRoutingHandler(t *testing.T, provider string) *handler {
	t.Helper()
	m, err := New(Config{OutputFormat: provider}, rt.Lookup{
		providers.Prometheus: prometheus.NewClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.(*handler)
}

func TestTSMRouteEligibility(t *testing.T) {
	h := newRoutingHandler(t, providers.Prometheus)

	for _, name := range []string{"query", "query_range", "series", "labels", "alerts", "label/job/values", "label/U__http_2e_status/values"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, methods.MethodQuery, http.MethodDelete, http.MethodHead, http.MethodOptions} {
			path := "/api/v1/" + name
			want := slices.Contains(methods.QueryableMethods(), method)
			if name == "alerts" || strings.HasPrefix(name, "label/") {
				want = method == http.MethodGet
			}
			got := slices.ContainsFunc(h.mergePaths, func(p mergePath) bool { return p.matches(method, path) })
			if got != want {
				t.Errorf("%s %s: mergeable = %v, want %v", method, path, got, want)
			}
		}
	}
}

func TestTSMExcludedRoutesUseOneMember(t *testing.T) {
	h := newRoutingHandler(t, providers.Prometheus)
	var calls, mergeCalls atomic.Int32
	const body = `{"status":"success","data":[{"seriesLabels":{"__name__":"up"},"exemplars":[]}]}`
	member := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if rsc := request.GetResources(r); rsc != nil && rsc.IsMergeMember {
			mergeCalls.Add(1)
		}
		w.Header().Set("X-Origin", "first")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, body)
	})
	p, _, _ := albpool.NewHealthy([]http.Handler{member, member})
	defer p.Stop()
	h.SetPool(p)
	paths := []string{
		"/api/v1/query_exemplars", "/api/v1/query_extra", "/api/v1/query/anything",
		"/api/v1/query_range/anything", "/api/v1/series_extra", "/api/v1/labels_extra",
		"/api/v1/alerts_extra", "/api/v1/label/", "/api/v1/label/job",
		"/api/v1/label/job/other", "/api/v1/label/job/values/extra", "/api/v1/label/a/b/values",
		"/api/v1/label//values", "/api/v1/read", "/api/v1/write", "/api/v1/openapi.yaml",
		"/api/v1/status/config", "/api/v1/admin/tsdb/delete_series", "/metrics", "/-/healthy",
	}
	for _, route := range prometheus.SupportedPaths(nil) {
		if !slices.Contains(prometheus.MergeablePaths(), route.Path) {
			paths = append(paths, route.Path)
		}
	}
	for _, path := range prometheus.MergeablePaths() {
		if !strings.HasSuffix(path, "/") {
			paths = append(paths, path+"/")
		}
	}
	for _, path := range paths {
		for _, method := range []string{http.MethodGet, http.MethodPost, methods.MethodQuery} {
			t.Run(method+path, func(t *testing.T) {
				calls.Store(0)
				mergeCalls.Store(0)
				r := httptest.NewRequest(method, path, strings.NewReader("query=up"))
				r = request.SetResources(r, request.NewResources(nil, nil, nil, nil, nil, nil))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if calls.Load() != 1 || mergeCalls.Load() != 0 {
					t.Fatalf("calls=%d merge calls=%d", calls.Load(), mergeCalls.Load())
				}
				if w.Code != http.StatusAccepted || w.Body.String() != body || w.Header().Get("X-Origin") != "first" {
					t.Fatalf("response not preserved: %d %s %v", w.Code, w.Body.String(), w.Header())
				}
			})
		}
	}
}

func TestTSMSupportedRoutesFanOut(t *testing.T) {
	h := newRoutingHandler(t, providers.Prometheus)
	var calls atomic.Int32
	member := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		rsc := request.GetResources(r)
		if rsc != nil && rsc.IsMergeMember {
			rsc.MergeFunc = prommodel.MergeAndWriteLabelDataMergeFunc()
			rsc.MergeRespondFunc = prommodel.MergeAndWriteLabelDataRespondFunc()
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":["job"]}`)
	})
	p, _, _ := albpool.NewHealthy([]http.Handler{member, member})
	defer p.Stop()
	h.SetPool(p)
	for _, route := range h.mergePaths {
		path := route.path
		if route.prefix {
			path += "job/values"
		}
		calls.Store(0)
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r = request.SetResources(r, request.NewResources(nil, nil, nil, nil, nil, nil))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || calls.Load() != 2 || !strings.Contains(w.Body.String(), `"job"`) {
			t.Errorf("%s: status=%d calls=%d body=%s", path, w.Code, calls.Load(), w.Body.String())
		}
	}
}

func TestTSMUnsupportedMergeMethodsUseOneMember(t *testing.T) {
	h := newRoutingHandler(t, providers.Prometheus)
	var calls, mergeCalls atomic.Int32
	member := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if rsc := request.GetResources(r); rsc != nil && rsc.IsMergeMember {
			mergeCalls.Add(1)
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	p, _, _ := albpool.NewHealthy([]http.Handler{member, member})
	defer p.Stop()
	h.SetPool(p)
	for _, route := range h.mergePaths {
		path := route.path
		if route.prefix {
			path += "job/values"
		}
		for _, method := range methods.AllHTTPMethods() {
			if slices.Contains(route.methods, method) {
				continue
			}
			t.Run(method+path, func(t *testing.T) {
				calls.Store(0)
				mergeCalls.Store(0)
				r := httptest.NewRequest(method, path, nil)
				r = request.SetResources(r, request.NewResources(nil, nil, nil, nil, nil, nil))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if calls.Load() != 1 || mergeCalls.Load() != 0 || w.Code != http.StatusMethodNotAllowed {
					t.Fatalf("status=%d calls=%d merge calls=%d", w.Code, calls.Load(), mergeCalls.Load())
				}
			})
		}
	}
}
