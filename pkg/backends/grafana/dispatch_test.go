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

package grafana

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	registrytypes "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	cachepkg "github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

func TestParseDataSourceProxyPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		want      dataSourceRef
		wantMatch bool
	}{
		{
			name: "uid",
			path: "/api/datasources/proxy/uid/prom-main/api/v1/query_range",
			want: dataSourceRef{
				kind:        dataSourceRefUID,
				value:       "prom-main",
				proxyPrefix: "/api/datasources/proxy/uid/prom-main",
				innerPath:   "/api/v1/query_range",
			},
			wantMatch: true,
		},
		{
			name: "numeric id",
			path: "/api/datasources/proxy/42/query",
			want: dataSourceRef{
				kind:        dataSourceRefID,
				value:       "42",
				proxyPrefix: "/api/datasources/proxy/42",
				innerPath:   "/query",
			},
			wantMatch: true,
		},
		{
			name:      "datasource root",
			path:      "/api/datasources/proxy/uid/prom-main",
			want:      dataSourceRef{kind: dataSourceRefUID, value: "prom-main", proxyPrefix: "/api/datasources/proxy/uid/prom-main", innerPath: "/"},
			wantMatch: true,
		},
		{name: "missing uid", path: "/api/datasources/proxy/uid/", wantMatch: false},
		{name: "invalid id", path: "/api/datasources/proxy/not-an-id/query", wantMatch: false},
		{name: "query API", path: "/api/ds/query", wantMatch: false},
		{name: "Grafana UI", path: "/d/example/dashboard", wantMatch: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseDataSourceProxyPath(test.path)
			if ok != test.wantMatch {
				t.Fatalf("parse match = %v, want %v", ok, test.wantMatch)
			}
			if ok && got != test.want {
				t.Fatalf("parse result = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestProviderForDataSource(t *testing.T) {
	tests := []struct {
		typeName string
		access   string
		want     string
		ok       bool
	}{
		{typeName: "prometheus", access: "proxy", want: providers.Prometheus, ok: true},
		{typeName: "InfluxDB", access: "proxy", want: providers.InfluxDB, ok: true},
		{typeName: "vertamedia-clickhouse-datasource", access: "proxy", want: providers.ClickHouse, ok: true},
		{typeName: "loki", access: "proxy"},
		{typeName: "prometheus", access: "direct"},
	}
	for _, test := range tests {
		got, ok := providerForDataSource(&dataSource{Type: test.typeName, Access: test.access})
		if got != test.want || ok != test.ok {
			t.Errorf("providerForDataSource(%q, %q) = %q, %v; want %q, %v",
				test.typeName, test.access, got, ok, test.want, test.ok)
		}
	}
}

func TestDataSourceBackendIdentityIncludesProviderAndConfiguration(t *testing.T) {
	ds := &dataSource{
		ID: 1, UID: "metrics", OrgID: 2, Type: providers.Prometheus,
		Access: "proxy", URL: "http://prometheus-a:9090",
		JSONData: map[string]any{"httpMethod": "POST"},
	}
	name, prefix := dataSourceBackendIdentity("grafana", providers.Prometheus, ds)
	if name == "" || prefix == "" {
		t.Fatal("data source backend identity must not be empty")
	}

	changedURL := *ds
	changedURL.URL = "http://prometheus-b:9090"
	changedName, changedPrefix := dataSourceBackendIdentity("grafana", providers.Prometheus, &changedURL)
	if changedName == name || changedPrefix == prefix {
		t.Fatal("data source URL change reused the previous backend identity")
	}

	changedName, changedPrefix = dataSourceBackendIdentity("grafana", providers.InfluxDB, ds)
	if changedName == name || changedPrefix == prefix {
		t.Fatal("data source provider change reused the previous backend identity")
	}
}

func TestGrafanaHandlerCachesPrometheusPerIdentity(t *testing.T) {
	var metadataRequests atomic.Int32
	var queryRequests atomic.Int32
	now := time.Now().Add(-time.Minute).Truncate(time.Second)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/datasources/uid/prom-main":
			metadataRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"uid":"prom-main","orgId":1,"type":"prometheus","access":"proxy"}`)
		case "/api/datasources/proxy/uid/prom-main/api/v1/query_range":
			queryRequests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up"},"values":[[%d,"1"]]}]}}`, now.Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	client, cache := newGrafanaTestClient(t, origin.URL)
	defer cache.Close()

	query := url.Values{
		"query": {"up"},
		"start": {fmt.Sprint(now.Unix())},
		"end":   {fmt.Sprint(now.Unix())},
		"step":  {"60"},
	}.Encode()
	path := "/api/datasources/proxy/uid/prom-main/api/v1/query_range?" + query

	first := serveGrafanaRequest(t, client, cache, http.MethodGet, path, "grafana_session=user-a", "")
	if got := first.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "kmiss") {
		t.Fatalf("first result header = %q, want kmiss", got)
	}
	second := serveGrafanaRequest(t, client, cache, http.MethodGet, path, "grafana_session=user-a", "")
	if got := second.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "hit") {
		t.Fatalf("second result header = %q, want hit", got)
	}
	third := serveGrafanaRequest(t, client, cache, http.MethodGet, path, "grafana_session=user-b", "")
	if got := third.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "kmiss") {
		t.Fatalf("different-session result header = %q, want kmiss", got)
	}
	proxyUserA := make(http.Header)
	proxyUserA.Set(grafanaAuthProxyHeader, "user-a")
	fourth := serveGrafanaRequestWithHeaders(t, client, cache, http.MethodGet, path, proxyUserA, "")
	if got := fourth.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "kmiss") {
		t.Fatalf("first auth-proxy result header = %q, want kmiss", got)
	}
	fifth := serveGrafanaRequestWithHeaders(t, client, cache, http.MethodGet, path, proxyUserA, "")
	if got := fifth.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "hit") {
		t.Fatalf("second auth-proxy result header = %q, want hit", got)
	}
	proxyUserB := make(http.Header)
	proxyUserB.Set(grafanaAuthProxyHeader, "user-b")
	sixth := serveGrafanaRequestWithHeaders(t, client, cache, http.MethodGet, path, proxyUserB, "")
	if got := sixth.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "kmiss") {
		t.Fatalf("different auth-proxy user result header = %q, want kmiss", got)
	}

	if got := queryRequests.Load(); got != 4 {
		t.Fatalf("Grafana data proxy requests = %d, want 4", got)
	}
	if got := metadataRequests.Load(); got != 4 {
		t.Fatalf("Grafana metadata requests = %d, want 4", got)
	}
	client.mu.RLock()
	dispatcherCount := len(client.dispatchers)
	client.mu.RUnlock()
	if dispatcherCount != 1 {
		t.Fatalf("data source dispatchers = %d, want one shared dispatcher", dispatcherCount)
	}
}

func TestGrafanaHandlerFallsBackWithoutCaching(t *testing.T) {
	var metadataRequests atomic.Int32
	var proxyRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/datasources/uid/logs":
			metadataRequests.Add(1)
			fmt.Fprint(w, `{"id":2,"uid":"logs","orgId":1,"type":"loki","access":"proxy"}`)
		case "/api/datasources/proxy/uid/logs/loki/api/v1/query_range":
			proxyRequests.Add(1)
			fmt.Fprint(w, `{"data":"uncached"}`)
		case "/api/ds/query":
			proxyRequests.Add(1)
			fmt.Fprint(w, `{"results":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	client, cache := newGrafanaTestClient(t, origin.URL)
	defer cache.Close()
	path := "/api/datasources/proxy/uid/logs/loki/api/v1/query_range"
	for range 2 {
		response := serveGrafanaRequest(t, client, cache, http.MethodGet, path,
			"grafana_session=user-a", "")
		if got := response.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "proxy-only") {
			t.Fatalf("fallback result header = %q, want proxy-only", got)
		}
	}
	response := serveGrafanaRequest(t, client, cache, http.MethodPost, "/api/ds/query",
		"grafana_session=user-a", `{"queries":[]}`)
	if got := response.Header().Get(headers.NameTricksterResult); !strings.Contains(got, "proxy-only") {
		t.Fatalf("query API result header = %q, want proxy-only", got)
	}

	if got := metadataRequests.Load(); got != 1 {
		t.Fatalf("Grafana metadata requests = %d, want 1", got)
	}
	if got := proxyRequests.Load(); got != 3 {
		t.Fatalf("transparent proxy requests = %d, want 3", got)
	}
}

func newGrafanaTestClient(t *testing.T, originURL string) (*Client, cachepkg.Cache) {
	t.Helper()
	cacheConfig := cacheoptions.New()
	if err := cacheConfig.Initialize("default"); err != nil {
		t.Fatal(err)
	}
	cache := manager.NewCache(memory.New("default", cacheConfig), manager.CacheOptions{}, cacheConfig)
	if err := cache.Connect(); err != nil {
		t.Fatal(err)
	}

	o := bo.New()
	o.Provider = providers.Grafana
	o.OriginURL = originURL
	o.FastForwardDisable = true
	o.HealthCheck = nil
	if err := o.Initialize("grafana"); err != nil {
		t.Fatal(err)
	}
	factories := registrytypes.Lookup{providers.Prometheus: prometheus.NewClient}
	backend, err := NewClient("grafana", o, lm.NewRouter(), nil, nil, factories)
	if err != nil {
		t.Fatal(err)
	}
	client := backend.(*Client)
	client.SetCache(cache)
	o.HTTPClient = client.HTTPClient()
	paths := client.DefaultPathConfigs(o)
	if err := paths.Initialize(); err != nil {
		t.Fatal(err)
	}
	o.Paths = paths
	return client, cache
}

func serveGrafanaRequest(t *testing.T, client *Client, cache cachepkg.Cache,
	method, path, cookie, body string,
) *httptest.ResponseRecorder {
	requestHeaders := make(http.Header)
	if cookie != "" {
		requestHeaders.Set("Cookie", cookie)
	}
	return serveGrafanaRequestWithHeaders(t, client, cache, method, path, requestHeaders, body)
}

func serveGrafanaRequestWithHeaders(t *testing.T, client *Client, cache cachepkg.Cache,
	method, path string, requestHeaders http.Header, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, "http://trickster.example"+path, reader)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for name, values := range requestHeaders {
		for _, value := range values {
			r.Header.Add(name, value)
		}
	}
	o := client.Configuration()
	p := o.Paths[0]
	rsc := request.NewResources(o, p, cache.Configuration(), cache, client, nil)
	r = request.SetResources(r, rsc)
	w := httptest.NewRecorder()
	client.GrafanaHandler(w, r)
	return w
}
