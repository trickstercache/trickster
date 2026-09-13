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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

func TestCompileCacheKeyComponentsOnCachingPaths(t *testing.T) {
	// cache key params and headers reach the caching paths, and only those: a non-caching
	// route hashes no key
	m := simple()
	m.Routes[0].Rules[0].Policy = "p1"
	m.Policies = []ir.Policy{{
		Name: "p1", CacheName: "objects",
		CacheKeyParams: []string{"q", "step"}, CacheKeyHeaders: []string{"X-Tenant"},
	}}
	b := emitted(t, m, serviceOpts(t))["kgw--httproute.shop.web_r0"]
	require.Equal(t, []string{"q", "step"}, b.Paths[0].CacheKeyParams)
	require.Equal(t, []string{"X-Tenant"}, b.Paths[0].CacheKeyHeaders)

	m.Policies[0].CacheName = ""
	m.Policies[0].Handler = ir.HandlerProxy
	pb := emitted(t, m, serviceOpts(t))["kgw--httproute.shop.web_r0"]
	require.Empty(t, pb.Paths[0].CacheKeyParams)
	require.Empty(t, pb.Paths[0].CacheKeyHeaders)
}

func TestCompileHidesTheResultHeaderWherePathsFaceTheClient(t *testing.T) {
	// the switch lands on every path a client's request is served from: a terminal's, an
	// ALB's, a redirect's, and a pool member's own catch-all, which serves the ALB's dispatch
	m := simple()
	m.Routes[0].Rules[0].Policy = "p1"
	m.Policies = []ir.Policy{{Name: "p1", ResultHeader: ir.ResultHeaderHide}}
	b := emitted(t, m, serviceOpts(t))["kgw--httproute.shop.web_r0"]
	for _, p := range b.Paths {
		require.True(t, p.HideResultHeader)
	}
	shown := emitted(t, simple(), serviceOpts(t))["kgw--httproute.shop.web_r0"]
	require.False(t, shown.Paths[0].HideResultHeader)

	g := group("shop", "web", 0,
		svcMember(0, "shop", "a-svc", 80, 1), svcMember(1, "shop", "b-svc", 80, 1))
	w := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name, Policy: "p1"})},
		Backends:  []ir.BackendGroup{g},
		Policies:  []ir.Policy{{Name: "p1", ResultHeader: ir.ResultHeaderHide}},
	}
	docs := emitted(t, w, serviceOpts(t))
	require.True(t, docs["kgw--httproute.shop.web_r0"].Paths[0].HideResultHeader)
	require.True(t, docs["kgw--httproute.shop.web_r0_b0"].Paths[0].HideResultHeader)

	e := endpointShape()
	e.Policies[0].ResultHeader = ir.ResultHeaderHide
	docs = emitted(t, e, serviceOpts(t))
	require.True(t, docs["kgw--httproute.shop.web_r0"].Paths[0].HideResultHeader)
	require.True(t, docs["kgw--httproute.shop.web_r0_b0_tmpl"].Paths[0].HideResultHeader)

	r := simple()
	r.Routes[0].Rules[0].Policy = "p1"
	r.Routes[0].Rules[0].Filters = []ir.Filter{{
		Type: ir.FilterRedirect, Redirect: &ir.RedirectFilter{Scheme: "https"},
	}}
	r.Policies = []ir.Policy{{Name: "p1", ResultHeader: ir.ResultHeaderHide}}
	require.True(t, emitted(t, r, serviceOpts(t))["kgw--httproute.shop.web_r0"].
		Paths[0].HideResultHeader)
}

func TestCompileWeightedMembersDecideTheirOwnResultHeader(t *testing.T) {
	// A weighted rule's ALB carries the rule's disposition for what it answers itself, and each
	// member carries its own, so a Service policy exposing the header is honored behind a
	// hiding rule and a member without one inherits the rule's
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a-svc", 80, 1), svcMember(1, "shop", "b-svc", 80, 1))
	g.Members[0].Policy = "expose"
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name, Policy: "rule"})},
		Backends:  []ir.BackendGroup{g},
		Policies: []ir.Policy{
			{Name: "rule", ResultHeader: ir.ResultHeaderHide},
			{Name: "expose", ResultHeader: ir.ResultHeaderExpose},
		},
	}
	docs := emitted(t, m, serviceOpts(t))
	require.True(t, docs["kgw--httproute.shop.web_r0"].Paths[0].HideResultHeader)
	require.False(t, docs["kgw--httproute.shop.web_r0_b0"].Paths[0].HideResultHeader)
	require.True(t, docs["kgw--httproute.shop.web_r0_b1"].Paths[0].HideResultHeader)

	e := endpointShape()
	e.Backends[0].Members = append(e.Backends[0].Members, svcMember(1, "shop", "b-svc", 8080, 1))
	e.Backends[0].Members[0].Policy = "expose"
	e.Policies = append(e.Policies, m.Policies...)
	e.Routes[0].Rules[0].Policy = "rule"
	docs = emitted(t, e, endpointOpts(t))
	require.True(t, docs["kgw--httproute.shop.web_r0"].Paths[0].HideResultHeader)
	require.False(t, docs["kgw--httproute.shop.web_r0_b0_tmpl"].Paths[0].HideResultHeader)
	require.True(t, docs["kgw--httproute.shop.web_r0_b1_tmpl"].Paths[0].HideResultHeader)
}

func TestCompileMemberPolicyOverlaysTheRules(t *testing.T) {
	// A policy on a member's Service is written over the rule's for that member alone
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a-svc", 80, 1), svcMember(1, "shop", "b-svc", 80, 1))
	g.Members[1].Policy = "svc"
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name, Policy: "rule"})},
		Backends:  []ir.BackendGroup{g},
		Policies: []ir.Policy{
			{Name: "rule", CacheName: "objects", TimeoutMS: 5000},
			{
				Name: "svc", TimeoutMS: 9000, Provider: providers.Prometheus,
				RequestHeaders: map[string]string{"X-Svc": "1"},
			},
		},
	}
	docs := emittedWith(t, m, serviceOpts(t), promPaths)
	a, b := docs["kgw--httproute.shop.web_r0_b0"], docs["kgw--httproute.shop.web_r0_b1"]
	require.Equal(t, providers.ReverseProxyCacheShort, a.Provider)
	require.Equal(t, "5s", a.Timeout)
	require.Empty(t, a.Paths[0].RequestHeaders)
	require.Equal(t, providers.Prometheus, b.Provider)
	require.Equal(t, "objects", b.CacheName, "the rule's setting is kept where the member sets none")
	require.Equal(t, "9s", b.Timeout)
	require.Equal(t, map[string]string{"X-Svc": "1"}, b.Paths[0].RequestHeaders)

	// a single member's policy reaches its direct backend the same way
	s := simple()
	s.Backends[0].Members[0].Policy = "svc"
	s.Policies = m.Policies
	one := emittedWith(t, s, serviceOpts(t), promPaths)["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.Prometheus, one.Provider)
	require.Equal(t, "9s", one.Timeout)

	// an unknown member policy name applies nothing
	s.Backends[0].Members[0].Policy = "absent"
	none := emitted(t, s, serviceOpts(t))["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.ReverseProxyShort, none.Provider)
}

func TestResolveMember(t *testing.T) {
	opts := serviceOpts(t)
	rule := &ir.Policy{CacheName: "a", TimeoutMS: 1000}
	require.Equal(t, resolve(opts, rule), resolveMember(opts, rule, nil))
	e := resolveMember(opts, nil, &ir.Policy{Provider: providers.Graphite})
	require.Equal(t, providers.Graphite, e.provider())
	require.True(t, e.caches())
	require.Equal(t, handlerProxyCache, e.handler())
	e = resolveMember(opts, rule, &ir.Policy{Handler: ir.HandlerProxy})
	require.Equal(t, handlerProxy, e.handler())
	require.Equal(t, "a", e.cacheName)
}
