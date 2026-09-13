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
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	cacheregistry "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"

	"github.com/stretchr/testify/require"
)

// hostRoute builds a ranked route on one host, attached to the standard
// test listener; rank is the route's age order, oldest first
func hostRoute(name, host string, rank int, rules ...ir.Rule) ir.Route {
	r := route("shop", name, rules...)
	r.Rank = rank
	if host != "" {
		r.Hostnames = []string{host}
	}
	return r
}

// serviceRule is one rule dispatching every match to one Service; the group
// is named after the route so several rules can share a fixture
func serviceRule(name string, ruleIndex int, service string, matches ...ir.Match,
) (ir.Rule, ir.BackendGroup) {
	g := group("shop", name, ruleIndex, svcMember(0, "shop", service, 80, 1))
	g.Name = "shop." + name + "-g" + strconv.Itoa(ruleIndex)
	return ir.Rule{Matches: matches, BackendGroup: g.Name}, g
}

func exact(path string) ir.Match {
	return ir.Match{Path: ir.PathMatch{Type: ir.PathExact, Value: path}}
}

func prefix(path string) ir.Match {
	return ir.Match{Path: ir.PathMatch{Type: ir.PathPrefix, Value: path}}
}

func withHeaders(m ir.Match, kv ...string) ir.Match {
	for i := 0; i+1 < len(kv); i += 2 {
		m.Headers = append(m.Headers, ir.KeyValueMatch{Name: kv[i], Value: kv[i+1]})
	}
	return m
}

func withMethods(m ir.Match, methods ...string) ir.Match {
	m.Methods = methods
	return m
}

// withMethod names one method the way a source declares it, which is what
// ranks the match ahead of one that names none
func withMethod(m ir.Match, method string) ir.Match {
	m.Methods = []string{method}
	m.MethodSpecific = true
	return m
}

// model assembles routes and their groups into an IR on the test listener
func model(routes []ir.Route, groups ...ir.BackendGroup) *ir.IR {
	return &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    routes,
		Backends:  groups,
	}
}

// serveIR compiles an IR and serves it through the real loader and router,
// with one origin per Service whose response names it
func serveIR(t *testing.T, m *ir.IR, opts *kubecfg.Options) router.Router {
	t.Helper()
	o, err := Compile(m, opts)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(loadableBaseConfig), 0o600))
	conf, err := config.LoadWithOverlay([]string{"-config", path}, o)
	require.NoError(t, err)
	require.NoError(t, conf.Backends.Validate())
	require.NoError(t, validate.Validate(conf))

	origins := make(map[string]string)
	for _, b := range conf.Backends {
		if b.OriginURL == "" || !strings.Contains(b.OriginURL, ".svc:") {
			continue
		}
		b.OriginURL = originFor(t, origins, b.OriginURL)
		parsed, err := neturl.Parse(b.OriginURL)
		require.NoError(t, err)
		b.Scheme, b.Host, b.PathPrefix = parsed.Scheme, parsed.Host, parsed.Path
	}
	require.NoError(t, conf.Process())
	clients := make(backends.Backends, len(conf.Backends))
	require.NoError(t, validate.RoutesRulesAndPools(conf, clients))
	caches := cacheregistry.LoadCachesFromConfig(conf)
	t.Cleanup(func() { cacheregistry.CloseCaches(caches) })
	rtr := lm.NewRouter()
	require.NoError(t, routing.RegisterProxyRoutes(conf, clients, rtr, nil,
		caches, nil, false))
	// the daemon starts the ALB pools after registration; a member with no
	// health check of its own is admitted as passing
	require.NoError(t, alb.StartALBPools(clients, healthcheck.StatusLookup{}))
	t.Cleanup(func() { _ = alb.StopPools(clients) })
	return rtr
}

// originFor returns a server standing in for one in-cluster Service whose
// response names the Service and echoes the path it was asked for
func originFor(t *testing.T, origins map[string]string, clusterURL string) string {
	t.Helper()
	if url, ok := origins[clusterURL]; ok {
		return url
	}
	u, err := neturl.Parse(clusterURL)
	require.NoError(t, err)
	service, _, _ := strings.Cut(u.Host, ".")
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Upstream-Path", r.URL.Path)
			// a HEAD response carries no body, so the Service names itself
			// in a header as well
			w.Header().Set("X-Upstream-Service", service)
			_, _ = w.Write([]byte(service))
		}))
	t.Cleanup(srv.Close)
	origins[clusterURL] = srv.URL
	return srv.URL
}

// requestService issues one request and returns the status and the Service
// that answered, which a HEAD response can only name in a header
func requestService(rtr router.Router, method, host, path string, headers ...string,
) (int, string) {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code, w.Header().Get("X-Upstream-Service")
}

// request issues one request and returns the status and the body, which
// names the Service that answered
func request(rtr router.Router, method, host, path string, headers ...string) (int, string) {
	req := httptest.NewRequest(method, path, nil)
	req.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

const testHost = "shop.example.com"

// A header match is a conditioned path on its own backend, ranked ahead of the
// plain match on the same path
func TestHeaderMatchIsAConditionedPath(t *testing.T) {
	stableRule, stable := serviceRule("web", 0, "stable", exact("/api"))
	canaryRule, canary := serviceRule("canary", 0, "canary",
		withHeaders(exact("/api"), "X-Canary", "true"))
	m := model([]ir.Route{
		hostRoute("web", testHost, 0, stableRule),
		hostRoute("canary", testHost, 1, canaryRule),
	}, stable, canary)
	rtr := serveIR(t, m, serviceOpts(t))

	code, body := request(rtr, http.MethodGet, testHost, "/api", "X-Canary", "true")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "canary", body, "the header match must win when the header is present")
	code, body = request(rtr, http.MethodGet, testHost, "/api", "X-Canary", "false")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "stable", body, "a wrong value must fall through to the plain match")
	code, body = request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "stable", body, "a missing header must fall through to the plain match")

	// the shape: both terminals register the path on the host, the header
	// match conditioned and ranked ahead of the plain one
	got := emitted(t, m, serviceOpts(t))
	require.NotContains(t, got, "kgw--httproute.shop.canary_r0_m0_p0", "no rule chain is generated")
	cond := got["kgw--httproute.shop.canary_r0"]
	require.NotNil(t, cond, "backends: %v", keysOf(got))
	require.Equal(t, []string{testHost}, cond.Hosts)
	require.Len(t, cond.Paths, 1)
	require.Equal(t, "/api", cond.Paths[0].Path)
	require.Equal(t, []*conditionDoc{{Name: "X-Canary", Value: "true"}}, cond.Paths[0].MatchHeaders)
	require.Positive(t, cond.Paths[0].MatchOrder)

	plain := got["kgw--httproute.shop.web_r0"]
	require.Equal(t, []string{testHost}, plain.Hosts)
	require.Len(t, plain.Paths, 1)
	require.Empty(t, plain.Paths[0].MatchHeaders)
	require.Greater(t, plain.Paths[0].MatchOrder, cond.Paths[0].MatchOrder,
		"the unconditioned match ranks last")
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A header miss goes where the router would have sent the request without the
// match: the longest covering prefix, possibly on a less specific host tier
func TestConditionMissFallsToTheCoveringMatch(t *testing.T) {
	apiRule, api := serviceRule("api", 0, "api",
		withHeaders(exact("/api/orders"), "X-Version", "2"))
	siteRule, site := serviceRule("site", 0, "site", prefix("/"))
	hostless, hostlessGroup := serviceRule("hostless", 0, "fallback", prefix("/api/"))
	m := model([]ir.Route{
		hostRoute("api", testHost, 0, apiRule),
		hostRoute("site", testHost, 1, siteRule),
		hostRoute("hostless", "", 2, hostless),
	}, api, site, hostlessGroup)
	rtr := serveIR(t, m, serviceOpts(t))

	code, body := request(rtr, http.MethodGet, testHost, "/api/orders", "X-Version", "2")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "api", body)
	code, body = request(rtr, http.MethodGet, testHost, "/api/orders")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "site", body,
		"the same host's covering prefix must answer before a less specific tier")

	// with no cover on the host, the global tier is next
	m.Routes = m.Routes[:1:1]
	m.Routes = append(m.Routes, hostRoute("hostless", "", 2, hostless))
	rtr = serveIR(t, m, serviceOpts(t))
	code, body = request(rtr, http.MethodGet, testHost, "/api/orders")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "fallback", body,
		"a hostless route covering the path must catch the miss")
}

// A header miss with nothing covering the path is a request no route
// matched, which is a 404 rather than a 405 or a proxy to the wrong place
func TestConditionMissWithoutCoverAnswers404(t *testing.T) {
	apiRule, api := serviceRule("api", 0, "api",
		withHeaders(exact("/api"), "X-Version", "2"))
	m := model([]ir.Route{hostRoute("api", testHost, 0, apiRule)}, api)
	rtr := serveIR(t, m, serviceOpts(t))

	code, body := request(rtr, http.MethodGet, testHost, "/api", "X-Version", "2")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "api", body)
	code, _ = request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, http.StatusNotFound, code)

	got := emitted(t, m, serviceOpts(t))
	require.NotContains(t, got, NotFoundName("kgw--listener-http-80", testHost),
		"the router's own 404 answers a miss; a responder is generated only for method fills")
}

// A method no match claims on a claimed path is filled by the covering match
// where one exists, and is a 404 otherwise, never a 405
func TestUnclaimedMethodsAreFilled(t *testing.T) {
	getRule, getGroup := serviceRule("reads", 0, "reads",
		withMethods(exact("/api"), http.MethodGet))
	siteRule, site := serviceRule("site", 0, "site", prefix("/"))
	m := model([]ir.Route{
		hostRoute("reads", testHost, 0, getRule),
		hostRoute("site", testHost, 1, siteRule),
	}, getGroup, site)
	rtr := serveIR(t, m, serviceOpts(t))

	code, body := request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "reads", body)
	code, body = request(rtr, http.MethodPost, testHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "site", body, "the covering prefix must answer the unclaimed method")

	got := emitted(t, m, serviceOpts(t))
	cover := got["kgw--httproute.shop.site_r0"]
	require.Len(t, cover.Paths, 2, "the cover carries its own path and the fill")
	require.Equal(t, "/api", cover.Paths[1].Path)
	require.Equal(t, "exact", cover.Paths[1].MatchType)
	require.NotContains(t, cover.Paths[1].Methods, http.MethodGet)
	require.Contains(t, cover.Paths[1].Methods, http.MethodPost)

	// with nothing covering, the unclaimed methods are a 404
	m.Routes = m.Routes[:1]
	rtr = serveIR(t, m, serviceOpts(t))
	code, _ = request(rtr, http.MethodPost, testHost, "/api")
	require.Equal(t, http.StatusNotFound, code)
	code, _ = request(rtr, http.MethodHead, testHost, "/api")
	require.Equal(t, http.StatusNotFound, code,
		"HEAD is not implied by GET for a match that named only GET")
	got = emitted(t, m, serviceOpts(t))
	nf := got[NotFoundName("kgw--listener-http-80", testHost)]
	require.NotNil(t, nf)
	require.Equal(t, []string{testHost}, nf.Hosts)
	require.Len(t, nf.Paths, 1)
	require.Equal(t, "/api", nf.Paths[0].Path)
	require.Equal(t, http.StatusNotFound, nf.Paths[0].ResponseCode)
}

// A fill carries the covering match's rewriter, because the filled requests
// are ones the covering match would have rewritten
func TestFillsCarryTheCoveringRewriter(t *testing.T) {
	getRule, getGroup := serviceRule("reads", 0, "reads",
		withMethods(exact("/legacy/x"), http.MethodGet))
	siteRule, site := serviceRule("site", 0, "site", prefix("/legacy/"))
	siteRule.Policy = "rewrite"
	m := model([]ir.Route{
		hostRoute("reads", testHost, 0, getRule),
		hostRoute("site", testHost, 1, siteRule),
	}, getGroup, site)
	m.Policies = []ir.Policy{{Name: "rewrite", RewriteTarget: "/v2/"}}
	rtr := serveIR(t, m, serviceOpts(t))
	req := httptest.NewRequest(http.MethodPost, "/legacy/x", nil)
	req.Host = testHost
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "site", w.Body.String())
	require.Equal(t, "/v2/x", w.Header().Get("X-Upstream-Path"))
}

// Several conditioned matches on one path are tried in the Gateway API's order:
// more headers, then the older route, then declaration; the plain match last
func TestMatchOrderFollowsPrecedence(t *testing.T) {
	twoRule, two := serviceRule("two", 0, "two",
		withHeaders(exact("/api"), "X-A", "1", "X-B", "2"))
	oneRule, one := serviceRule("one", 0, "one", withHeaders(exact("/api"), "X-A", "1"))
	olderRule, older := serviceRule("older", 0, "older",
		withHeaders(exact("/api"), "X-B", "2"))
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/api"))
	m := model([]ir.Route{
		// the two-header match is the newest route and still goes first
		hostRoute("two", testHost, 3, twoRule),
		hostRoute("one", testHost, 2, oneRule),
		hostRoute("older", testHost, 1, olderRule),
		hostRoute("plain", testHost, 0, plainRule),
	}, two, one, older, plain)
	rtr := serveIR(t, m, serviceOpts(t))

	tests := []struct {
		name    string
		headers []string
		want    string
	}{
		{"both headers", []string{"X-A", "1", "X-B", "2"}, "two"},
		{"only A", []string{"X-A", "1"}, "one"},
		{"only B", []string{"X-B", "2"}, "older"},
		{"neither", nil, "plain"},
		{"wrong values", []string{"X-A", "9", "X-B", "9"}, "plain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, body := request(rtr, http.MethodGet, testHost, "/api", test.headers...)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, test.want, body)
		})
	}

	// the match orders sort the candidates the same way
	got := emitted(t, m, serviceOpts(t))
	order := func(name string) int { return got["kgw--httproute.shop."+name+"_r0"].Paths[0].MatchOrder }
	require.Less(t, order("two"), order("older"))
	require.Less(t, order("older"), order("one"))
	require.Less(t, order("one"), order("plain"))
	require.Positive(t, order("two"))
}

// Query parameters are tested like headers, and a regular expression value
// must match the whole value rather than be found within it
func TestQueryAndRegexPredicates(t *testing.T) {
	betaRule, beta := serviceRule("beta", 0, "beta", ir.Match{
		Path:        ir.PathMatch{Type: ir.PathExact, Value: "/api"},
		QueryParams: []ir.KeyValueMatch{{Name: "channel", Value: "beta"}},
	})
	mobileRule, mobile := serviceRule("mobile", 0, "mobile", ir.Match{
		Path:    ir.PathMatch{Type: ir.PathExact, Value: "/api"},
		Headers: []ir.KeyValueMatch{{Name: "User-Agent", Value: "Mobile-[0-9]+", Regex: true}},
	})
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/api"))
	m := model([]ir.Route{
		hostRoute("beta", testHost, 0, betaRule),
		hostRoute("mobile", testHost, 1, mobileRule),
		hostRoute("plain", testHost, 2, plainRule),
	}, beta, mobile, plain)
	rtr := serveIR(t, m, serviceOpts(t))

	_, body := request(rtr, http.MethodGet, testHost, "/api?channel=beta")
	require.Equal(t, "beta", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api?channel=stable")
	require.Equal(t, "plain", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api", "User-Agent", "Mobile-12")
	require.Equal(t, "mobile", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api", "User-Agent", "Not-Mobile-12")
	require.Equal(t, "plain", body, "the pattern must match the whole value")

	got := emitted(t, m, serviceOpts(t))
	require.Equal(t, []*conditionDoc{{Name: "User-Agent", Value: "^(?:Mobile-[0-9]+)$", Regex: true}},
		got["kgw--httproute.shop.mobile_r0"].Paths[0].MatchHeaders)
	require.Equal(t, []*conditionDoc{{Name: "channel", Value: "beta"}},
		got["kgw--httproute.shop.beta_r0"].Paths[0].MatchQueryParams)
}

// A terminal ranks only the path where another candidate meets it; its other
// paths stay unranked
func TestOnlySharedPathsCarryAMatchOrder(t *testing.T) {
	webRule, web := serviceRule("web", 0, "web", exact("/api"), exact("/other"))
	canaryRule, canary := serviceRule("canary", 0, "canary",
		withHeaders(exact("/api"), "X-Canary", "true"))
	m := model([]ir.Route{
		hostRoute("web", testHost, 0, webRule),
		hostRoute("canary", testHost, 1, canaryRule),
	}, web, canary)
	got := emitted(t, m, serviceOpts(t))
	b := got["kgw--httproute.shop.web_r0"]
	require.Equal(t, []string{testHost}, b.Hosts)
	require.Len(t, b.Paths, 2)
	byPath := map[string]int{}
	for _, p := range b.Paths {
		byPath[p.Path] = p.MatchOrder
	}
	require.Positive(t, byPath["/api"], "the shared path is ranked")
	require.Zero(t, byPath["/other"], "the path nothing else claims is not")

	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/other")
	require.Equal(t, "web", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, "web", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-Canary", "true")
	require.Equal(t, "canary", body)
}

// A method-specific conditioned match shares only that method's slot, so the
// plain terminal carries the path twice: ranked for that method, plain for the rest
func TestMethodsResolvedApartSplitThePath(t *testing.T) {
	webRule, web := serviceRule("web", 0, "web", exact("/api"))
	canaryRule, canary := serviceRule("canary", 0, "canary",
		withMethod(withHeaders(exact("/api"), "X-Canary", "true"), http.MethodPost))
	m := model([]ir.Route{
		hostRoute("web", testHost, 0, webRule),
		hostRoute("canary", testHost, 1, canaryRule),
	}, web, canary)
	got := emitted(t, m, serviceOpts(t))
	b := got["kgw--httproute.shop.web_r0"]
	require.Len(t, b.Paths, 2)
	require.Zero(t, b.Paths[0].MatchOrder)
	require.NotContains(t, b.Paths[0].Methods, http.MethodPost)
	require.Positive(t, b.Paths[1].MatchOrder)
	require.Equal(t, []string{http.MethodPost}, b.Paths[1].Methods)
	condPath := got["kgw--httproute.shop.canary_r0"].Paths[0]
	require.Equal(t, []string{http.MethodPost}, condPath.Methods)
	require.Less(t, condPath.MatchOrder, b.Paths[1].MatchOrder)

	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/api", "X-Canary", "true")
	require.Equal(t, "web", body, "the header match named only POST")
	_, body = request(rtr, http.MethodPost, testHost, "/api", "X-Canary", "true")
	require.Equal(t, "canary", body)
	_, body = request(rtr, http.MethodPost, testHost, "/api")
	require.Equal(t, "web", body)
}

// A weighted rule with a header match conditions its ALB's path the same way
func TestConditionedMatchOnAWeightedALB(t *testing.T) {
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/api"))
	g := group("shop", "split", 0,
		svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	split := ir.Rule{
		Matches:      []ir.Match{withHeaders(exact("/api"), "X-Split", "1")},
		BackendGroup: g.Name,
	}
	m := model([]ir.Route{
		hostRoute("plain", testHost, 0, plainRule),
		hostRoute("split", testHost, 1, split),
	}, plain, g)
	rtr := serveIR(t, m, serviceOpts(t))
	seen := map[string]bool{}
	for range 6 {
		code, body := request(rtr, http.MethodGet, testHost, "/api", "X-Split", "1")
		require.Equal(t, http.StatusOK, code)
		seen[body] = true
	}
	require.Equal(t, map[string]bool{"a": true, "b": true}, seen)
	_, body := request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, "plain", body)
}

// Many header conditions on one match are all tested on one path
func TestManyHeaderConditionsOnOnePath(t *testing.T) {
	kv := make([]string, 0, 32)
	for i := range 16 {
		kv = append(kv, "X-H"+strconv.Itoa(i), "1")
	}
	longRule, long := serviceRule("long", 0, "long", withHeaders(exact("/api"), kv...))
	m := model([]ir.Route{hostRoute("long", testHost, 0, longRule)}, long)
	got := emitted(t, m, serviceOpts(t))
	require.Len(t, got["kgw--httproute.shop.long_r0"].Paths[0].MatchHeaders, 16)
	rtr := serveIR(t, m, serviceOpts(t))
	code, body := request(rtr, http.MethodGet, testHost, "/api", kv...)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "long", body)
	code, _ = request(rtr, http.MethodGet, testHost, "/api", kv[:30]...)
	require.Equal(t, http.StatusNotFound, code, "every condition must hold")
}

// A route on two listeners with a conditioned peer on one of them compiles
// to one backend: the peer's rank orders it wherever the two meet
func TestRouteOnTwoListenersWithAConditionedPeer(t *testing.T) {
	webRule, web := serviceRule("web", 0, "web", exact("/api"))
	canaryRule, canary := serviceRule("canary", 0, "canary",
		withHeaders(exact("/api"), "X-Canary", "true"))
	both := hostRoute("web", testHost, 0, webRule)
	both.Listeners = []string{"l80", "l8080"}
	m := model([]ir.Route{both, hostRoute("canary", testHost, 1, canaryRule)}, web, canary)
	m.Listeners = append(m.Listeners, ir.Listener{
		Name: "l8080", Port: 8080, Protocol: ir.ProtocolHTTP,
		Source: src("Gateway", "infra", "gw"),
	})
	got := emitted(t, m, serviceOpts(t))
	b := got["kgw--httproute.shop.web_r0"]
	require.Len(t, b.ListenerNames, 2)
	require.Len(t, b.Paths, 1)
	require.Greater(t, b.Paths[0].MatchOrder, got["kgw--httproute.shop.canary_r0"].Paths[0].MatchOrder)
}

// Two plain matches on one slot and method are a translator bug; the
// compiler keeps the one precedence puts first rather than emitting both
func TestDuplicatePlainClaimsKeepTheFirst(t *testing.T) {
	aRule, a := serviceRule("a", 0, "a", exact("/api"))
	bRule, b := serviceRule("b", 0, "b", exact("/api"))
	m := model([]ir.Route{
		hostRoute("b", testHost, 1, bRule),
		hostRoute("a", testHost, 0, aRule),
	}, a, b)
	got := emitted(t, m, serviceOpts(t))
	require.Equal(t, []string{testHost}, got["kgw--httproute.shop.a_r0"].Hosts)
	require.NotEmpty(t, got["kgw--httproute.shop.a_r0"].Paths)
	require.Empty(t, got["kgw--httproute.shop.b_r0"].Paths,
		"the losing match registers nothing")
}

// The conditioned and responder shapes must survive the same soundness and
// loading invariants as every other generated shape
func TestConditionedShapesAreSoundAndLoad(t *testing.T) {
	shapes := conditionedShapes()
	factories := providerregistry.SupportedProviders()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(loadableBaseConfig), 0o600))
	for name, m := range shapes {
		t.Run(name, func(t *testing.T) {
			for _, cfgName := range []string{"no cache", "cache"} {
				opts := serviceOpts(t)
				if cfgName == "cache" {
					opts.Defaults.CacheName = "default"
				}
				o, err := Compile(m, opts)
				require.NoError(t, err)
				assertGeneratedRoutesAreSound(t, factories, decode(t, o.Data).Backends)
				conf, err := config.LoadWithOverlay([]string{"-config", path}, o)
				require.NoError(t, err)
				require.NoError(t, conf.Backends.Validate())
				require.NoError(t, validate.Validate(conf))
			}
		})
	}
}

// conditionedShapes is one IR per shape the planner adds to the projection
func conditionedShapes() map[string]*ir.IR {
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/api"))
	headerRule, header := serviceRule("header", 0, "header",
		withHeaders(exact("/api"), "X-A", "1"))
	getRule, get := serviceRule("get", 0, "get", withMethods(exact("/only"), http.MethodGet))
	return map[string]*ir.IR{
		"condition ahead of plain": model([]ir.Route{
			hostRoute("plain", testHost, 0, plainRule),
			hostRoute("header", testHost, 1, headerRule),
		}, plain, header),
		"condition alone": model([]ir.Route{
			hostRoute("header", testHost, 0, headerRule),
		}, header),
		"method fill onto 404": model([]ir.Route{
			hostRoute("get", testHost, 0, getRule),
		}, get),
		"hostless condition": model([]ir.Route{
			hostRoute("plain", "", 0, plainRule),
			hostRoute("header", "", 1, headerRule),
		}, plain, header),
	}
}

// Compiling the same conditioned IR twice yields identical bytes
func TestConditionedCompilationIsDeterministic(t *testing.T) {
	for name, m := range conditionedShapes() {
		t.Run(name, func(t *testing.T) {
			a, err := Compile(m, serviceOpts(t))
			require.NoError(t, err)
			b, err := Compile(m, serviceOpts(t))
			require.NoError(t, err)
			require.Equal(t, string(a.Data), string(b.Data))
		})
	}
}

// The generated conditions decode into the projection and load as path
// predicates through the real configuration loader
func TestConditionedDocumentRoundTrips(t *testing.T) {
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/api"))
	headerRule, header := serviceRule("header", 0, "header",
		withHeaders(exact("/api"), "X-A", "1"))
	m := model([]ir.Route{
		hostRoute("plain", testHost, 0, plainRule),
		hostRoute("header", testHost, 1, headerRule),
	}, plain, header)
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)
	h := got.Backends["kgw--httproute.shop.header_r0"].Paths[0]
	require.Len(t, h.MatchHeaders, 1)
	require.Equal(t, "X-A", h.MatchHeaders[0].Name)
	require.Equal(t, "1", h.MatchHeaders[0].Value)
	require.Positive(t, h.MatchOrder)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(loadableBaseConfig), 0o600))
	conf, err := config.LoadWithOverlay([]string{"-config", path}, o)
	require.NoError(t, err)
	loaded := conf.Backends["kgw--httproute.shop.header_r0"].Paths[0]
	require.NotNil(t, loaded.Predicates)
	require.Len(t, loaded.Predicates.Headers, 1)
	require.Equal(t, h.MatchOrder, loaded.MatchOrder)
}

// The precedence order is total: method specificity, header count, query
// count, route rank, route name, rule index, match index; exact and prefix
// paths never meet, since the router keeps them in separate tiers
func TestClaimantPrecedenceIsTotal(t *testing.T) {
	mk := func(rank int, name string, rule, match int, m ir.Match) *claimant {
		return &claimant{
			route: &ir.Route{Name: name, Rank: rank}, listeners: []string{"l80"},
			ruleIdx: rule, matchIdx: match, match: m,
			group:   ir.BackendGroup{Source: src("HTTPRoute", "shop", name), RuleIndex: rule},
			methods: methods.Expand(m.Methods), method: m.MethodSpecific,
		}
	}
	base := exact("/api")
	oneHeader := withHeaders(exact("/api"), "X-A", "1")
	oneQuery := ir.Match{Path: base.Path, QueryParams: []ir.KeyValueMatch{{Name: "q", Value: "1"}}}
	tests := []struct {
		name  string
		first *claimant
		then  *claimant
	}{
		{"method beats headers", mk(9, "z", 9, 9, withMethod(base, "POST")), mk(0, "a", 0, 0, oneHeader)},
		{"an awarded subset confers no rank", mk(0, "a", 0, 0, oneHeader), mk(9, "z", 9, 9, withMethods(base, "POST"))},
		{"headers beat queries", mk(9, "z", 9, 9, oneHeader), mk(0, "a", 0, 0, oneQuery)},
		{"more queries first", mk(9, "z", 9, 9, ir.Match{Path: base.Path, QueryParams: []ir.KeyValueMatch{
			{Name: "q", Value: "1"}, {Name: "r", Value: "2"},
		}}), mk(0, "a", 0, 0, oneQuery)},
		{"older route first", mk(0, "z", 9, 9, oneHeader), mk(1, "a", 0, 0, oneHeader)},
		{"then route name", mk(0, "a", 9, 9, oneHeader), mk(0, "b", 0, 0, oneHeader)},
		{"then rule index", mk(0, "a", 0, 9, oneHeader), mk(0, "a", 1, 0, oneHeader)},
		{"then match index", mk(0, "a", 0, 0, oneHeader), mk(0, "a", 0, 1, oneHeader)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Negative(t, test.first.compare(test.then))
			require.Positive(t, test.then.compare(test.first))
		})
	}
}

// A method-specific conditioned match meets the general one only on its
// method; the general match still catches that method when the specific misses
func TestMethodSpecificConditionSharesTheSlot(t *testing.T) {
	allRule, all := serviceRule("all", 0, "all", withHeaders(exact("/api"), "X-A", "1"))
	postRule, post := serviceRule("post", 0, "post",
		withMethod(withHeaders(exact("/api"), "X-B", "2"), http.MethodPost))
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/api"))
	m := model([]ir.Route{
		hostRoute("all", testHost, 0, allRule),
		hostRoute("post", testHost, 1, postRule),
		hostRoute("plain", testHost, 2, plainRule),
	}, all, post, plain)
	got := emitted(t, m, serviceOpts(t))
	postPath := got["kgw--httproute.shop.post_r0"].Paths[0]
	allPath := got["kgw--httproute.shop.all_r0"].Paths[0]
	require.Equal(t, []string{http.MethodPost}, postPath.Methods)
	require.Less(t, postPath.MatchOrder, allPath.MatchOrder, "the method-specific match ranks first")

	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodPost, testHost, "/api", "X-B", "2")
	require.Equal(t, "post", body)
	_, body = request(rtr, http.MethodPost, testHost, "/api", "X-A", "1")
	require.Equal(t, "all", body, "a POST missing X-B still reaches the general chain")
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-B", "2")
	require.Equal(t, "plain", body, "X-B is only consulted for POST")
}

// A fill whose cover is conditioned carries the cover's conditions, so the
// filled requests are tested as the cover would test them
func TestFillOntoAConditionedCover(t *testing.T) {
	getRule, get := serviceRule("get", 0, "get", withMethods(exact("/api/x"), http.MethodGet))
	coverRule, cover := serviceRule("cover", 0, "cover", withHeaders(prefix("/api/"), "X-A", "1"))
	m := model([]ir.Route{
		hostRoute("get", testHost, 0, getRule),
		hostRoute("cover", testHost, 1, coverRule),
	}, get, cover)
	got := emitted(t, m, serviceOpts(t))
	coverDoc := got["kgw--httproute.shop.cover_r0"]
	require.Len(t, coverDoc.Paths, 2, "the cover carries its own path and the fill")
	require.Equal(t, "/api/x", coverDoc.Paths[1].Path)
	require.NotContains(t, coverDoc.Paths[1].Methods, http.MethodGet)
	require.Equal(t, coverDoc.Paths[0].MatchHeaders, coverDoc.Paths[1].MatchHeaders)

	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodPost, testHost, "/api/x", "X-A", "1")
	require.Equal(t, "cover", body)
	code, _ := request(rtr, http.MethodPost, testHost, "/api/x")
	require.Equal(t, http.StatusNotFound, code, "the cover's header still gates the fill")
}

// A conditioned match on a hostname falls back to the same exact path on a
// less specific host tier before any prefix there
func TestConditionMissFallsToTheSamePathOnALowerTier(t *testing.T) {
	hostedRule, hosted := serviceRule("hosted", 0, "hosted", withHeaders(exact("/api"), "X-A", "1"))
	exactRule, exactGroup := serviceRule("exact", 0, "exact-svc", exact("/api"))
	rootRule, root := serviceRule("root", 0, "root", prefix("/"))
	m := model([]ir.Route{
		hostRoute("hosted", testHost, 0, hostedRule),
		hostRoute("exact", "", 1, exactRule),
		hostRoute("root", "", 2, rootRule),
	}, hosted, exactGroup, root)
	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, "exact-svc", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-A", "1")
	require.Equal(t, "hosted", body)
}

// A regular expression slot is neither covered nor a cover for method fills;
// a condition miss on it falls to the next pattern the router tries
func TestRegexSlotsDoNotCover(t *testing.T) {
	rxRule, rx := serviceRule("rx", 0, "rx", ir.Match{
		Path:    ir.PathMatch{Type: ir.PathRegex, Value: "^/api/[0-9]+"},
		Headers: []ir.KeyValueMatch{{Name: "X-A", Value: "1"}},
	})
	wideRule, wide := serviceRule("wide", 0, "wide", ir.Match{
		Path: ir.PathMatch{Type: ir.PathRegex, Value: "^/.*"},
	})
	m := model([]ir.Route{
		hostRoute("rx", testHost, 0, rxRule),
		hostRoute("wide", testHost, 1, wideRule),
	}, rx, wide)
	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/api/42", "X-A", "1")
	require.Equal(t, "rx", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api/42")
	require.Equal(t, "wide", body, "a miss continues to the next pattern")
	_, body = request(rtr, http.MethodGet, testHost, "/other")
	require.Equal(t, "wide", body)
	// a method the pattern does not claim is a 404, not a fill from a prefix
	postRule, post := serviceRule("post", 0, "post", withMethods(ir.Match{
		Path: ir.PathMatch{Type: ir.PathRegex, Value: "^/rx$"},
	}, http.MethodPost))
	rootRule, root := serviceRule("root", 0, "root", prefix("/"))
	m = model([]ir.Route{
		hostRoute("post", testHost, 0, postRule),
		hostRoute("root", testHost, 1, rootRule),
	}, post, root)
	got := emitted(t, m, serviceOpts(t))
	require.Len(t, got["kgw--httproute.shop.root_r0"].Paths, 1, "a regex slot takes no fill")
	require.Contains(t, got, NotFoundName("kgw--listener-http-80", testHost))
}

// The operator-tier names a policy carries reach the generated backend
func TestCompilePolicyOperatorNames(t *testing.T) {
	m := simple()
	m.Policies = []ir.Policy{{
		Name: "class", TracingName: "otlp",
		ReqRewriterName: "strip", AuthenticatorName: "auth",
	}}
	m.Routes[0].Rules[0].Policy = "class"
	b := emitted(t, m, serviceOpts(t))["kgw--httproute.shop.web_r0"]
	require.Equal(t, "otlp", b.TracingConfigName)
	require.Equal(t, "strip", b.ReqRewriterName)
	require.Equal(t, "auth", b.AuthenticatorName)
}

// An exact match outranks a prefix on the path itself, whatever their ages or
// conditions, because the router tries the exact tier first
func TestExactOutranksPrefixOnThePathItself(t *testing.T) {
	// an older prefix and a newer exact, both plain
	prefixRule, prefixGroup := serviceRule("older", 0, "older", prefix("/api"))
	exactRule, exactGroup := serviceRule("newer", 0, "newer", exact("/api"))
	m := model([]ir.Route{
		hostRoute("older", testHost, 0, prefixRule),
		hostRoute("newer", testHost, 1, exactRule),
	}, prefixGroup, exactGroup)
	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, "newer", body, "the declared exact must win the path itself")
	_, body = request(rtr, http.MethodGet, testHost, "/api/orders")
	require.Equal(t, "older", body, "the prefix keeps everything below it")

	// an older prefix with a header and a newer plain exact: the exact wins
	// the path outright, and the header is consulted only below it
	prefixRule, prefixGroup = serviceRule("older", 0, "older",
		withHeaders(prefix("/api"), "X-A", "1"))
	m = model([]ir.Route{
		hostRoute("older", testHost, 0, prefixRule),
		hostRoute("newer", testHost, 1, exactRule),
	}, prefixGroup, exactGroup)
	rtr = serveIR(t, m, serviceOpts(t))
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-A", "1")
	require.Equal(t, "newer", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api/x", "X-A", "1")
	require.Equal(t, "older", body)
	code, _ := request(rtr, http.MethodGet, testHost, "/api/x")
	require.Equal(t, http.StatusNotFound, code)

	// a newer exact with a header and an older plain prefix: the exact's
	// header is tested first, and a miss falls to the prefix
	exactRule, exactGroup = serviceRule("newer", 0, "newer",
		withHeaders(exact("/api"), "X-A", "1"))
	prefixRule, prefixGroup = serviceRule("older", 0, "older", prefix("/api"))
	m = model([]ir.Route{
		hostRoute("older", testHost, 0, prefixRule),
		hostRoute("newer", testHost, 1, exactRule),
	}, prefixGroup, exactGroup)
	rtr = serveIR(t, m, serviceOpts(t))
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-A", "1")
	require.Equal(t, "newer", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, "older", body)
}

// A late miss on one conditioned match falls to a conditioned cover, whose
// conditions are tested in full
func TestConditionMissFallsToAConditionedCover(t *testing.T) {
	var apiKV, rootKV []string
	for i := range 9 {
		apiKV = append(apiKV, "X-Api-"+strconv.Itoa(i), "1")
		rootKV = append(rootKV, "X-Root-"+strconv.Itoa(i), "1")
	}
	apiRule, api := serviceRule("api", 0, "api", withHeaders(exact("/api"), apiKV...))
	rootRule, root := serviceRule("root", 0, "root", withHeaders(prefix("/"), rootKV...))
	m := model([]ir.Route{
		hostRoute("api", testHost, 0, apiRule),
		hostRoute("root", testHost, 1, rootRule),
	}, api, root)
	rtr := serveIR(t, m, serviceOpts(t))
	// the first eight api headers pass, the ninth is missing, and every
	// root header passes
	headers := append(append([]string{}, apiKV[:16]...), rootKV...)
	code, body := request(rtr, http.MethodGet, testHost, "/api", headers...)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "root", body)
	_, body = request(rtr, http.MethodGet, testHost, "/api", apiKV...)
	require.Equal(t, "api", body)
}

// Methods a match was awarded after a conflict are not a declaration and
// confer no rank over a header match
func TestAwardedMethodsConferNoRank(t *testing.T) {
	plainRule, plain := serviceRule("plain", 0, "plain", withMethods(exact("/api"), http.MethodGet))
	headerRule, header := serviceRule("header", 0, "header", withHeaders(exact("/api"), "X-A", "1"))
	m := model([]ir.Route{
		hostRoute("plain", testHost, 0, plainRule),
		hostRoute("header", testHost, 1, headerRule),
	}, plain, header)
	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/api", "X-A", "1")
	require.Equal(t, "header", body, "an awarded subset must not confer method precedence")

	// the same shape with the method declared ranks the plain match first
	plainRule, plain = serviceRule("plain", 0, "plain", withMethod(exact("/api"), http.MethodGet))
	m = model([]ir.Route{
		hostRoute("plain", testHost, 0, plainRule),
		hostRoute("header", testHost, 1, headerRule),
	}, plain, header)
	rtr = serveIR(t, m, serviceOpts(t))
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-A", "1")
	require.Equal(t, "plain", body)
}

// A pattern the empty string satisfies must not match an absent field, whose
// value reads as empty; a present-but-empty field matches as declared
func TestEmptyMatchingPatternsRequireThePresence(t *testing.T) {
	anyHeader, anyHeaderGroup := serviceRule("anyheader", 0, "anyheader", ir.Match{
		Path:    ir.PathMatch{Type: ir.PathExact, Value: "/h"},
		Headers: []ir.KeyValueMatch{{Name: "X-Opt", Value: ".*", Regex: true}},
	})
	anyParam, anyParamGroup := serviceRule("anyparam", 0, "anyparam", ir.Match{
		Path:        ir.PathMatch{Type: ir.PathExact, Value: "/q"},
		QueryParams: []ir.KeyValueMatch{{Name: "opt", Value: ".*", Regex: true}},
	})
	digits, digitsGroup := serviceRule("digits", 0, "digits", ir.Match{
		Path:    ir.PathMatch{Type: ir.PathExact, Value: "/d"},
		Headers: []ir.KeyValueMatch{{Name: "X-N", Value: "[0-9]+", Regex: true}},
	})
	plainRule, plain := serviceRule("plain", 0, "plain", exact("/h"), exact("/q"), exact("/d"))
	m := model([]ir.Route{
		hostRoute("anyheader", testHost, 0, anyHeader),
		hostRoute("anyparam", testHost, 1, anyParam),
		hostRoute("digits", testHost, 2, digits),
		hostRoute("plain", testHost, 3, plainRule),
	}, anyHeaderGroup, anyParamGroup, digitsGroup, plain)

	got := emitted(t, m, serviceOpts(t))
	require.Len(t, got["kgw--httproute.shop.anyheader_r0"].Paths[0].MatchHeaders, 1,
		"the router requires presence itself; no presence condition is added")

	rtr := serveIR(t, m, serviceOpts(t))
	_, body := request(rtr, http.MethodGet, testHost, "/h")
	require.Equal(t, "plain", body, "an absent header must not satisfy .*")
	_, body = request(rtr, http.MethodGet, testHost, "/h", "X-Opt", "")
	require.Equal(t, "anyheader", body, "a present, empty header satisfies .*")
	_, body = request(rtr, http.MethodGet, testHost, "/h", "X-Opt", "x")
	require.Equal(t, "anyheader", body)
	_, body = request(rtr, http.MethodGet, testHost, "/q")
	require.Equal(t, "plain", body, "an absent parameter must not satisfy .*")
	_, body = request(rtr, http.MethodGet, testHost, "/q?opt=")
	require.Equal(t, "anyparam", body, "a present, empty parameter satisfies .*")
	_, body = request(rtr, http.MethodGet, testHost, "/q?opt=1")
	require.Equal(t, "anyparam", body)
	_, body = request(rtr, http.MethodGet, testHost, "/d")
	require.Equal(t, "plain", body)
	_, body = request(rtr, http.MethodGet, testHost, "/d", "X-N", "42")
	require.Equal(t, "digits", body)
}

// A route declaring only GET must not answer HEAD: the implicit HEAD
// candidate of a conditioned GET yields to the fill the planner registers
func TestConditionedGETOnlyRouteDoesNotAnswerHEAD(t *testing.T) {
	getRule, get := serviceRule("get", 0, "get",
		withMethod(withHeaders(exact("/api"), "X-Canary", "true"), http.MethodGet))
	m := model([]ir.Route{hostRoute("get", testHost, 0, getRule)}, get)
	rtr := serveIR(t, m, serviceOpts(t))

	code, body := request(rtr, http.MethodGet, testHost, "/api", "X-Canary", "true")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "get", body)
	code, _ = request(rtr, http.MethodHead, testHost, "/api", "X-Canary", "true")
	require.Equal(t, http.StatusNotFound, code, "the route declared only GET")
	code, _ = request(rtr, http.MethodHead, testHost, "/api")
	require.Equal(t, http.StatusNotFound, code)
	code, _ = request(rtr, http.MethodGet, testHost, "/api")
	require.Equal(t, http.StatusNotFound, code, "a condition miss is not the route's either")

	// the same route with a covering prefix: HEAD reaches the cover, never
	// the GET-only backend
	coverRule, cover := serviceRule("cover", 0, "cover", prefix("/"))
	m = model([]ir.Route{
		hostRoute("get", testHost, 0, getRule),
		hostRoute("cover", testHost, 1, coverRule),
	}, get, cover)
	rtr = serveIR(t, m, serviceOpts(t))
	_, body = request(rtr, http.MethodGet, testHost, "/api", "X-Canary", "true")
	require.Equal(t, "get", body)
	code, service := requestService(rtr, http.MethodHead, testHost, "/api", "X-Canary", "true")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "cover", service, "HEAD reaches the cover, never the GET-only backend")
}
