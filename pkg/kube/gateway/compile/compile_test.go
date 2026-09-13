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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	cacheregistry "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/template"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func serviceOpts(t *testing.T) *kubecfg.Options {
	t.Helper()
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeService
	require.NoError(t, o.Validate())
	return o
}

func route(ns, name string, rules ...ir.Rule) ir.Route {
	return ir.Route{
		Name:      ns + "." + name,
		Source:    src("HTTPRoute", ns, name),
		Listeners: []string{"l80"},
		Rules:     rules,
	}
}

func httpListener() ir.Listener {
	return ir.Listener{
		Name: "l80", Port: 80, Protocol: ir.ProtocolHTTP,
		Source: src("Gateway", "infra", "gw"),
	}
}

func group(ns, name string, ruleIndex int, members ...ir.BackendMember) ir.BackendGroup {
	return ir.BackendGroup{
		Name:      ns + "." + name + "-g",
		Source:    src("HTTPRoute", ns, name),
		RuleIndex: ruleIndex,
		Members:   members,
	}
}

func svcMember(refIndex int, ns, name string, port int32, weight int) ir.BackendMember {
	return ir.BackendMember{
		RefIndex: refIndex, Weight: weight,
		Service: ir.ServiceTarget{Namespace: ns, Name: name, Port: port},
	}
}

func simple() *ir.IR {
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	return &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{func() ir.Route {
			r := route("shop", "web", ir.Rule{
				Matches: []ir.Match{{
					Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"},
				}},
				BackendGroup: g.Name,
			})
			r.Hostnames = []string{"shop.example.com"}
			return r
		}()},
		Backends: []ir.BackendGroup{g},
	}
}

func emitted(t *testing.T, m *ir.IR, opts *kubecfg.Options) map[string]*backendDoc {
	t.Helper()
	doc, err := buildDocument(m, opts, nil)
	require.NoError(t, err)
	return doc.Backends
}

// generatedConfig is every section the compiler may emit, in the real configuration types, so
// decoding into it proves the emit structs do not diverge from the config
type generatedConfig struct {
	Discovery        do.Lookup                    `yaml:"discovery"`
	Listeners        map[string]*listener.Options `yaml:"listeners"`
	Backends         map[string]*bo.Options       `yaml:"backends"`
	Rules            ro.Lookup                    `yaml:"rules"`
	RequestRewriters rwopts.Lookup                `yaml:"request_rewriters"`
}

func decode(t *testing.T, data []byte) generatedConfig {
	t.Helper()
	var out generatedConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	require.NoError(t, dec.Decode(&out),
		"generated configuration must decode into the real option types")
	return out
}

func TestCompileServiceMode(t *testing.T) {
	o, err := Compile(simple(), serviceOpts(t))
	require.NoError(t, err)
	require.Equal(t, Prefix, o.Prefix)
	require.NotEmpty(t, o.Version)
	require.False(t, o.IsEmpty())

	got := decode(t, o.Data)
	require.Len(t, got.Backends, 1)
	b, ok := got.Backends["kgw--httproute.shop.web_r0"]
	require.True(t, ok, "backends: %v", got.Backends)
	require.Equal(t, providers.ReverseProxyShort, b.Provider)
	require.Equal(t, "http://web-svc.shop.svc:8080", b.OriginURL)
	require.Equal(t, []string{"shop.example.com"}, b.Hosts)
	require.True(t, b.PathRoutingDisabled)
	require.Equal(t, "httproute.shop.web", b.CacheKeyPrefix)
	require.Len(t, b.Paths, 1)
	require.Equal(t, "/api", b.Paths[0].Path)
	require.EqualValues(t, "segment", b.Paths[0].MatchTypeName)

	require.Len(t, got.Listeners, 1)
	l, ok := got.Listeners["kgw--listener-http-80"]
	require.True(t, ok, "listeners: %v", got.Listeners)
	require.Equal(t, 80, l.ListenPort)
	require.Zero(t, l.TLSListenPort)
}

func TestCompileNamesAllCarryTheReservedPrefix(t *testing.T) {
	// Every generated object must carry the reserved prefix, which is what keeps
	// generated and hand-written configuration from ever colliding
	m := simple()
	m.Backends[0].Members = append(m.Backends[0].Members,
		svcMember(1, "shop", "canary", 8080, 1))
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)
	for name := range got.Backends {
		require.Equal(t, Prefix, reserved.MatchNamePrefix(name), "backend %q", name)
	}
	for name := range got.Listeners {
		require.Equal(t, Prefix, reserved.MatchNamePrefix(name), "listener %q", name)
	}
}

func TestCompileWeightedBackendRefs(t *testing.T) {
	// Several backendRefs become a weighted round-robin ALB over one generated
	// backend per ref, which is how Gateway API weights are realized
	g := group("shop", "web", 0,
		svcMember(0, "shop", "stable", 80, 9),
		svcMember(1, "shop", "canary", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)

	alb, ok := got.Backends["kgw--httproute.shop.web_r0"]
	require.True(t, ok)
	require.Equal(t, providers.ALB, alb.Provider)
	require.Equal(t, "rr", alb.ALBOptions.MechanismName)
	require.Len(t, alb.ALBOptions.Pool, 2)
	require.Equal(t, "kgw--httproute.shop.web_r0_b0", alb.ALBOptions.Pool[0].Name)
	require.Equal(t, 9, alb.ALBOptions.Pool[0].Weight)
	require.Equal(t, "kgw--httproute.shop.web_r0_b1", alb.ALBOptions.Pool[1].Name)
	require.Equal(t, 1, alb.ALBOptions.Pool[1].Weight)

	stable := got.Backends["kgw--httproute.shop.web_r0_b0"]
	require.Equal(t, "http://stable.shop.svc:80", stable.OriginURL)
	require.Empty(t, stable.Hosts, "hosts attach to the ALB, not its members")

	// a rule with no matches answers everything the route admits, and the
	// ALB's own paths must select the alb handler
	require.Len(t, alb.Paths, 1)
	require.Equal(t, "/", alb.Paths[0].Path)
	require.Equal(t, providers.ALB, alb.Paths[0].HandlerName)
}

func TestCompileInvalidBackendRef(t *testing.T) {
	// An unresolvable backendRef must hold its share of the traffic and answer
	// it, rather than shifting that share onto its siblings
	g := group("shop", "web", 0,
		svcMember(0, "shop", "stable", 80, 1),
		ir.BackendMember{
			RefIndex: 1, Weight: 1, Invalid: true,
			InvalidReason: "Service not found",
		})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)

	bad := got.Backends["kgw--httproute.shop.web_r0_b1"]
	require.Equal(t, providers.ReverseProxyShort, bad.Provider)
	// an origin backend must name an origin to validate at all; this one is
	// never dialed, because its only path answers locally
	require.Equal(t, unresolvedOriginURL, bad.OriginURL)
	require.Len(t, bad.Paths, 1)
	require.Equal(t, "localresponse", bad.Paths[0].HandlerName)
	require.Equal(t, 500, bad.Paths[0].ResponseCode)

	alb := got.Backends["kgw--httproute.shop.web_r0"]
	require.Len(t, alb.ALBOptions.Pool, 2,
		"the invalid ref keeps its pool slot and its weight")
}

func TestCompileSingleInvalidBackendRef(t *testing.T) {
	// A single invalid ref still becomes an ALB, so the responder is reached
	// through the same shape as any other pool
	g := group("shop", "web", 0,
		ir.BackendMember{RefIndex: 0, Weight: 1, Invalid: true})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)
	require.Equal(t, providers.ALB, got.Backends["kgw--httproute.shop.web_r0"].Provider)
	require.Equal(t, "localresponse",
		got.Backends["kgw--httproute.shop.web_r0_b0"].Paths[0].HandlerName)
}

func TestCompileTLSListener(t *testing.T) {
	// An https listener keeps its port open with no certificate file behind it,
	// because Secret-sourced certificates only arrive once the controller runs
	m := simple()
	m.Listeners = append(m.Listeners, ir.Listener{
		Name: "l443", Port: 443, Protocol: ir.ProtocolHTTPS,
		CertRefs: []string{"c1"},
		Source:   ir.Source{Kind: "Gateway", Namespace: "infra", Name: "gw"},
	})
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)

	tls, ok := got.Listeners["kgw--listener-https-443"]
	require.True(t, ok, "listeners: %v", got.Listeners)
	require.Equal(t, 443, tls.TLSListenPort)
	require.Zero(t, tls.ListenPort)
	require.True(t, tls.TLSRuntimeCerts)
}

func TestCompileMergesListenersOnOnePort(t *testing.T) {
	// Two Gateways on one port are the Gateway API's same-port merge; a port can
	// only be bound once
	m := simple()
	m.Listeners = append(m.Listeners, ir.Listener{
		Name: "l80-b", Port: 80, Protocol: ir.ProtocolHTTP,
		Source: ir.Source{Kind: "Gateway", Namespace: "other", Name: "gw2"},
	})
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	require.Len(t, decode(t, o.Data).Listeners, 1)
}

func TestCompileDefaultsApplied(t *testing.T) {
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default"
	opts.Defaults.Timeout = timeconv.Duration(45 * time.Second)

	o, err := Compile(simple(), opts)
	require.NoError(t, err)
	b := decode(t, o.Data).Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, "default", b.CacheName)
	require.Equal(t, timeconv.Duration(45*time.Second), b.Timeout)
	require.Equal(t, "proxycache", b.Paths[0].HandlerName,
		"a configured cache makes generated paths cache")
}

func TestCompilePathMatchTypes(t *testing.T) {
	for irType, want := range map[string]string{
		ir.PathExact:  "exact",
		ir.PathPrefix: "segment",
		ir.PathRegex:  "regex",
	} {
		g := group("shop", "web", 0, svcMember(0, "shop", "s", 80, 1))
		m := &ir.IR{
			Listeners: []ir.Listener{httpListener()},
			Routes: []ir.Route{route("shop", "web", ir.Rule{
				Matches: []ir.Match{{
					Path:    ir.PathMatch{Type: irType, Value: "/p"},
					Methods: []string{"GET"},
				}},
				BackendGroup: g.Name,
			})},
			Backends: []ir.BackendGroup{g},
		}
		o, err := Compile(m, serviceOpts(t))
		require.NoError(t, err)
		p := decode(t, o.Data).Backends["kgw--httproute.shop.web_r0"].Paths[0]
		require.EqualValues(t, want, p.MatchTypeName, "ir type %q", irType)
		require.Equal(t, []string{"GET"}, p.Methods)
	}
}

func TestCompileEmptyIR(t *testing.T) {
	// An empty IR must produce no overlay data at all, rather than a document of
	// empty sections that would look like a change
	for _, m := range []*ir.IR{nil, {}} {
		o, err := Compile(m, serviceOpts(t))
		require.NoError(t, err)
		require.True(t, o.IsEmpty())
		require.NotEmpty(t, o.Version)
	}
}

func TestCompileUnknownBackendGroupIsSkipped(t *testing.T) {
	// A rule whose group the translator could not resolve routes nowhere and is omitted rather
	// than failing the compile; the Gateway's listener still compiles with no routes
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{
			route("shop", "web", ir.Rule{BackendGroup: "missing"}),
		},
	}
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	require.Empty(t, routeBackends(decode(t, o.Data)))
	require.Len(t, decode(t, o.Data).Listeners, 1)

	empty := group("shop", "web", 0)
	m2 := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: empty.Name})},
		Backends:  []ir.BackendGroup{empty},
	}
	o2, err := Compile(m2, serviceOpts(t))
	require.NoError(t, err)
	require.Empty(t, routeBackends(decode(t, o2.Data)))
}

func TestCompileErrors(t *testing.T) {
	_, err := Compile(simple(), nil)
	require.ErrorIs(t, err, ErrNoOptions)

	// a routing mode the compiler does not know must say so rather than
	// silently emitting service-mode configuration
	none := kubecfg.New()
	_, err = Compile(simple(), none)
	require.ErrorIs(t, err, ErrUnsupportedRoutingMode)

	// the same refusal must reach the multi-member path, not just the
	// single-backend shortcut
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	multi := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	_, err = Compile(multi, none)
	require.ErrorIs(t, err, ErrUnsupportedRoutingMode)
}

func TestCompileIsDeterministic(t *testing.T) {
	// The overlay is fed to the config loader, whose version gate is the IR
	// hash: identical IRs must produce byte-identical output
	m := simple()
	m.Backends[0].Members = append(m.Backends[0].Members,
		svcMember(1, "shop", "canary", 8080, 1))
	m.Listeners = append(m.Listeners, ir.Listener{
		Name: "l443", Port: 443, Protocol: ir.ProtocolHTTPS,
	})
	first, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	for range 8 {
		next, err := Compile(m, serviceOpts(t))
		require.NoError(t, err)
		require.Equal(t, string(first.Data), string(next.Data))
		require.Equal(t, first.Version, next.Version)
	}
}

func TestCompileBindsBackendsToTheirListeners(t *testing.T) {
	// A generated backend must name the listeners its route is attached to, or validation binds
	// it to the default frontend, the generated listener never starts, and it answers elsewhere
	m := simple()
	m.Listeners = append(m.Listeners, ir.Listener{
		Name: "l443", Port: 443, Protocol: ir.ProtocolHTTPS,
		Source: src("Gateway", "infra", "gw"),
	})
	m.Routes[0].Listeners = []string{"l443", "l80"}

	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)
	b := got.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, []string{
		"kgw--listener-http-80", "kgw--listener-https-443",
	}, b.ListenerNames, "listener names must be resolved, ordered and complete")

	// every name the backend claims must be a listener the overlay defines
	for _, n := range b.ListenerNames {
		require.Contains(t, got.Listeners, n)
	}
}

func TestCompileDeduplicatesMergedListenerNames(t *testing.T) {
	// Several IR listeners merging onto one port must not produce a repeated
	// listener name on the backend
	m := simple()
	m.Listeners = append(m.Listeners, ir.Listener{
		Name: "l80-b", Port: 80, Protocol: ir.ProtocolHTTP,
		Source: src("Gateway", "other", "gw2"),
	})
	m.Routes[0].Listeners = []string{"l80", "l80-b"}

	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	b := decode(t, o.Data).Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, []string{"kgw--listener-http-80"}, b.ListenerNames)
}

func TestCompileSkipsUnattachedRoutes(t *testing.T) {
	// A route attached to no listener has no way to receive traffic; emitting it would bind it
	// to the default frontend and expose it on an unrelated port
	m := simple()
	m.Routes[0].Listeners = nil
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	require.Empty(t, routeBackends(decode(t, o.Data)))

	// naming a listener the IR does not define is the same situation
	m.Routes[0].Listeners = []string{"nonexistent"}
	o, err = Compile(m, serviceOpts(t))
	require.NoError(t, err)
	require.Empty(t, routeBackends(decode(t, o.Data)))
}

func TestCompileALBPathsAlwaysUseTheALBHandler(t *testing.T) {
	// An ALB exposes only the alb and localresponse handlers and registration drops a path naming
	// any other, so every path on a generated ALB must select alb whatever the cache implies
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default" // would make a terminal proxycache

	g := group("shop", "web", 0,
		svcMember(0, "shop", "stable", 80, 9),
		svcMember(1, "shop", "canary", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{route("shop", "web", ir.Rule{
			Matches: []ir.Match{
				{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/a"}},
				{Path: ir.PathMatch{Type: ir.PathExact, Value: "/b"}},
			},
			BackendGroup: g.Name,
		})},
		Backends: []ir.BackendGroup{g},
	}
	o, err := Compile(m, opts)
	require.NoError(t, err)
	got := decode(t, o.Data)

	alb := got.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.ALB, alb.Provider)
	require.Len(t, alb.Paths, 2)
	for _, p := range alb.Paths {
		require.Equal(t, providers.ALB, p.HandlerName,
			"path %q on an ALB must select the alb handler", p.Path)
	}
	// caching stays where it can act: on the selected member
	require.Equal(t, "default", got.Backends["kgw--httproute.shop.web_r0_b0"].CacheName)

	// the single-invalid-ref shape is also an ALB and gets the same handler
	inv := group("shop", "only", 0,
		ir.BackendMember{RefIndex: 0, Weight: 1, Invalid: true})
	mi := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "only", ir.Rule{BackendGroup: inv.Name})},
		Backends:  []ir.BackendGroup{inv},
	}
	oi, err := Compile(mi, opts)
	require.NoError(t, err)
	gi := decode(t, oi.Data)
	require.Equal(t, providers.ALB,
		gi.Backends["kgw--httproute.shop.only_r0"].Paths[0].HandlerName)
}

func TestCompileMembersCarryTheALBListeners(t *testing.T) {
	// Pool members are reachable only through their ALB, so they must not be
	// left to bind to the default frontend and activate a port serving nothing
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)
	for _, name := range []string{
		"kgw--httproute.shop.web_r0_b0", "kgw--httproute.shop.web_r0_b1",
	} {
		b := got.Backends[name]
		require.Equal(t, []string{"kgw--listener-http-80"}, b.ListenerNames, name)
		require.True(t, b.PathRoutingDisabled, name)
		require.Empty(t, b.Hosts, name)
	}
}

func TestCompilePolicyOverridesDefaults(t *testing.T) {
	// The IR contract says a rule may name a policy overriding the defaults;
	// the compiler must actually apply it
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default"
	opts.Defaults.Timeout = timeconv.Duration(10 * time.Second)

	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 80, 1))
	r := route("shop", "web", ir.Rule{
		BackendGroup: g.Name, Policy: "p1",
	})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r},
		Backends:  []ir.BackendGroup{g},
		Policies: []ir.Policy{{
			Name:      "p1",
			Source:    r.Source,
			CacheName: "special",
			TimeoutMS: 90000,
		}},
	}
	b := emitted(t, m, opts)["kgw--httproute.shop.web_r0"]
	require.Equal(t, "special", b.CacheName)
	require.Equal(t, "1m30s", b.Timeout)
	require.Equal(t, providers.ReverseProxyCacheShort, b.Provider)

	// a policy naming a handler wins over the cache-derived default, and a
	// non-caching handler leaves no cache the backend would ignore
	m.Policies[0].Handler = "proxy"
	p := emitted(t, m, opts)["kgw--httproute.shop.web_r0"]
	require.Equal(t, "proxy", p.Paths[0].Handler)
	require.Equal(t, providers.ReverseProxyShort, p.Provider)
	require.Empty(t, p.CacheName)
}

func TestCompilePolicyRoutingModeOverride(t *testing.T) {
	// A policy may override the routing mode for one route
	m := endpointShape()
	m.Policies = []ir.Policy{{Name: "p1", RoutingMode: kubecfg.RoutingModeEndpoint}}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Equal(t, providers.ALB, doc.Backends["kgw--httproute.shop.web_r0"].Provider,
		"a per-route routing mode must reach the member compiler")
	require.Contains(t, doc.Backends, "kgw--httproute.shop.web_r0_b0_tmpl")
}

func endpointShape() *ir.IR {
	// endpointShape is a single-member rule whose policy selects the endpoint
	// routing mode
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	g.Members[0].Service.PortName = "http"
	return &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{route("shop", "web", ir.Rule{
			Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}}},
			BackendGroup: g.Name, Policy: "p1",
		})},
		Backends: []ir.BackendGroup{g},
		Policies: []ir.Policy{{Name: "p1", RoutingMode: kubecfg.RoutingModeEndpoint}},
	}
}

func endpointOpts(t *testing.T) *kubecfg.Options {
	// endpointOpts is a validated section in endpoint routing mode
	t.Helper()
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeEndpoint
	o.Connection.Kubeconfig = "/etc/trickster/kubeconfig"
	o.Connection.InCluster = false
	require.NoError(t, o.Validate())
	return o
}

func TestCompileEndpointMode(t *testing.T) {
	// In endpoint mode a member is an ALB whose pool is discovered from the Service's
	// EndpointSlices, cloned from a template carrying everything the origin would have
	opts := endpointOpts(t)
	opts.Defaults.CacheName = "default"
	opts.Defaults.Timeout = timeconv.Duration(15 * time.Second)
	m := endpointShape()
	m.Policies = nil
	m.Routes[0].Rules[0].Policy = ""
	doc, err := buildDocument(m, opts, nil)
	require.NoError(t, err)

	require.Len(t, doc.Discovery, 1)
	disc := doc.Discovery[DiscovererName()]
	require.NotNil(t, disc)
	require.Equal(t, "kubernetes", disc.Provider)
	require.Equal(t, "/etc/trickster/kubeconfig", disc.Kubernetes.Kubeconfig,
		"the generated discoverer uses the controller's connection")
	require.NotSame(t, opts.Connection, disc.Kubernetes, "the connection is copied")

	front := doc.Backends["kgw--httproute.shop.web_r0"]
	require.NotNil(t, front)
	require.Equal(t, providers.ALB, front.Provider)
	require.Empty(t, front.CacheKeyPrefix, "an ALB caches nothing")
	require.True(t, front.AnyHostRouting, "the fixture route names no hostname")
	require.Equal(t, []string{ListenerName(80, ir.ProtocolHTTP)}, front.ListenerNames)
	for _, p := range front.Paths {
		require.Equal(t, providers.ALB, p.Handler)
	}
	require.NotNil(t, front.ALB)
	require.NotNil(t, front.ALB.Discovery)
	require.Empty(t, front.ALB.Pool, "the pool is discovered, not static")
	d := front.ALB.Discovery
	require.Equal(t, DiscovererName(), d.DiscovererName)
	require.Equal(t, "kgw--httproute.shop.web_r0_b0_tmpl", d.TemplateBackend)
	require.Equal(t, "provider", d.HealthMode)
	require.Equal(t, &queryDoc{
		Kind: "endpointslices", Namespace: "shop",
		Service: "web-svc", Port: "http",
	}, d.Query)

	tmpl := doc.Backends[d.TemplateBackend]
	require.NotNil(t, tmpl)
	require.True(t, tmpl.IsTemplate)
	require.Empty(t, tmpl.OriginURL, "the discovered member supplies the address")
	require.Equal(t, providers.ReverseProxyCacheShort, tmpl.Provider)
	require.Equal(t, "default", tmpl.CacheName)
	require.Equal(t, "15s", tmpl.Timeout)
	require.Equal(t, "httproute.shop.web", tmpl.CacheKeyPrefix)
	require.True(t, tmpl.PathRoutingDisabled)
	require.Nil(t, tmpl.HealthCheck, "provider mode runs no probe")
	require.Empty(t, tmpl.Hosts, "a template registers nothing itself")
	require.Len(t, tmpl.Paths, 2, "the cache split applies to the clones")
	require.Equal(t, "/", tmpl.Paths[0].Path)
}

func TestCompileEndpointModeProbe(t *testing.T) {
	// The probe health mode gives the template the configured health check, or a
	// default one, so the clones actually probe; the policy may select it
	opts := endpointOpts(t)
	opts.Defaults.HealthMode = "probe"
	opts.Defaults.HealthCheck = &ho.Options{Path: "/healthz"}
	m := endpointShape()
	m.Policies = nil
	m.Routes[0].Rules[0].Policy = ""
	doc, err := buildDocument(m, opts, nil)
	require.NoError(t, err)
	front := doc.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, "probe", front.ALB.Discovery.HealthMode)
	tmpl := doc.Backends[front.ALB.Discovery.TemplateBackend]
	require.NotNil(t, tmpl.HealthCheck)
	require.Equal(t, "/healthz", tmpl.HealthCheck.Path)
	require.Equal(t, timeconv.Duration(kubecfg.DefaultProbeInterval), tmpl.HealthCheck.Interval,
		"a health check naming no interval would never run")
	require.Zero(t, opts.Defaults.HealthCheck.Interval, "the configured block is not mutated")

	// no configured health check: the default probe
	opts.Defaults.HealthCheck = nil
	doc, err = buildDocument(m, opts, nil)
	require.NoError(t, err)
	tmpl = doc.Backends["kgw--httproute.shop.web_r0_b0_tmpl"]
	require.Equal(t, &ho.Options{Interval: timeconv.Duration(kubecfg.DefaultProbeInterval)},
		tmpl.HealthCheck)

	// the policy selects the mode for one route
	opts.Defaults.HealthMode = "provider"
	m.Policies = []ir.Policy{{Name: "p1", HealthMode: "probe"}}
	m.Routes[0].Rules[0].Policy = "p1"
	doc, err = buildDocument(m, opts, nil)
	require.NoError(t, err)
	require.Equal(t, "probe", doc.Backends["kgw--httproute.shop.web_r0"].ALB.Discovery.HealthMode)
	require.NotNil(t, doc.Backends["kgw--httproute.shop.web_r0_b0_tmpl"].HealthCheck)
}

func TestCompileEndpointModeWeighted(t *testing.T) {
	// A weighted rule in endpoint mode is an outer static ALB over one discovered
	// ALB per member, each with its own template and query
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 3), svcMember(1, "shop", "b", 80, 1))
	g.Members[1].Service.Scheme = ir.ProtocolHTTPS
	g.Members[1].TLS = &ir.BackendTLS{Hostname: "b.internal", System: true}
	g.Members[1].Filters = []ir.Filter{{
		Type:       ir.FilterURLRewrite,
		URLRewrite: &ir.URLRewriteFilter{Path: &ir.PathModifier{Type: ir.PathReplaceFull, Value: "/b"}},
	}}
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	doc, err := buildDocument(m, endpointOpts(t), nil)
	require.NoError(t, err)
	outer := doc.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.ALB, outer.Provider)
	require.Nil(t, outer.ALB.Discovery)
	require.Len(t, outer.ALB.Pool, 2)
	require.Len(t, doc.Discovery, 1)

	for _, name := range []string{"kgw--httproute.shop.web_r0_b0", "kgw--httproute.shop.web_r0_b1"} {
		member := doc.Backends[name]
		require.NotNil(t, member, name)
		require.Equal(t, providers.ALB, member.Provider)
		require.Empty(t, member.Hosts, "a pool member registers no route of its own")
		require.False(t, member.AnyHostRouting)
		require.Equal(t, []string{ListenerName(80, ir.ProtocolHTTP)}, member.ListenerNames)
		require.Equal(t, name+"_tmpl", member.ALB.Discovery.TemplateBackend)
		require.Len(t, member.Paths, 1)
		require.Equal(t, providers.ALB, member.Paths[0].Handler)
		require.Contains(t, doc.Backends, name+"_tmpl")
	}
	b := doc.Backends["kgw--httproute.shop.web_r0_b1"]
	require.Equal(t, "https", b.ALB.Discovery.Query.Scheme, "a TLS member's endpoints are dialed over TLS")
	tmpl := doc.Backends["kgw--httproute.shop.web_r0_b1_tmpl"]
	require.Equal(t, &tlsDoc{ServerName: "b.internal"}, tmpl.TLS)
	require.Equal(t, "kgw--httproute.shop.web_r0_b1_w", tmpl.Paths[0].ReqRewriterName,
		"the member's own rewrite runs on the template's path")
	require.Equal(t, [][]string{{"path", "set", "/b"}},
		doc.RequestRewriters["kgw--httproute.shop.web_r0_b1_w"].Instructions)
	require.Empty(t, doc.Backends["kgw--httproute.shop.web_r0_b0_tmpl"].TLS)
}

func TestCompileRedirect(t *testing.T) {
	// A redirecting rule is one backend answering every match from the redirect
	// handler, with a rewriter composing the Location from the filter
	g := group("shop", "web", 0)
	r := route("shop", "web", ir.Rule{
		Matches: []ir.Match{
			{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/old"}},
		},
		BackendGroup: g.Name,
		Filters: []ir.Filter{{Type: ir.FilterRedirect, Redirect: &ir.RedirectFilter{
			Scheme: "https", Hostname: "new.example.com", Port: 8443, StatusCode: 301,
			Path: &ir.PathModifier{Type: ir.PathReplacePrefix, Value: "/new"},
		}}},
	})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r},
		Backends:  []ir.BackendGroup{g},
	}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Len(t, doc.Backends, 1)
	b := doc.Backends["kgw--httproute.shop.web_r0"]
	require.NotNil(t, b, "a redirecting rule with no members still compiles")
	require.Equal(t, providers.ReverseProxyShort, b.Provider)
	require.Equal(t, redirectOriginURL, b.OriginURL)
	require.Empty(t, b.CacheKeyPrefix)
	require.Len(t, b.Paths, 1)
	for _, p := range b.Paths {
		require.Equal(t, handlerRedirect, p.Handler)
		require.Equal(t, 301, p.ResponseCode)
		require.Equal(t, "kgw--httproute.shop.web_r0_w0", p.ReqRewriterName)
	}
	require.Equal(t, [][]string{
		{"scheme", "set", "https"},
		{"hostname", "set", "new.example.com"},
		{"port", "set", "8443"},
		{"path", "prefix-replace", "/old", "/new"},
	}, doc.RequestRewriters["kgw--httproute.shop.web_r0_w0"].Instructions)

	// an unset status is the default redirection, and the endpoint routing
	// mode changes nothing for a rule that forwards nowhere
	r.Rules[0].Filters[0].Redirect.StatusCode = 0
	m.Routes[0] = r
	doc, err = buildDocument(m, endpointOpts(t), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, doc.Backends["kgw--httproute.shop.web_r0"].Paths[0].ResponseCode)
	require.Empty(t, doc.Discovery)
}

func TestCompileHeaderFilters(t *testing.T) {
	// Header filters fold into one operation per header, in declaration order,
	// so the emitted map never says two things about one header
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	g.Members[0].Filters = []ir.Filter{{
		Type:           ir.FilterRequestHeaders,
		RequestHeaders: &ir.HeaderFilter{Set: []ir.Header{{Name: "X-Member", Value: "m"}}},
	}}
	p := ir.Policy{Name: "p", RequestHeaders: map[string]string{"X-Policy": "1", "-X-Kept": ""}}
	r := route("shop", "web", ir.Rule{
		BackendGroup: g.Name, Policy: "p",
		Filters: []ir.Filter{
			{Type: ir.FilterRequestHeaders, RequestHeaders: &ir.HeaderFilter{
				Set:    []ir.Header{{Name: "X-Set", Value: "a"}, {Name: "x-kept", Value: "back"}},
				Add:    []ir.Header{{Name: "X-Set", Value: "b"}, {Name: "X-Add", Value: "c"}},
				Remove: []string{"X-Gone", "X-Set-Then-Gone"},
			}},
			{Type: ir.FilterURLRewrite, URLRewrite: &ir.URLRewriteFilter{Hostname: "web.internal"}},
			{Type: ir.FilterResponseHeaders, ResponseHeaders: &ir.HeaderFilter{
				Add:    []ir.Header{{Name: "Vary", Value: "A"}, {Name: "Vary", Value: "B"}},
				Remove: []string{"Server", "X-Re-Added"},
			}},
			{Type: ir.FilterResponseHeaders, ResponseHeaders: &ir.HeaderFilter{
				Add: []ir.Header{{Name: "X-Re-Added", Value: "z"}},
			}},
		},
	})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r}, Backends: []ir.BackendGroup{g},
		Policies: []ir.Policy{p},
	}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	path := doc.Backends["kgw--httproute.shop.web_r0"].Paths[0]
	require.Equal(t, map[string]string{
		"X-Policy":         "1",    // the policy's set survives
		"X-Kept":           "back", // a set after the policy's delete wins, under the first spelling
		"X-Set":            "a,b",  // set then add joins
		"+X-Add":           "c",    // add alone appends to the request's value
		"-X-Gone":          "",     // remove
		"-X-Set-Then-Gone": "",     // a removal still applies to what the request carried
		"Host":             "web.internal",
		"X-Member":         "m", // the member's filters come after the rule's
	}, path.RequestHeaders)
	require.Equal(t, map[string]string{
		"+Vary":      "A,B",
		"-Server":    "",
		"X-Re-Added": "z", // add after remove is a set
	}, path.ResponseHeaders)
}

func TestCompileURLRewrite(t *testing.T) {
	// URL rewrites produce one rewriter per distinct instruction list, and a
	// member's full-path rewrite lands on its own catch-all path
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	g.Members[1].Filters = []ir.Filter{{
		Type:       ir.FilterURLRewrite,
		URLRewrite: &ir.URLRewriteFilter{Path: &ir.PathModifier{Type: ir.PathReplaceFull, Value: "/b"}},
	}}
	r := route("shop", "web", ir.Rule{
		Matches: []ir.Match{
			{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}},
		},
		BackendGroup: g.Name,
		Filters: []ir.Filter{{Type: ir.FilterURLRewrite, URLRewrite: &ir.URLRewriteFilter{
			Path: &ir.PathModifier{Type: ir.PathReplacePrefix, Value: ""},
		}}},
	})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r}, Backends: []ir.BackendGroup{g},
	}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	alb := doc.Backends["kgw--httproute.shop.web_r0"]
	for _, p := range alb.Paths {
		require.Equal(t, "kgw--httproute.shop.web_r0_w0", p.ReqRewriterName)
	}
	require.Equal(t, [][]string{{"path", "prefix-replace", "/api", "/"}},
		doc.RequestRewriters["kgw--httproute.shop.web_r0_w0"].Instructions,
		"an empty replacement is the root, and the exact half names the same prefix")
	require.NotContains(t, doc.RequestRewriters, "kgw--httproute.shop.web_r0_w1",
		"matches whose instructions agree share one rewriter")
	require.Empty(t, doc.Backends["kgw--httproute.shop.web_r0_b0"].Paths[0].ReqRewriterName)
	require.Equal(t, "kgw--httproute.shop.web_r0_b1_w",
		doc.Backends["kgw--httproute.shop.web_r0_b1"].Paths[0].ReqRewriterName)

	// a member's prefix replacement behind a weighted rule is refused by the
	// translators; the compiler ignores it rather than guessing a prefix
	g.Members[1].Filters[0].URLRewrite.Path.Type = ir.PathReplacePrefix
	m.Backends = []ir.BackendGroup{g}
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Empty(t, doc.Backends["kgw--httproute.shop.web_r0_b1"].Paths[0].ReqRewriterName)
}

func TestCompileBackendTLS(t *testing.T) {
	// A BackendTLSPolicy becomes the origin's TLS client settings: the SNI name
	// and, unless the system store is trusted, the CA bundle inline
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8443, 1))
	g.Members[0].Service.Scheme = ir.ProtocolHTTPS
	g.Members[0].TLS = &ir.BackendTLS{
		Hostname:       "web.internal",
		CACertificates: []string{"-----A-----", "-----B-----"},
	}
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	b := doc.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, "https://web-svc.shop.svc:8443", b.OriginURL)
	require.Equal(t, &tlsDoc{
		ServerName:              "web.internal",
		CertificateAuthorityPEM: "-----A-----\n-----B-----", ExcludeSystemRoots: true,
	}, b.TLS)

	g.Members[0].TLS = &ir.BackendTLS{Hostname: "web.internal", System: true}
	m.Backends = []ir.BackendGroup{g}
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Equal(t, &tlsDoc{ServerName: "web.internal"}, doc.Backends["kgw--httproute.shop.web_r0"].TLS)
}

func TestCompileUnknownPolicyFallsBackToDefaults(t *testing.T) {
	// A rule naming a policy the IR does not define falls back to the defaults
	// rather than failing the compile
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default"
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{route("shop", "web",
			ir.Rule{BackendGroup: g.Name, Policy: "absent"})},
		Backends: []ir.BackendGroup{g},
	}
	o, err := Compile(m, opts)
	require.NoError(t, err)
	b := decode(t, o.Data).Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, "default", b.CacheName)
	require.Equal(t, "proxycache", b.Paths[0].HandlerName)
}

func TestCompileEmitsDefaultsAccessLog(t *testing.T) {
	// A configured defaults.access_log must reach the generated backends; otherwise it is
	// silently discarded and generated routes inherit the unrelated top-level settings
	opts := serviceOpts(t)
	opts.Defaults.AccessLog = &alo.Options{
		Filename: "stdout", Format: "combined",
	}
	require.NoError(t, opts.Validate())

	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	o, err := Compile(m, opts)
	require.NoError(t, err)
	got := decode(t, o.Data)

	// it lands on the backends that actually proxy, which is where an
	// access log has a request to record
	for _, name := range []string{
		"kgw--httproute.shop.web_r0_b0", "kgw--httproute.shop.web_r0_b1",
	} {
		b := got.Backends[name]
		require.NotNil(t, b.AccessLog, name)
		require.Equal(t, "stdout", b.AccessLog.Filename, name)
		require.Equal(t, "combined", b.AccessLog.Format, name)
	}
}

func TestCompileVersionCoversOptions(t *testing.T) {
	// The overlay version gates the reload and the emitted bytes depend on the options as well
	// as the IR, so a version covering only the IR would drop a changed default as a no-op
	m := simple()
	base, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)

	cases := map[string]func(*kubecfg.Options){
		"cache name": func(o *kubecfg.Options) { o.Defaults.CacheName = "other" },
		"timeout": func(o *kubecfg.Options) {
			o.Defaults.Timeout = timeconv.Duration(time.Minute)
		},
		"access log": func(o *kubecfg.Options) {
			o.Defaults.AccessLog = &alo.Options{Filename: "stdout"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := serviceOpts(t)
			mutate(opts)
			changed, err := Compile(m, opts)
			require.NoError(t, err)
			require.NotEqual(t, string(base.Data), string(changed.Data),
				"the option should have changed the generated bytes")
			require.NotEqual(t, base.Version, changed.Version,
				"a changed option must make the overlay stale")
		})
	}

	// the same inputs still produce the same version
	again, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	require.Equal(t, base.Version, again.Version)
}

func TestCompiledRoutesAreSound(t *testing.T) {
	// For every path the compiler emits, in every shape and options combination: the handler
	// exists on the backend's provider, and no mutating method reaches the object cache
	factories := providerregistry.SupportedProviders()

	shapes := everyShape()
	configs := map[string]func(*kubecfg.Options){
		"no cache": func(*kubecfg.Options) {},
		"cache":    func(o *kubecfg.Options) { o.Defaults.CacheName = "default" },
	}

	for shapeName, model := range shapes {
		for cfgName, mutate := range configs {
			t.Run(shapeName+"/"+cfgName, func(t *testing.T) {
				opts := serviceOpts(t)
				mutate(opts)
				o, err := Compile(model, opts)
				require.NoError(t, err)
				assertGeneratedRoutesAreSound(t, factories, decode(t, o.Data).Backends)
			})
		}
	}
}

func everyShape() map[string]*ir.IR {
	// everyShape is one IR per output shape the compiler emits, so an invariant
	// asserted over it is asserted over the whole projection
	return map[string]*ir.IR{
		"single backend": simple(),
		"weighted backends": func() *ir.IR {
			g := group("shop", "web", 0,
				svcMember(0, "shop", "a", 80, 9), svcMember(1, "shop", "b", 80, 1))
			return &ir.IR{
				Listeners: []ir.Listener{httpListener()},
				Routes: []ir.Route{route("shop", "web",
					ir.Rule{BackendGroup: g.Name})},
				Backends: []ir.BackendGroup{g},
			}
		}(),
		"invalid ref": func() *ir.IR {
			g := group("shop", "web", 0,
				svcMember(0, "shop", "a", 80, 1),
				ir.BackendMember{RefIndex: 1, Weight: 1, Invalid: true})
			return &ir.IR{
				Listeners: []ir.Listener{httpListener()},
				Routes: []ir.Route{route("shop", "web",
					ir.Rule{BackendGroup: g.Name})},
				Backends: []ir.BackendGroup{g},
			}
		}(),
		"policy overrides": policyShape(),
		"weighted policy overrides": func() *ir.IR {
			m := policyShape()
			g := group("shop", "web", 0,
				svcMember(0, "shop", "a", 80, 3), svcMember(1, "shop", "b", 80, 1))
			m.Backends = []ir.BackendGroup{g}
			m.Routes[0].Rules[0].BackendGroup = g.Name
			return m
		}(),
		"redirect": func() *ir.IR {
			g := group("shop", "web", 0)
			return &ir.IR{
				Listeners: []ir.Listener{httpListener()},
				Routes: []ir.Route{route("shop", "web", ir.Rule{
					BackendGroup: g.Name,
					Filters: []ir.Filter{{
						Type:     ir.FilterRedirect,
						Redirect: &ir.RedirectFilter{Scheme: "https", StatusCode: 301},
					}},
				})},
				Backends: []ir.BackendGroup{g},
			}
		}(),
		"filters": filterShape(),
		"weighted filters": func() *ir.IR {
			m := filterShape()
			g := group("shop", "web", 0,
				svcMember(0, "shop", "a", 80, 3), svcMember(1, "shop", "b", 80, 1))
			g.Members[1].Filters = m.Backends[0].Members[0].Filters
			m.Backends = []ir.BackendGroup{g}
			m.Routes[0].Rules[0].BackendGroup = g.Name
			return m
		}(),
		"endpoint": func() *ir.IR {
			m := endpointShape()
			m.Policies[0].CacheName = "default"
			return m
		}(),
		"weighted endpoint": func() *ir.IR {
			m := endpointShape()
			m.Policies[0].HealthMode = "probe"
			g := group("shop", "web", 0,
				svcMember(0, "shop", "a", 80, 3), svcMember(1, "shop", "b", 80, 1))
			m.Backends = []ir.BackendGroup{g}
			m.Routes[0].Rules[0].BackendGroup = g.Name
			return m
		}(),
		"backend tls": func() *ir.IR {
			m := simple()
			m.Backends[0].Members[0].Service.Scheme = ir.ProtocolHTTPS
			m.Backends[0].Members[0].TLS = &ir.BackendTLS{
				Hostname:       "web.internal",
				CACertificates: []string{testCAPEM()},
			}
			return m
		}(),
		"endpoint tls": func() *ir.IR {
			m := endpointShape()
			m.Backends[0].Members[0].Service.Scheme = ir.ProtocolHTTPS
			m.Backends[0].Members[0].TLS = &ir.BackendTLS{Hostname: "web.internal", System: true}
			return m
		}(),
	}
}

func filterShape() *ir.IR {
	// filterShape exercises every filter type a forwarding rule and its member
	// may carry
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	g.Members[0].Filters = []ir.Filter{
		{Type: ir.FilterRequestHeaders, RequestHeaders: &ir.HeaderFilter{
			Set: []ir.Header{{Name: "X-Member", Value: "1"}},
		}},
		{Type: ir.FilterURLRewrite, URLRewrite: &ir.URLRewriteFilter{
			Path: &ir.PathModifier{Type: ir.PathReplaceFull, Value: "/member"},
		}},
	}
	r := route("shop", "web", ir.Rule{
		Matches: []ir.Match{
			{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}},
		},
		BackendGroup: g.Name,
		Filters: []ir.Filter{
			{Type: ir.FilterRequestHeaders, RequestHeaders: &ir.HeaderFilter{
				Set: []ir.Header{{Name: "X-A", Value: "1"}}, Add: []ir.Header{{Name: "X-B", Value: "2"}},
				Remove: []string{"X-C"},
			}},
			{Type: ir.FilterResponseHeaders, ResponseHeaders: &ir.HeaderFilter{
				Add: []ir.Header{{Name: "Vary", Value: "X-A"}},
			}},
			{Type: ir.FilterURLRewrite, URLRewrite: &ir.URLRewriteFilter{Hostname: "web.internal"}},
		},
	})
	return &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r},
		Backends:  []ir.BackendGroup{g},
	}
}

func testCAPEM() string {
	// testCAPEM is a certificate the loader accepts as a CA bundle
	_, cert, err := tlstest.GetTestKeyAndCert(true)
	if err != nil {
		panic(err)
	}
	return string(cert)
}

func policyShape() *ir.IR {
	// policyShape exercises every field a Policy can carry, so the objects a
	// policy generates (rewriters, negative caches) are covered too
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	p := ir.Policy{
		Name:                "p",
		Source:              src("Ingress", "shop", "web"),
		Handler:             ir.HandlerProxyCache,
		CacheName:           "default",
		TimeoutMS:           15000,
		MaxTTLMS:            600000,
		NegativeCacheName:   "api-errors",
		RequestHeaders:      map[string]string{"X-Set": "1", "-X-Gone": ""},
		ResponseHeaders:     map[string]string{"+Vary": "Accept-Encoding"},
		CORSMode:            "merge",
		CORSHeaders:         map[string]string{"Access-Control-Allow-Origin": "*"},
		CollapsedForwarding: "progressive",
		RewriteTarget:       "/v2",
	}
	r := route("shop", "web", ir.Rule{
		Matches: []ir.Match{
			{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}},
			{Path: ir.PathMatch{Type: ir.PathRegex, Value: "^/x/(.*)$"}},
		},
		BackendGroup: g.Name,
		Policy:       p.Name,
	})
	return &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r},
		Backends:  []ir.BackendGroup{g},
		Policies:  []ir.Policy{p},
	}
}

func TestCompiledConfigurationLoads(t *testing.T) {
	// Generated configuration is loaded by the code that loads a file, so every shape has to
	// survive it; one that validates only as a decoded struct would fail the reload
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(loadableBaseConfig), 0o600))
	for name, model := range everyShape() {
		t.Run(name, func(t *testing.T) {
			o, err := Compile(model, serviceOpts(t))
			require.NoError(t, err)
			conf, err := config.LoadWithOverlay([]string{"-config", path}, o)
			require.NoError(t, err)
			require.NoError(t, conf.Backends.Validate())
			require.NoError(t, validate.Validate(conf))
		})
	}
}

// loadableBaseConfig is the minimal file configuration a generated overlay
// is merged onto
const loadableBaseConfig = `
backends:
  default:
    provider: rp
    origin_url: http://example.com
negative_caches:
  api-errors:
    404: 30s
`

func assertGeneratedRoutesAreSound(t *testing.T, factories rt.Lookup,
	generated map[string]*bo.Options,
) {
	// assertGeneratedRoutesAreSound builds the real client for each generated backend and checks
	// every path's handler exists on its provider and no mutating method reaches the cache
	t.Helper()
	require.NotEmpty(t, generated)
	for name, b := range generated {
		require.NoError(t, b.Initialize(name))
		if b.IsTemplate {
			// a template's paths are what its discovered clones serve, so
			// the invariants are checked on a clone
			clone, err := template.Instantiate(name, b, discovery.Member{
				Name: "pod", Scheme: "http", Address: "127.0.0.1:1",
			})
			require.NoError(t, err, "template %q", name)
			b = clone
		}
		factory, ok := factories[b.Provider]
		require.True(t, ok, "backend %q names unknown provider %q", name, b.Provider)

		client, err := factory(name, b, lm.NewRouter(), nil, nil, factories)
		require.NoError(t, err, "backend %q", name)
		available := client.Handlers()

		// generated backends suppress the provider defaults, so the check
		// must resolve paths the way route registration does
		paths := b.Paths
		if !b.PathDefaultsDisabled {
			paths = client.DefaultPathConfigs(b).Overlay(b.Paths)
		}
		require.NoError(t, paths.Initialize())
		require.NotEmpty(t, paths, "backend %q has no paths at all", name)
		for _, p := range paths {
			h, ok := available[p.HandlerName]
			require.True(t, ok,
				"backend %q (provider %q) path %q names handler %q, which the "+
					"provider does not register; the path would be dropped",
				name, b.Provider, p.Path, p.HandlerName)
			require.NotNil(t, h)

			if p.HandlerName != "proxycache" {
				continue
			}
			// the object cache handler does not reject a non-cacheable method itself: it would
			// store a cacheable response to the mutating request and serve it later
			for _, m := range p.Methods {
				require.True(t, methods.IsCacheable(m),
					"backend %q path %q routes %s through proxycache",
					name, p.Path, m)
			}
		}
	}
}

func TestCompileUsesACacheCapableProviderWhenCaching(t *testing.T) {
	// A configured cache must make the generated backends cache-capable; the reverse-proxy
	// provider has no proxycache handler, so emitting it there made the cache unreachable
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default"

	b := emitted(t, simple(), opts)["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.ReverseProxyCacheShort, b.Provider)
	require.Equal(t, "default", b.CacheName)
	require.Equal(t, "proxycache", b.Paths[0].Handler)

	// with no cache configured a plain reverse proxy is correct, and
	// carries no cache name it would ignore
	pb := emitted(t, simple(), serviceOpts(t))["kgw--httproute.shop.web_r0"]
	require.Equal(t, providers.ReverseProxyShort, pb.Provider)
	require.Empty(t, pb.CacheName)
	require.Equal(t, "proxy", pb.Paths[0].Handler)
}

func TestCompileMembersCarryTheEffectiveHandler(t *testing.T) {
	// Weighted members are dispatched to through their own router, so each needs a catch-all
	// path carrying the effective handler, or the configured cache never applies to them
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default"

	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 9),
		svcMember(1, "shop", "b", 80, 1),
		ir.BackendMember{RefIndex: 2, Weight: 1, Invalid: true})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	got := emitted(t, m, opts)
	for _, name := range []string{
		"kgw--httproute.shop.web_r0_b0", "kgw--httproute.shop.web_r0_b1",
	} {
		b := got[name]
		require.Equal(t, providers.ReverseProxyCacheShort, b.Provider, name)
		require.Equal(t, "default", b.CacheName, name)
		// a caching member is split along the provider's own method
		// boundary: the cache sees only GET and HEAD
		require.Len(t, b.Paths, 2, name)
		byHandler := map[string][]string{}
		for _, p := range b.Paths {
			require.Equal(t, "/", p.Path, name)
			byHandler[p.Handler] = p.Methods
		}
		require.Equal(t, methods.CacheableHTTPMethods(),
			byHandler["proxycache"], name)
		require.Equal(t, methods.UncacheableHTTPMethods(),
			byHandler["proxy"], name)
	}
	// the invalid member keeps its localresponse path and gains no other
	inv := got["kgw--httproute.shop.web_r0_b2"]
	require.Len(t, inv.Paths, 1)
	require.Equal(t, "localresponse", inv.Paths[0].Handler)
	require.Equal(t, providers.ReverseProxyShort, inv.Provider,
		"a fixed responder never reaches an origin, so it needs no cache")
}

func TestCompilePolicyHandlerReachesMembers(t *testing.T) {
	// A policy-selected handler must reach weighted members, not just the
	// single-backend shape
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default" // members would otherwise cache

	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	r := route("shop", "web", ir.Rule{BackendGroup: g.Name, Policy: "p1"})
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r},
		Backends:  []ir.BackendGroup{g},
		Policies:  []ir.Policy{{Name: "p1", Handler: "proxy"}},
	}
	got := emitted(t, m, opts)
	require.Equal(t, providers.ALB, got["kgw--httproute.shop.web_r0"].Provider)
	for _, name := range []string{
		"kgw--httproute.shop.web_r0_b0", "kgw--httproute.shop.web_r0_b1",
	} {
		b := got[name]
		require.Equal(t, "proxy", b.Paths[0].Handler, name)
		require.Equal(t, providers.ReverseProxyShort, b.Provider,
			"a policy opting out of caching must not leave a cache provider")
		require.Empty(t, b.CacheName, name)
	}
}

func TestCompiledPathsAcceptEveryMethodByDefault(t *testing.T) {
	// An empty method list means GET alone once path options are initialized, so a generated
	// route that names no method must say "any" explicitly or refuse every other verb
	o, err := Compile(simple(), serviceOpts(t))
	require.NoError(t, err)
	b := decode(t, o.Data).Backends["kgw--httproute.shop.web_r0"]
	require.NoError(t, b.Paths.Initialize())
	require.Len(t, b.Paths, 1, "no cache means no method split")
	require.Subset(t, b.Paths[0].Methods,
		[]string{http.MethodGet, http.MethodPost, http.MethodDelete},
		"a match naming no method must accept them all")

	// a match that does name methods keeps exactly those
	g := group("shop", "web", 0, svcMember(0, "shop", "s", 80, 1))
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{route("shop", "web", ir.Rule{
			Matches: []ir.Match{{
				Path:    ir.PathMatch{Type: ir.PathPrefix, Value: "/"},
				Methods: []string{http.MethodGet},
			}},
			BackendGroup: g.Name,
		})},
		Backends: []ir.BackendGroup{g},
	}
	o2, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	b2 := decode(t, o2.Data).Backends["kgw--httproute.shop.web_r0"]
	require.NoError(t, b2.Paths.Initialize())
	require.Equal(t, []string{http.MethodGet}, b2.Paths[0].Methods)
}

func TestCompileKeepsMutatingMethodsOutOfTheCache(t *testing.T) {
	// The object cache handler does not reject a non-cacheable method, so a mutating request
	// routed into it would be stored; cache-derived routes split along the method boundary
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "default"

	t.Run("unconstrained match", func(t *testing.T) {
		b := emitted(t, simple(), opts)["kgw--httproute.shop.web_r0"]
		require.Len(t, b.Paths, 2)
		byHandler := map[string]*pathDoc{}
		for _, p := range b.Paths {
			require.Equal(t, "/api", p.Path, "both halves keep the match path")
			byHandler[p.Handler] = p
		}
		require.Equal(t, methods.CacheableHTTPMethods(),
			byHandler["proxycache"].Methods)
		require.Equal(t, methods.UncacheableHTTPMethods(),
			byHandler["proxy"].Methods)

		// together they still cover every method the wildcard would have
		var all []string
		all = append(all, byHandler["proxycache"].Methods...)
		all = append(all, byHandler["proxy"].Methods...)
		require.ElementsMatch(t, append(methods.AllHTTPMethods(), methods.Wildcard), all,
			"the split must not narrow what the route accepts")
	})

	t.Run("explicit non-cacheable method", func(t *testing.T) {
		g := group("shop", "web", 0, svcMember(0, "shop", "s", 80, 1))
		m := &ir.IR{
			Listeners: []ir.Listener{httpListener()},
			Routes: []ir.Route{route("shop", "web", ir.Rule{
				Matches: []ir.Match{{
					Path:    ir.PathMatch{Type: ir.PathPrefix, Value: "/submit"},
					Methods: []string{http.MethodPost},
				}},
				BackendGroup: g.Name,
			})},
			Backends: []ir.BackendGroup{g},
		}
		b := emitted(t, m, opts)["kgw--httproute.shop.web_r0"]
		require.Len(t, b.Paths, 1,
			"a POST-only match has no cacheable half to emit")
		require.Equal(t, "proxy", b.Paths[0].Handler)
		require.Equal(t, []string{http.MethodPost}, b.Paths[0].Methods)
	})

	t.Run("explicit cacheable method", func(t *testing.T) {
		g := group("shop", "web", 0, svcMember(0, "shop", "s", 80, 1))
		m := &ir.IR{
			Listeners: []ir.Listener{httpListener()},
			Routes: []ir.Route{route("shop", "web", ir.Rule{
				Matches: []ir.Match{{
					Path:    ir.PathMatch{Type: ir.PathPrefix, Value: "/read"},
					Methods: []string{http.MethodGet},
				}},
				BackendGroup: g.Name,
			})},
			Backends: []ir.BackendGroup{g},
		}
		b := emitted(t, m, opts)["kgw--httproute.shop.web_r0"]
		require.Len(t, b.Paths, 1)
		require.Equal(t, "proxycache", b.Paths[0].Handler)
		require.Equal(t, []string{http.MethodGet}, b.Paths[0].Methods)
	})

	t.Run("mixed methods", func(t *testing.T) {
		g := group("shop", "web", 0, svcMember(0, "shop", "s", 80, 1))
		m := &ir.IR{
			Listeners: []ir.Listener{httpListener()},
			Routes: []ir.Route{route("shop", "web", ir.Rule{
				Matches: []ir.Match{{
					Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/mixed"},
					Methods: []string{
						http.MethodGet, http.MethodPost, http.MethodHead,
					},
				}},
				BackendGroup: g.Name,
			})},
			Backends: []ir.BackendGroup{g},
		}
		b := emitted(t, m, opts)["kgw--httproute.shop.web_r0"]
		require.Len(t, b.Paths, 2)
		byHandler := map[string][]string{}
		for _, p := range b.Paths {
			byHandler[p.Handler] = p.Methods
		}
		require.Equal(t, []string{http.MethodGet, http.MethodHead},
			byHandler["proxycache"])
		require.Equal(t, []string{http.MethodPost}, byHandler["proxy"])
	})

	// a non-caching route is not split at all
	t.Run("no cache configured", func(t *testing.T) {
		b := emitted(t, simple(), serviceOpts(t))["kgw--httproute.shop.web_r0"]
		require.Len(t, b.Paths, 1)
		require.Equal(t, "proxy", b.Paths[0].Handler)
		require.Equal(t, anyMethod, b.Paths[0].Methods)
	})
}

const (
	boundaryHost      = "shop.example.com"
	boundaryOtherHost = "other.example.com"
	boundaryPath      = "/api"
	boundaryOffPath   = "/admin"
)

func hostless() *ir.IR {
	// hostless is the simple fixture with its hostname constraint removed, the
	// shape of an Ingress rule with no host or a route on an open listener
	m := simple()
	m.Routes[0].Hostnames = nil
	return m
}

func weighted(hostnames ...string) *ir.IR {
	// weighted is two backendRefs behind one rule, which compiles to an ALB
	g := group("shop", "web", 0,
		svcMember(0, "shop", "a", 80, 9), svcMember(1, "shop", "b", 80, 1))
	r := route("shop", "web", ir.Rule{
		Matches: []ir.Match{{
			Path: ir.PathMatch{Type: ir.PathPrefix, Value: boundaryPath},
		}},
		BackendGroup: g.Name,
	})
	r.Hostnames = hostnames
	return &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{r},
		Backends:  []ir.BackendGroup{g},
	}
}

func TestCompileHostConstraintDrivesRegistrationMode(t *testing.T) {
	// A route that constrains no hostname must still install a frontend route, and only the
	// route-facing backend may claim every hostname; a pool member is reachable via its ALB alone
	tests := []struct {
		name      string
		model     *ir.IR
		anyHost   bool
		routeName string
	}{
		{"hosted", simple(), false, RuleName(src("HTTPRoute", "shop", "web"), 0)},
		{"hostless", hostless(), true, RuleName(src("HTTPRoute", "shop", "web"), 0)},
		{"hostless weighted", weighted(), true, RuleName(src("HTTPRoute", "shop", "web"), 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := emitted(t, test.model, serviceOpts(t))
			require.NotEmpty(t, got)
			for name, b := range got {
				require.True(t, b.PathDefaultsDisabled,
					"backend %q must not inherit its provider's catch-all paths", name)
				if name == test.routeName {
					require.Equal(t, test.anyHost, b.AnyHostRouting, "backend %q", name)
					continue
				}
				require.False(t, b.AnyHostRouting,
					"pool member %q is reached through its ALB and must claim no hostname", name)
			}
		})
	}
}

func serveStatus(rtr router.Router, host, path string) int {
	// serveStatus registers the compiled configuration the way the daemon does
	// and reports the status of one request against the resulting router
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	w := httptest.NewRecorder()
	rtr.ServeHTTP(w, req)
	return w.Code
}

func registerGenerated(t *testing.T, data []byte, origin string) router.Router {
	// registerGenerated loads compiled output as real configuration, points its
	// backends at origin, builds their clients and registers their routes
	t.Helper()
	conf := config.NewConfig()
	// a backend named "default" would be promoted to is_default and register
	// root routes that mask what these assertions cover
	delete(conf.Backends, "default")
	factories := providerregistry.SupportedProviders()
	clients := backends.Backends{}
	generated := decode(t, data).Backends
	require.NotEmpty(t, generated)
	for name, b := range generated {
		// the compiled origin is an in-cluster Service address, so the
		// routing assertions need a reachable one in its place
		if b.OriginURL != "" {
			b.OriginURL = origin
		}
		require.NoError(t, b.Initialize(name))
		// the loader initializes paths after backends, which compiles their conditions
		require.NoError(t, b.Paths.Initialize())
		conf.Backends[name] = b
	}
	for name, b := range conf.Backends {
		client, err := factories[b.Provider](name, b, lm.NewRouter(), nil, clients, factories)
		require.NoError(t, err, "backend %q", name)
		clients[name] = client
		b.HTTPClient = client.HTTPClient()
	}
	caches := cacheregistry.LoadCachesFromConfig(conf)
	t.Cleanup(func() { cacheregistry.CloseCaches(caches) })
	rtr := lm.NewRouter()
	require.NoError(t, routing.RegisterProxyRoutes(conf, clients, rtr, nil, caches, nil, false))
	return rtr
}

func TestGeneratedRoutesServeOnlyTheirKubernetesPaths(t *testing.T) {
	// The compiled configuration is correct only if the running router serves exactly the
	// Kubernetes route, which the emitted YAML alone cannot show
	origin := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer origin.Close()

	tests := []struct {
		name     string
		model    *ir.IR
		served   []string
		unserved string
	}{
		{"hosted", simple(), []string{boundaryHost}, boundaryOtherHost},
		{"hostless", hostless(), []string{boundaryHost, boundaryOtherHost}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o, err := Compile(test.model, serviceOpts(t))
			require.NoError(t, err)
			rtr := registerGenerated(t, o.Data, origin.URL)
			for _, host := range test.served {
				require.Equal(t, http.StatusOK, serveStatus(rtr, host, boundaryPath),
					"host %q must reach the generated backend", host)
				require.Equal(t, http.StatusNotFound, serveStatus(rtr, host, boundaryOffPath),
					"host %q must not answer a path the route never attached", host)
			}
			if test.unserved != "" {
				require.Equal(t, http.StatusNotFound, serveStatus(rtr, test.unserved, boundaryPath),
					"a host the route does not name must not be served")
			}
		})
	}
}

func TestCompileExternalListeners(t *testing.T) {
	// A listener the operator configured is not the controller's to define: routes attach to it
	// by name and nothing is emitted for it, while a Gateway listener is minted
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8080, 1))
	r := route("shop", "web", ir.Rule{BackendGroup: g.Name})
	r.Listeners = []string{"websecure", "l80"}
	m := &ir.IR{
		Listeners: []ir.Listener{
			httpListener(),
			{Name: "websecure", External: true, Source: src("Controller", "", "ingress")},
		},
		Routes:   []ir.Route{r},
		Backends: []ir.BackendGroup{g},
	}
	o, err := Compile(m, serviceOpts(t))
	require.NoError(t, err)
	got := decode(t, o.Data)

	require.Len(t, got.Listeners, 1)
	require.Contains(t, got.Listeners, ListenerName(80, ir.ProtocolHTTP),
		"a Gateway listener is still generated")
	require.NotContains(t, got.Listeners, "websecure",
		"a configured listener must not be redefined by the controller")

	b := got.Backends["kgw--httproute.shop.web_r0"]
	require.NotNil(t, b)
	require.Equal(t, []string{ListenerName(80, ir.ProtocolHTTP), "websecure"},
		b.ListenerNames,
		"the route attaches to both by the names the router knows them by")
}

func TestCompiledListenerName(t *testing.T) {
	// CompiledListenerName is what resolves an IR listener to the name the
	// routing configuration uses
	require.Equal(t, "web", CompiledListenerName(
		ir.Listener{Name: "web", External: true}))
	require.Equal(t, ListenerName(443, ir.ProtocolHTTPS), CompiledListenerName(
		ir.Listener{Name: "l443", Port: 443, Protocol: ir.ProtocolHTTPS}))
}

func TestCompileOperatorNameReferences(t *testing.T) {
	// The names the operator configured reach every generated backend, and the
	// controller defines none of the objects behind them
	opts := serviceOpts(t)
	opts.Defaults.CacheName = "objects"
	opts.Defaults.NegativeCacheName = "api-errors"
	opts.Defaults.TracingName = "otlp"
	opts.Defaults.ReqRewriterName = "strip-internal"
	opts.Defaults.AuthenticatorName = "gateway-auth"

	b := emitted(t, simple(), opts)["kgw--httproute.shop.web_r0"]
	require.NotNil(t, b)
	require.Equal(t, "objects", b.CacheName)
	require.Equal(t, "api-errors", b.NegativeCacheName)
	require.Equal(t, "otlp", b.TracingConfigName)
	require.Equal(t, "strip-internal", b.ReqRewriterName)
	require.Equal(t, "gateway-auth", b.AuthenticatorName)

	// they are references, not definitions: the controller emits none of
	// the objects behind them
	o, err := Compile(simple(), opts)
	require.NoError(t, err)
	require.NotContains(t, string(o.Data), "negative_caches:")

	// a negative cache is emitted only where the backend caches, for the
	// same reason cache_name is: a non-caching provider ignores it
	plain := serviceOpts(t)
	plain.Defaults.NegativeCacheName = "api-errors"
	for name, b := range emitted(t, simple(), plain) {
		require.Empty(t, b.NegativeCacheName,
			"backend %q does not cache, so a negative cache would do nothing",
			name)
	}
}

func TestCompileRedirectCarriesOperatorControls(t *testing.T) {
	// A redirecting rule is still a backend the operator's controls apply to: the
	// authenticator, rewriter, tracer and access log reach it from the defaults and a policy
	g := group("shop", "web", 0)
	redirect := ir.Filter{Type: ir.FilterRedirect, Redirect: &ir.RedirectFilter{Scheme: "https"}}
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{route("shop", "web", ir.Rule{
			BackendGroup: g.Name, Filters: []ir.Filter{redirect},
		})},
		Backends: []ir.BackendGroup{g},
	}
	opts := serviceOpts(t)
	opts.Defaults.AuthenticatorName = "gateway-auth"
	opts.Defaults.ReqRewriterName = "strip"
	opts.Defaults.TracingName = "otlp"
	opts.Defaults.AccessLog = &alo.Options{Filename: "stdout", Format: "combined"}
	doc, err := buildDocument(m, opts, nil)
	require.NoError(t, err)
	b := doc.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, "gateway-auth", b.AuthenticatorName)
	require.Equal(t, "strip", b.ReqRewriterName)
	require.Equal(t, "otlp", b.TracingConfigName)
	require.Equal(t, "stdout", b.AccessLog.Filename)
	require.Equal(t, map[string]string{
		"route_kind": "HTTPRoute", "route_namespace": "shop",
		"route_name": "web",
	}, b.AccessLog.Extra, "the access log names the object served")
	require.Nil(t, opts.Defaults.AccessLog.Extra, "the operator's settings are not modified")
	require.Empty(t, b.CacheName, "a redirect caches nothing")

	m.Policies = []ir.Policy{{
		Name: "p", AuthenticatorName: "class-auth",
		ResponseHeaders: map[string]string{"X-Policy": "1"},
	}}
	m.Routes[0].Rules[0].Policy = "p"
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	b = doc.Backends["kgw--httproute.shop.web_r0"]
	require.Equal(t, "class-auth", b.AuthenticatorName,
		"a GatewayClass's authenticator reaches a redirecting rule")
	for _, p := range b.Paths {
		require.Equal(t, map[string]string{"X-Policy": "1"}, p.ResponseHeaders,
			"policy response headers reach the redirection")
	}
}

func TestCompileRedirectCarriesResponseHeaders(t *testing.T) {
	// A redirect's response-header filters modify the redirection itself
	g := group("shop", "web", 0)
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes: []ir.Route{route("shop", "web", ir.Rule{
			BackendGroup: g.Name,
			Filters: []ir.Filter{
				{Type: ir.FilterRedirect, Redirect: &ir.RedirectFilter{Scheme: "https", StatusCode: 301}},
				{Type: ir.FilterResponseHeaders, ResponseHeaders: &ir.HeaderFilter{
					Set:    []ir.Header{{Name: "Cache-Control", Value: "no-store"}},
					Remove: []string{"Server"},
				}},
			},
		})},
		Backends: []ir.BackendGroup{g},
	}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	b := doc.Backends["kgw--httproute.shop.web_r0"]
	require.NotEmpty(t, b.Paths)
	for _, p := range b.Paths {
		require.Equal(t, handlerRedirect, p.Handler)
		require.Equal(t, 301, p.ResponseCode)
		require.Equal(t, map[string]string{"Cache-Control": "no-store", "-Server": ""},
			p.ResponseHeaders)
	}
}

func TestCompileRedirectUsesTheListenerPort(t *testing.T) {
	// A redirect naming neither scheme nor port sends the client back to the listener's port,
	// which the compiler knows; an explicit scheme or port leaves the port to the handler
	build := func(t *testing.T, listeners []ir.Listener, attach []string,
		red *ir.RedirectFilter,
	) *document {
		t.Helper()
		g := group("shop", "web", 0)
		r := route("shop", "web", ir.Rule{
			BackendGroup: g.Name,
			Filters:      []ir.Filter{{Type: ir.FilterRedirect, Redirect: red}},
		})
		r.Listeners = attach
		doc, err := buildDocument(&ir.IR{
			Listeners: listeners, Routes: []ir.Route{r}, Backends: []ir.BackendGroup{g},
		}, serviceOpts(t), nil)
		require.NoError(t, err)
		return doc
	}
	alt := ir.Listener{
		Name: "l8080", Port: 8080, Protocol: ir.ProtocolHTTP,
		Source: src("Gateway", "infra", "gw"),
	}
	instructions := func(doc *document) [][]string {
		rw := doc.RequestRewriters["kgw--httproute.shop.web_r0_w0"]
		if rw == nil {
			return nil
		}
		return rw.Instructions
	}

	doc := build(t, []ir.Listener{alt}, []string{"l8080"},
		&ir.RedirectFilter{Hostname: "new.example.com"})
	require.Equal(t, [][]string{{"hostname", "set", "new.example.com"}, {"port", "set", "8080"}},
		instructions(doc))

	doc = build(t, []ir.Listener{httpListener()}, []string{"l80"},
		&ir.RedirectFilter{Hostname: "new.example.com"})
	require.Equal(t, [][]string{{"hostname", "set", "new.example.com"}, {"port", "set", "80"}},
		instructions(doc), "the well-known port is the handler's to omit")

	doc = build(t, []ir.Listener{alt}, []string{"l8080"},
		&ir.RedirectFilter{Scheme: "https"})
	require.Equal(t, [][]string{{"scheme", "set", "https"}}, instructions(doc),
		"an explicit scheme selects its well-known port")

	doc = build(t, []ir.Listener{alt}, []string{"l8080"},
		&ir.RedirectFilter{Port: 9443})
	require.Equal(t, [][]string{{"port", "set", "9443"}}, instructions(doc))

	// two ports on one route: the compiler cannot name one, so the handler
	// falls back to the request; translators emit one route per port
	doc = build(t, []ir.Listener{httpListener(), alt}, []string{"l80", "l8080"},
		&ir.RedirectFilter{Hostname: "new.example.com"})
	require.Equal(t, [][]string{{"hostname", "set", "new.example.com"}}, instructions(doc))

	// an operator-configured listener's port is not the IR's to know
	external := ir.Listener{Name: "web", External: true, Source: src("Controller", "", "ingress")}
	doc = build(t, []ir.Listener{external}, []string{"web"},
		&ir.RedirectFilter{Hostname: "new.example.com"})
	require.Equal(t, [][]string{{"hostname", "set", "new.example.com"}}, instructions(doc))
}

func TestCompileBackendTLSExcludesSystemRoots(t *testing.T) {
	// A policy's CA bundle is the whole of the trust for its backend; only a
	// policy that chose the well-known authorities trusts the system roots
	g := group("shop", "web", 0, svcMember(0, "shop", "web-svc", 8443, 1))
	g.Members[0].Service.Scheme = ir.ProtocolHTTPS
	g.Members[0].TLS = &ir.BackendTLS{Hostname: "web.internal", CACertificates: []string{"-----A-----"}}
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
		Backends:  []ir.BackendGroup{g},
	}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.True(t, doc.Backends["kgw--httproute.shop.web_r0"].TLS.ExcludeSystemRoots)

	g.Members[0].TLS = &ir.BackendTLS{Hostname: "web.internal", System: true}
	m.Backends = []ir.BackendGroup{g}
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.False(t, doc.Backends["kgw--httproute.shop.web_r0"].TLS.ExcludeSystemRoots)
}

func TestManifestNamesEveryGeneratedBackend(t *testing.T) {
	// The manifest joins every generated backend to the object it serves, and only those
	web := ir.Source{Kind: ir.KindHTTPRoute, Namespace: "shop", Name: "web"}
	api := ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "api.v1"}
	model := &ir.IR{
		Listeners: []ir.Listener{{
			Name: "l", Port: 80, Protocol: ir.ProtocolHTTP,
			Source: ir.Source{Kind: ir.KindGateway, Namespace: "infra", Name: "gw"},
		}},
		Routes: []ir.Route{
			{Name: "web", Source: web, Listeners: []string{"l"}, Rules: []ir.Rule{{
				Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/"}}},
				BackendGroup: "web|r0",
			}}},
			{Name: "api", Source: api, Listeners: []string{"l"}, Rules: []ir.Rule{{
				Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/api"}}},
				BackendGroup: "api|r0",
			}}},
		},
		Backends: []ir.BackendGroup{
			{Name: "web|r0", Source: web, Members: []ir.BackendMember{
				{Weight: 1, Service: ir.ServiceTarget{Namespace: "shop", Name: "a", Port: 80}},
				{RefIndex: 1, Weight: 1, Invalid: true, InvalidReason: "gone"},
			}},
			{Name: "api|r0", Source: api, Members: []ir.BackendMember{
				{Weight: 1, Service: ir.ServiceTarget{Namespace: "shop", Name: "b", Port: 80}},
			}},
		},
	}
	_, manifest, err := CompileWithManifest(model, serviceOpts(t))
	require.NoError(t, err)
	require.Len(t, manifest, 2)
	require.Equal(t, web, manifest[0].Source, "ordered by source")
	require.Equal(t, []string{
		"kgw--httproute.shop.web_r0", "kgw--httproute.shop.web_r0_b0",
		"kgw--httproute.shop.web_r0_b1",
	}, manifest[0].Backends)
	require.Equal(t, api, manifest[1].Source)
	require.Equal(t, []string{"kgw--ingress.shop.api.v1_r0"}, manifest[1].Backends)

	_, manifest, err = CompileWithManifest(nil, serviceOpts(t))
	require.NoError(t, err)
	require.Nil(t, manifest)
	_, _, err = CompileWithManifest(model, nil)
	require.ErrorIs(t, err, ErrNoOptions)
}

// routeBackends is every compiled backend except the placeholders binding routeless listeners
func routeBackends(doc generatedConfig) map[string]*bo.Options {
	out := make(map[string]*bo.Options)
	for name, b := range doc.Backends {
		if !strings.HasSuffix(name, placeholderSuffix) {
			out[name] = b
		}
	}
	return out
}
