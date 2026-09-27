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
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

const (
	stickyOuter   = "kgw--httproute.shop.web_r0"
	stickyInner0  = "kgw--httproute.shop.web_r0_b0"
	stickyInner1  = "kgw--httproute.shop.web_r0_b1"
	stickyStream  = "kgw--tcproute.data.db_r0"
	stickyStream0 = "kgw--tcproute.data.db_r0_b0"
	hostCookie    = "__Host-s"
	tenantKey     = "header:X-Tenant"
	hourMS        = 3600000
	// classKey is a base64 key as a class's sticky_secret Secret yields it
	classKey = "a2V5LWtleS1rZXkta2V5LWtleS1rZXkta2V5LWtleS1rZXk="
)

// weightedEndpointShape is a rule over two Services whose endpoints are discovered
func weightedEndpointShape() *ir.IR {
	m := endpointShape()
	g := group("shop", "web", 0, svcMember(0, "shop", "a", 80, 3), svcMember(1, "shop", "b", 80, 1))
	m.Backends = []ir.BackendGroup{g}
	m.Routes[0].Rules[0].BackendGroup = g.Name
	return m
}

// stickyShapes are the shapes whose ALBs keep sessions, each loaded as configuration
func stickyShapes() map[string]*ir.IR {
	return map[string]*ir.IR{
		"sticky rule": func() *ir.IR {
			m := weightedEndpointShape()
			m.Routes[0].Rules[0].Session = &ir.Session{
				Type: ir.SessionCookie, Name: hostCookie, Permanent: true, AbsoluteMS: hourMS,
			}
			m.Policies[0].Sticky = so.ModeCookie
			return m
		}(),
		"sticky members": func() *ir.IR {
			m := weightedEndpointShape()
			m.Backends[0].Members[0].Session = &ir.Session{Type: ir.SessionHeader, Name: "X-Session"}
			m.Policies[0].Sticky, m.Policies[0].StickyKey = so.ModeTable, tenantKey
			m.Policies[0].StickyTTLMS, m.Policies[0].StickyIdleMS = hourMS, 60000
			m.Policies[0].StickySecret = classKey
			return m
		}(),
		"sticky service hosts": serviceSessionHosts("b.example.com"),
		"sticky hosts": func() *ir.IR {
			// one rule served under two hosts sets one named cookie on one listener
			var routes []ir.Route
			var groups []ir.BackendGroup
			for i, host := range []string{"a.example.com", "b.example.com"} {
				g := group("shop", "web", i, svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
				g.Name += host
				r := route("shop", "web", ir.Rule{
					BackendGroup: g.Name,
					Session:      &ir.Session{Type: ir.SessionCookie, Name: "shared"},
				})
				r.Name += host
				r.Hostnames = []string{host}
				routes, groups = append(routes, r), append(groups, g)
			}
			return model(routes, groups...)
		}(),
	}
}

// serviceSessionHosts is two weighted endpoint routes, on a.example.com and the host given and each
// on its own path, whose first Service asks for one named cookie; each per-Service ALB registers
// no route of its own
func serviceSessionHosts(second string) *ir.IR {
	m := &ir.IR{
		Listeners: []ir.Listener{httpListener()},
		Policies:  []ir.Policy{{Name: "p1", RoutingMode: kubecfg.RoutingModeEndpoint}},
	}
	for i, host := range []string{"a.example.com", second} {
		g := group("shop", "web", i, svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
		g.Name += strconv.Itoa(i)
		g.Members[0].Session = &ir.Session{Type: ir.SessionCookie, Name: "carts"}
		// a path of its own, so on a shared host both routes are served rather than one winning
		r := route("shop", "web", ir.Rule{BackendGroup: g.Name, Policy: "p1", Matches: []ir.Match{
			{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/r" + strconv.Itoa(i)}},
		}})
		r.Name += host
		r.Hostnames = []string{host}
		m.Routes, m.Backends = append(m.Routes, r), append(m.Backends, g)
	}
	return m
}

func TestCompileSessionNamedOncePerHost(t *testing.T) {
	// one rule under two hosts is two ALBs setting one named cookie, each for its own host
	docs := emitted(t, stickyShapes()["sticky hosts"], serviceOpts(t))
	for i, host := range []string{"a.example.com", "b.example.com"} {
		alb := docs["kgw--httproute.shop.web_r"+strconv.Itoa(i)]
		require.Equal(t, []string{host}, alb.Hosts)
		require.Equal(t, "shared", alb.ALB.Sticky.Cookie.Name)
	}
}

func TestCompiledServiceSessionsFollowTheirRoutesHosts(t *testing.T) {
	// the per-Service ALBs carry no hosts, so validation reads theirs from the ALB that dispatches
	// into each: disjoint routes load, and routes sharing a host are refused
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(loadableBaseConfig), 0o600))
	load := func(m *ir.IR) error {
		o, err := Compile(m, serviceOpts(t))
		require.NoError(t, err)
		conf, err := config.LoadWithOverlay([]string{"-config", path}, o)
		require.NoError(t, err)
		if err := validate.Validate(conf); err != nil {
			return err
		}
		require.NoError(t, conf.Process())
		return validate.RoutesRulesAndPools(conf, make(backends.Backends, len(conf.Backends)))
	}
	disjoint := serviceSessionHosts("b.example.com")
	for _, name := range []string{stickyInner0, "kgw--httproute.shop.web_r1_b0"} {
		inner := emitted(t, disjoint, serviceOpts(t))[name]
		require.Empty(t, inner.Hosts)
		require.Equal(t, "carts", inner.ALB.Sticky.Cookie.Name)
	}
	require.NoError(t, load(disjoint))
	require.ErrorContains(t, load(serviceSessionHosts("a.example.com")),
		`both set sticky cookie "carts" for a host they both serve`)
}

func TestCompileSessionOnTheRuleALB(t *testing.T) {
	// the session is the outer ALB's, whose token carries the Service and the endpoint, so the
	// endpoint ALBs follow it and keep nothing of their own, whatever their policy says
	docs := emitted(t, stickyShapes()["sticky rule"], endpointOpts(t))
	require.Equal(t, &albStickyDoc{
		Mode: so.ModeCookie, TTL: "1h0m0s",
		Cookie: &albStickyCookieDoc{Name: hostCookie, Secure: so.SecureAlways},
	}, docs[stickyOuter].ALB.Sticky)
	require.Nil(t, docs[stickyInner0].ALB.Sticky)
	require.Nil(t, docs[stickyInner1].ALB.Sticky)

	// unnamed, the cookie is the ALB's own, and it ends with the browser session
	m := weightedEndpointShape()
	m.Routes[0].Rules[0].Session = &ir.Session{Type: ir.SessionCookie}
	docs = emitted(t, m, endpointOpts(t))
	require.Equal(t, &albStickyDoc{
		Mode: so.ModeCookie, TTL: noLimit,
		Cookie: &albStickyCookieDoc{Name: CookieName(stickyOuter), Lifetime: so.LifetimeSession},
	}, docs[stickyOuter].ALB.Sticky)

	// with one Service, the endpoint ALB is the rule's
	m = endpointShape()
	m.Routes[0].Rules[0].Session = &ir.Session{Type: ir.SessionHeader, Name: "X-Session", AbsoluteMS: 1500}
	docs = emitted(t, m, endpointOpts(t))
	require.Equal(t, &albStickyDoc{
		Mode: so.ModeHeader, TTL: "1.5s", Header: &albStickyHeaderDoc{Name: "X-Session"},
	}, docs[stickyOuter].ALB.Sticky)
	m.Routes[0].Rules[0].Session = &ir.Session{Type: ir.SessionHeader}
	require.Nil(t, emitted(t, m, endpointOpts(t))[stickyOuter].ALB.Sticky.Header,
		"an unnamed header is the default one")
}

func TestCompileMemberSessions(t *testing.T) {
	// with no session of the rule's, each Service's endpoint ALB keeps the session its Service
	// asks for, or else the one its policy does
	docs := emitted(t, stickyShapes()["sticky members"], endpointOpts(t))
	require.Nil(t, docs[stickyOuter].ALB.Sticky)
	require.Equal(t, &albStickyDoc{
		Mode: so.ModeHeader, TTL: noLimit, Secret: classKey, Header: &albStickyHeaderDoc{Name: "X-Session"},
	}, docs[stickyInner0].ALB.Sticky)
	require.Equal(t, &albStickyDoc{
		Mode: so.ModeTable, TTL: "1h0m0s", Idle: "1m0s", Table: &albStickyTableDoc{Key: tenantKey},
	}, docs[stickyInner1].ALB.Sticky)
}

func TestCompilePolicySticky(t *testing.T) {
	key := filepath.Join(t.TempDir(), "sticky.key")
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0o600))
	opts := endpointOpts(t)
	opts.Defaults.StickySecretFile = key
	m := endpointShape()
	p := &m.Policies[0]
	p.Sticky = so.ModeCookie
	require.Equal(t, &albStickyDoc{
		Mode: so.ModeCookie, SecretFile: key, Cookie: &albStickyCookieDoc{Name: CookieName(stickyOuter)},
	}, emitted(t, m, opts)[stickyOuter].ALB.Sticky, "the ttl is left to the default")

	// a class's Secret outranks the configured file, and a table has no use for either
	p.StickySecret = classKey
	p.Sticky = so.ModeHeader
	d := emitted(t, m, opts)[stickyOuter].ALB.Sticky
	require.Equal(t, classKey, d.Secret)
	require.Empty(t, d.SecretFile, "a key is set inline or as a file, not both")
	p.Sticky, p.StickyKey = so.ModeTable, tenantKey
	require.Equal(t, &albStickyDoc{Mode: so.ModeTable, Table: &albStickyTableDoc{Key: tenantKey}},
		emitted(t, m, opts)[stickyOuter].ALB.Sticky)

	// a key a request cannot carry is left at the default
	p.StickyKey = "sni"
	require.Nil(t, emitted(t, m, opts)[stickyOuter].ALB.Sticky.Table)

	// none turns it off, and the service routing mode has no endpoint ALB to keep one
	p.Sticky = "none"
	require.Nil(t, emitted(t, m, opts)[stickyOuter].ALB.Sticky)
	p.Sticky, p.RoutingMode = so.ModeCookie, kubecfg.RoutingModeService
	require.Nil(t, emitted(t, m, opts)[stickyOuter].ALB)
}

func TestCompileStreamSticky(t *testing.T) {
	// a stream listener keeps sessions in a table, on the ALB it maps to, whatever the mode asked
	sticky := ir.Policy{Sticky: so.ModeCookie, StickyKey: "sni", StickyTTLMS: hourMS}
	single := withPolicy(streamShape(ir.ProtocolTLS, tcpMember(0, "a-svc", 1)), sticky)
	require.Equal(t, &albStickyDoc{Mode: so.ModeTable, TTL: "1h0m0s", Table: &albStickyTableDoc{Key: "sni"}},
		emitted(t, single, endpointOpts(t))[stickyStream].ALB.Sticky)
	tcp := withPolicy(streamShape(ir.ProtocolTCP, tcpMember(0, "a-svc", 1)), sticky)
	require.Nil(t, emitted(t, tcp, endpointOpts(t))[stickyStream].ALB.Sticky.Table,
		"a tcp flow has no server name")

	// over several Services the pool keeps the path; in service mode it is the only ALB
	for _, opts := range []*kubecfg.Options{endpointOpts(t), serviceOpts(t)} {
		pool := withPolicy(streamShape(ir.ProtocolTCP, tcpMember(0, "a-svc", 1), tcpMember(1, "b-svc", 1)), sticky)
		docs := emitted(t, pool, opts)
		require.Equal(t, so.ModeTable, docs[stickyStream].ALB.Sticky.Mode)
		if inner := docs[stickyStream0].ALB; inner != nil {
			require.Nil(t, inner.Sticky)
		}
	}
	require.Nil(t, emitted(t, streamShape(ir.ProtocolTCP, tcpMember(0, "a-svc", 1), tcpMember(1, "b-svc", 1)),
		endpointOpts(t))[stickyStream].ALB.Sticky)
}

func TestCompileMirrorsKeepNoSessions(t *testing.T) {
	// a mirror's responses are discarded, so its ALB issues nothing
	m := endpointShape()
	m.Policies[0].Sticky = so.ModeCookie
	m.Routes[0].Rules[0].Filters = []ir.Filter{{Type: ir.FilterMirror, Mirror: &ir.MirrorFilter{
		Service: ir.ServiceTarget{Namespace: "shop", Name: "shadow", Port: 80, PortName: "http"}, Percent: 100,
	}}}
	docs := emitted(t, m, endpointOpts(t))
	require.NotNil(t, docs[stickyOuter].ALB.Sticky)
	require.Nil(t, docs[stickyOuter+mirrorSuffix].ALB.Sticky)
}

func TestCookieName(t *testing.T) {
	a, b := CookieName(stickyInner0), CookieName(stickyInner1)
	require.NotEqual(t, a, b)
	require.Equal(t, a, CookieName(stickyInner0))
	require.True(t, strings.HasPrefix(a, so.DefaultCookieName+"_"))
	require.NoError(t, (&http.Cookie{Name: a, Value: "v"}).Valid())
}

func TestGeneratedSessionsKeepAClientOnItsService(t *testing.T) {
	// served through the real loader and router, a rule's session keeps a client on the Service
	// its first request reached, where the round robin would otherwise alternate
	g := group("shop", "web", 0, svcMember(0, "shop", "a", 80, 1), svcMember(1, "shop", "b", 80, 1))
	m := model([]ir.Route{hostRoute("web", testHost, 0, ir.Rule{
		BackendGroup: g.Name, Session: &ir.Session{Type: ir.SessionCookie, Name: "shop"},
	})}, g)
	rtr := serveIR(t, m, serviceOpts(t))
	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = testHost
	rtr.ServeHTTP(first, req)
	require.Equal(t, http.StatusOK, first.Code)
	cookies := first.Result().Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, "shop", cookies[0].Name)
	require.Zero(t, cookies[0].MaxAge, "a Session cookie carries no Max-Age")
	service := first.Header().Get("X-Upstream-Service")
	for range 4 {
		code, got := requestService(rtr, http.MethodGet, testHost, "/", "Cookie", "shop="+cookies[0].Value)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, service, got)
	}
}
