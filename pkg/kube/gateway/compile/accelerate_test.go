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

package compile

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"

	"github.com/stretchr/testify/require"
)

// promPaths stands in for the prometheus provider's predefined paths
func promPaths(provider string) po.List {
	if provider != providers.Prometheus {
		return nil
	}
	return po.List{
		{
			Path: "/api/v1/query_range", HandlerName: "query_range",
			MatchTypeName: matching.PathMatchNameExact, Methods: []string{"GET", "POST"},
			CacheKeyParams:  []string{"query", "step", "stats"},
			ResponseHeaders: map[string]string{"Cache-Control": "s-maxage=30"},
		},
		{
			Path: "/api/v1/query", HandlerName: "query", MatchTypeName: matching.PathMatchNameExact,
			Methods: []string{"GET", "POST"}, CacheKeyParams: []string{"query", "time"},
		},
		{
			Path: "/api/v1/label/", HandlerName: "labels", MatchTypeName: matching.PathMatchNamePrefix,
			CacheKeyParams: []string{"match[]"},
		},
		{
			Path: "/metrics", HandlerName: "proxy", MatchTypeName: matching.PathMatchNameExact,
			Methods: []string{"GET"},
		},
		{
			Path: "/", HandlerName: "proxy", MatchTypeName: matching.PathMatchNamePrefix,
			Methods: []string{"GET", "POST"},
		},
	}
}

// realPaths reads the paths the providers really predefine, for assertions against the router
func realPaths(t *testing.T) ProviderPaths {
	t.Helper()
	factories := providerregistry.SupportedProviders()
	return func(provider string) po.List {
		o := bo.New()
		o.Provider = provider
		client, err := factories[provider](provider, o, nil, nil, nil, factories)
		require.NoError(t, err)
		return client.DefaultPathConfigs(o)
	}
}

func emittedWith(t *testing.T, m *ir.IR, opts *kubecfg.Options, paths ProviderPaths,
) map[string]*backendDoc {
	t.Helper()
	doc, err := buildDocument(m, opts, paths)
	require.NoError(t, err)
	return doc.Backends
}

func pathsOf(b *backendDoc, path string) []*pathDoc {
	var out []*pathDoc
	for _, p := range b.Paths {
		if p.Path == path {
			out = append(out, p)
		}
	}
	return out
}

// accelerating returns the backend's paths served by a provider handler
func accelerating(b *backendDoc, path string) []*pathDoc {
	var out []*pathDoc
	for _, p := range pathsOf(b, path) {
		if p.Handler != handlerProxy && p.Handler != handlerProxyCache {
			out = append(out, p)
		}
	}
	return out
}

func TestCompileProviderPathsFollowTheRoute(t *testing.T) {
	// A provider's predefined paths are emitted under the route's own prefix, each with the
	// provider's handler and key components and the policy's settings, and only those the
	// prefix reaches; the backend keeps provider defaults off, so nothing else is registered
	m := simple()
	m.Routes[0].Rules[0].Policy = "p1"
	m.Policies = []ir.Policy{{
		Name: "p1", Provider: providers.Prometheus, CacheName: "objects",
		CacheKeyParams: []string{"stats", "tenant"}, CacheKeyHeaders: []string{"X-Scope-OrgID"},
		ResponseHeaders: map[string]string{"+Vary": "Accept-Encoding"},
		ResultHeader:    ir.ResultHeaderHide,
	}}
	b := emittedWith(t, m, serviceOpts(t), promPaths)["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.Prometheus, b.Provider)
	require.True(t, b.PathDefaultsDisabled, "the provider's paths are emitted, not registered")

	qr := accelerating(b, "/api/v1/query_range")
	require.Len(t, qr, 1)
	require.Equal(t, "query_range", qr[0].Handler)
	require.Equal(t, string(matching.PathMatchNameExact), qr[0].MatchType)
	require.Equal(t, []string{"GET", "POST"}, qr[0].Methods)
	require.Equal(t, []string{"query", "step", "stats", "tenant"}, qr[0].CacheKeyParams,
		"the provider's components come first and the policy's join them once")
	require.Equal(t, []string{"X-Scope-OrgID"}, qr[0].CacheKeyHeaders)
	require.Equal(t, map[string]string{"Cache-Control": "s-maxage=30", "+Vary": "Accept-Encoding"},
		qr[0].ResponseHeaders)
	require.True(t, qr[0].HideResultHeader)
	// the methods the provider path does not serve resolve as the covering prefix would have:
	// here to the route's own plain paths, split as any of its paths are
	var filled []string
	for _, d := range pathsOf(b, "/api/v1/query_range") {
		if d.Handler == handlerProxyCache || d.Handler == handlerProxy {
			filled = append(filled, d.Methods...)
		}
	}
	require.ElementsMatch(t, []string{
		"HEAD", "PUT", "DELETE", "CONNECT", "OPTIONS", "TRACE",
		"PATCH", "PURGE",
	}, filled)
	require.Len(t, accelerating(b, "/api/v1/label/"), 1)
	require.Equal(t, []string{"GET"}, accelerating(b, "/api/v1/label/")[0].Methods,
		"a predefined path naming no method serves GET alone")
	require.Empty(t, pathsOf(b, "/metrics"), "a path outside the route's prefix is not reached")
	require.Empty(t, pathsOf(b, "/"), "the provider's catch-all never widens the route")
	require.Len(t, pathsOf(b, "/api"), 2, "the route's own paths are still served")

	// a rule declaring one of the paths, or a covering root, does not duplicate it
	m.Routes[0].Rules[0].Matches = append(m.Routes[0].Rules[0].Matches,
		ir.Match{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/"}})
	b = emittedWith(t, m, serviceOpts(t), promPaths)["kgw--httproute.shop.web_r0"]
	require.Len(t, accelerating(b, "/api/v1/query_range"), 1, "the longest covering prefix owns it")
	require.NotEmpty(t, pathsOf(b, "/metrics"), "the root reaches what /api did not")

	// without a source of the provider's paths the rule is refused rather than served bare
	_, err := Compile(m, serviceOpts(t))
	require.ErrorIs(t, err, ErrNoProviderPaths)
}

func TestCompileProviderPathsRespectOtherRoutes(t *testing.T) {
	// A slot another route owns below the prefix is not accelerated: an exact path some route
	// declared, and a longer prefix, both belong to their routes at request time
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	other := group("shop", "other", 0, svcMember(0, "shop", "other-svc", 8080, 1))
	third := group("shop", "third", 0, svcMember(0, "shop", "third-svc", 8080, 1))
	prom := route("shop", "web", ir.Rule{
		Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}}},
		BackendGroup: g.Name, Policy: "p1",
	})
	exact := route("shop", "other", ir.Rule{
		Matches: []ir.Match{{
			Path:    ir.PathMatch{Type: ir.PathExact, Value: "/api/v1/query"},
			Methods: []string{"GET"}, MethodSpecific: true,
		}},
		BackendGroup: other.Name,
	})
	exact.Rank = 1
	longer := route("shop", "third", ir.Rule{
		Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api/v1/label"}}},
		BackendGroup: third.Name,
	})
	longer.Rank = 2
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{prom, exact, longer},
		Backends:  []ir.BackendGroup{g, other, third},
		Policies:  []ir.Policy{{Name: "p1", Provider: providers.Prometheus}},
	}
	docs := emittedWith(t, m, serviceOpts(t), promPaths)
	b := docs["kgw--httproute.shop.web_r0"]
	require.Len(t, accelerating(b, "/api/v1/query_range"), 1)
	require.Empty(t, accelerating(b, "/api/v1/query"), "declared exactly by another route")
	// the methods the accelerated path does not serve go where the prefix sent them: with only
	// this route on the host, to its own paths, and nothing is left for a 404 responder
	for name := range docs {
		require.NotContains(t, name, "notfound", name)
	}
	require.NotEmpty(t, pathsOf(b, "/api/v1/query"),
		"the methods the declaring route left are still filled from the prefix, unaccelerated")
	require.Empty(t, pathsOf(b, "/api/v1/label/"), "covered by another route's longer prefix")
}

func TestCompileProviderPathsReachMembersAndTemplates(t *testing.T) {
	// behind a dispatch every predefined path is emitted with the member's settings, since the
	// dispatch already restricted what arrives; the ALB in front carries none of them
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a-svc", 80, 1), svcMember(1, "shop", "b-svc", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name, Policy: "p1"})},
		Backends:  []ir.BackendGroup{g},
		Policies: []ir.Policy{{
			Name: "p1", Provider: providers.Prometheus,
			CacheKeyHeaders: []string{"X-Tenant"}, ResultHeader: ir.ResultHeaderHide,
		}},
	}
	docs := emittedWith(t, m, serviceOpts(t), promPaths)
	require.Empty(t, pathsOf(docs["kgw--httproute.shop.web_r0"], "/api/v1/query_range"))
	member := docs["kgw--httproute.shop.web_r0_b0"]
	require.True(t, member.PathDefaultsDisabled)
	require.Len(t, member.Paths, 2+len(promPaths(providers.Prometheus)))
	qr := pathsOf(member, "/api/v1/query_range")
	require.Len(t, qr, 1)
	require.Equal(t, []string{"X-Tenant"}, qr[0].CacheKeyHeaders)
	require.True(t, qr[0].HideResultHeader)
	require.Len(t, pathsOf(member, "/"), 3, "the provider's root joins the member's catch-all")

	e := endpointShape()
	e.Policies[0].Provider = providers.Prometheus
	e.Policies[0].CacheKeyHeaders = []string{"X-Tenant"}
	docs = emittedWith(t, e, serviceOpts(t), promPaths)
	tmpl := docs["kgw--httproute.shop.web_r0_b0_tmpl"]
	require.True(t, tmpl.PathDefaultsDisabled)
	require.Equal(t, []string{"X-Tenant"}, pathsOf(tmpl, "/api/v1/query")[0].CacheKeyHeaders)
	require.Empty(t, pathsOf(docs["kgw--httproute.shop.web_r0"], "/api/v1/query"))
}

// prometheusStub answers range queries as a Prometheus would, with one sample per step
func prometheusStub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			w.WriteHeader(http.StatusOK)
			return
		}
		q := r.URL.Query()
		start, _ := strconv.ParseInt(q.Get("start"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("end"), 10, 64)
		step, _ := strconv.ParseInt(q.Get("step"), 10, 64)
		if step <= 0 {
			step = 60
		}
		values := make([][2]any, 0)
		for ts := start; ts <= end; ts += step {
			values = append(values, [2]any{ts, "1"})
		}
		body, _ := json.Marshal(map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{{
					"metric": map[string]string{"__name__": "up"}, "values": values,
				}},
			},
		})
		w.Header().Set(headers.NameContentType, "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
}

func rangeQuery() string {
	end := time.Now().Add(-2 * time.Hour).Truncate(time.Minute).Unix()
	return fmt.Sprintf("/api/v1/query_range?query=up&start=%d&end=%d&step=60", end-3600, end)
}

func serve(rtr router.Router, method, host, path string, hdr map[string]string,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w
}

func TestAcceleratedPathsStayBehindTheRoute(t *testing.T) {
	// Against the real router and provider: a provider path is reached only through the route's
	// own match, with its conditions and methods; a competing route on the host, a path outside
	// the prefix, and a method the route did not allow are all served as the planner decided
	origin := prometheusStub()
	defer origin.Close()

	gold := group("shop", "gold", 0, svcMember(0, "shop", "prom-svc", 9090, 1))
	plain := group("shop", "plain", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	only := group("shop", "getonly", 0, svcMember(0, "shop", "prom-svc", 9090, 1))
	hidden := group("shop", "hidden", 0, svcMember(0, "shop", "prom-svc", 9090, 1))
	tenant := route("shop", "gold", ir.Rule{
		Matches: []ir.Match{{
			Path:    ir.PathMatch{Type: ir.PathPrefix, Value: "/api"},
			Headers: []ir.KeyValueMatch{{Name: "X-Tenant", Value: "gold"}},
		}},
		BackendGroup: gold.Name, Policy: "prom",
	})
	tenant.Hostnames = []string{"shop.example.com"}
	fallback := route("shop", "plain", ir.Rule{
		Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}}},
		BackendGroup: plain.Name,
	})
	fallback.Hostnames = []string{"shop.example.com"}
	fallback.Rank = 1
	getOnly := route("shop", "getonly", ir.Rule{
		Matches: []ir.Match{{
			Path:    ir.PathMatch{Type: ir.PathPrefix, Value: "/api"},
			Methods: []string{"GET"}, MethodSpecific: true,
		}},
		BackendGroup: only.Name, Policy: "prom",
	})
	getOnly.Hostnames = []string{"api.example.com"}
	quiet := route("shop", "hidden", ir.Rule{
		Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}}},
		BackendGroup: hidden.Name, Policy: "quiet",
	})
	quiet.Hostnames = []string{"hidden.example.com"}
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{tenant, fallback, getOnly, quiet},
		Backends:  []ir.BackendGroup{gold, plain, only, hidden},
		Policies: []ir.Policy{
			{
				Name: "prom", Provider: providers.Prometheus, CacheName: "default",
				CacheKeyHeaders: []string{"X-Scope-OrgID"},
			},
			{
				Name: "quiet", Provider: providers.Prometheus, CacheName: "default",
				ResultHeader: ir.ResultHeaderHide,
			},
		},
	}
	o, _, err := CompileWith(m, serviceOpts(t), realPaths(t))
	require.NoError(t, err)
	rtr := registerGenerated(t, o.Data, origin.URL)
	result := func(w *httptest.ResponseRecorder) string {
		return w.Header().Get(headers.NameTricksterResult)
	}

	// the header-constrained route accelerates what matches it, and what does not match falls
	// to the plain route on the same host, which proxies
	w := serve(rtr, http.MethodGet, "shop.example.com", rangeQuery(),
		map[string]string{"X-Tenant": "gold", "X-Scope-OrgID": "a"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, result(w), "engine=DeltaProxyCache")
	require.Contains(t, result(w), "status=kmiss")
	w = serve(rtr, http.MethodGet, "shop.example.com", rangeQuery(), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, result(w), "DeltaProxyCache",
		"without the header the competing plain route serves the request")

	// the cache is partitioned by the policy's header on the accelerated path
	w = serve(rtr, http.MethodGet, "shop.example.com", rangeQuery(),
		map[string]string{"X-Tenant": "gold", "X-Scope-OrgID": "a"})
	require.Contains(t, result(w), "status=hit", result(w))
	w = serve(rtr, http.MethodGet, "shop.example.com", rangeQuery(),
		map[string]string{"X-Tenant": "gold", "X-Scope-OrgID": "b"})
	require.Contains(t, result(w), "status=kmiss", "another tenant must not share the object")

	// a path outside the route's prefix is not reachable however the provider predefines it
	for _, path := range []string{"/", "/metrics", "/-/healthy"} {
		w = serve(rtr, http.MethodGet, "shop.example.com", path,
			map[string]string{"X-Tenant": "gold"})
		require.Equal(t, http.StatusNotFound, w.Code, path)
	}

	// a method the route did not allow is refused even where the provider would serve it
	w = serve(rtr, http.MethodGet, "api.example.com", rangeQuery(), nil)
	require.Contains(t, result(w), "engine=DeltaProxyCache")
	w = serve(rtr, http.MethodPost, "api.example.com", rangeQuery(), nil)
	require.Equal(t, http.StatusNotFound, w.Code)

	// a hiding policy withholds the header from the accelerated response too
	w = serve(rtr, http.MethodGet, "hidden.example.com", rangeQuery(), nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Empty(t, result(w))
	require.True(t, strings.Contains(w.Body.String(), "matrix"))
}
