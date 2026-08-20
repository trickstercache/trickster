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

package routing

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	cacheRegistry "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

func TestGrafanaOriginRoutesDataSourceProxy(t *testing.T) {
	var queryRequests atomic.Int32
	now := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/datasources":
			fmt.Fprint(w, `[{"id":1,"uid":"prom-main","orgId":1,"type":"prometheus","access":"proxy","url":"http://prometheus:9090"}]`)
		case "/api/datasources/uid/prom-main":
			fmt.Fprint(w, `{"id":1,"uid":"prom-main","orgId":1,"type":"prometheus","access":"proxy","url":"http://prometheus:9090"}`)
		case "/api/datasources/proxy/uid/prom-main/api/v1/query_range":
			queryRequests.Add(1)
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up"},"values":[[%d,"1"]]}]}}`, now.Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	conf, err := config.Load([]string{"-log-level", "error", "-origin-url", origin.URL,
		"-provider", providers.Grafana})
	if err != nil {
		t.Fatal(err)
	}
	caches := cacheRegistry.LoadCachesFromConfig(conf)
	defer cacheRegistry.CloseCaches(caches)
	clients := make(backends.Backends)
	frontendRouter := lm.NewRouter()
	metricsRouter := lm.NewRouter()
	if err := RegisterProxyRoutes(conf, clients, frontendRouter, metricsRouter,
		caches, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := RegisterProxyRoutes(conf, clients, frontendRouter, metricsRouter,
		caches, nil, false); err != nil {
		t.Fatal(err)
	}
	RegisterDefaultBackendRoutes(frontendRouter, conf, clients, nil)

	query := url.Values{
		"query": {"up"},
		"start": {fmt.Sprint(now.Unix())},
		"end":   {fmt.Sprint(now.Unix())},
		"step":  {"60"},
	}.Encode()
	path := "/api/datasources/proxy/uid/prom-main/api/v1/query_range?" + query
	wants := []string{"kmiss", "hit"}
	for i, want := range wants {
		r := httptest.NewRequest(http.MethodGet, "http://trickster.example"+path, nil)
		r.Header.Set("Cookie", "grafana_session=user-a")
		w := httptest.NewRecorder()
		frontendRouter.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200; body=%s", i+1, w.Code, w.Body.String())
		}
		if got := w.Header().Get(headers.NameTricksterResult); !strings.Contains(got, want) {
			t.Fatalf("request %d result header = %q, want %s", i+1, got, want)
		}
	}
	if got := queryRequests.Load(); got != 1 {
		t.Fatalf("Grafana data proxy requests = %d, want 1", got)
	}
}
