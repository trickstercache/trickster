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

package gateway

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/discovery/template"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/compile"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"

	"github.com/stretchr/testify/require"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestTranslateTCPAndUDPRoutes(t *testing.T) {
	// a TCP or UDP listener carries one route of its kind: the oldest keeps it, a route with
	// several rules is refused, and a route of another kind does not attach
	problems := golden(t, "tcp")
	containing(t, problems, "Gateway/infra/gw", `listener "named"`, "cannot have a hostname")
	containing(t, problems, "Gateway/infra/gw", `listener "wrong-kind"`, "HTTPRoute is not supported")
	containing(t, problems, "Gateway/infra/gw", `listener "wrong-kind"`, "admits no kind")
	containing(t, problems, "TCPRoute/data/late", `listener "db"`, "already served by TCPRoute/data/db")
	containing(t, problems, "TCPRoute/data/two-rules", "exactly one rule")
	containing(t, problems, "TCPRoute/data/db", "backendRef 2", "missing-svc not found")
	containing(t, problems, "UDPRoute/data/dns", "backendRef 2", "tcp-only-svc has no udp port 53")
	containing(t, problems, "UDPRoute/data/misplaced", "no listener allows routes")
	containing(t, problems, "HTTPRoute/data/web", "no listener allows routes")
	require.Len(t, problems, 9, "%v", details(problems))

	model, report := translateReport(t, "tcp")
	require.Len(t, model.Routes, 3)
	byKey := make(map[string]ir.Route)
	for _, r := range model.Routes {
		byKey[r.Source.Key()] = r
	}
	db := byKey["TCPRoute/data/db"]
	require.Equal(t, ir.ProtocolTCP, db.Protocol)
	require.Empty(t, db.Hostnames)
	require.Len(t, db.Rules, 1)
	require.Empty(t, db.Rules[0].Matches, "a stream rule matches nothing; it relays everything")
	dns := byKey["UDPRoute/data/dns"]
	require.Equal(t, ir.ProtocolUDP, dns.Protocol)
	require.Len(t, model.Backends, 3)
	for _, g := range model.Backends {
		switch g.Source.Key() {
		case "TCPRoute/data/db":
			require.Len(t, g.Members, 3, "the zero-weight reference is dropped")
			require.Equal(t, 3, g.Members[0].Weight)
			require.Equal(t, ir.ProtocolTCP, g.Members[0].Service.Scheme)
			require.True(t, g.Members[2].Invalid)
		case "TCPRoute/data/dns-tcp":
			// the port number is exposed over both transports; the TCP one is chosen
			require.Equal(t, "dns-tcp", g.Members[0].Service.PortName)
		case "UDPRoute/data/dns":
			require.Equal(t, "dns-udp", g.Members[0].Service.PortName, "declared second")
			require.Equal(t, "dns-udp", g.Members[1].Service.PortName, "declared first")
			require.Equal(t, ir.ProtocolUDP, g.Members[1].Service.Scheme)
			require.True(t, g.Members[2].Invalid, "a Service exposing TCP alone cannot carry UDP")
		}
	}

	require.Len(t, report.Gateways, 1)
	listeners := make(map[string]ir.ListenerStatus)
	for _, l := range report.Gateways[0].Listeners {
		listeners[l.Name] = l
	}
	require.Equal(t, []string{"TCPRoute"}, listeners["db"].SupportedKinds)
	require.Equal(t, 1, listeners["db"].AttachedRoutes, "only the served route counts")
	require.Equal(t, []string{"UDPRoute"}, listeners["dns"].SupportedKinds)
	require.Equal(t, 1, listeners["dns"].AttachedRoutes)
	require.True(t, condOf(t, listeners["dns-tcp"].Conditions, "Accepted").Status,
		"a TCP and a UDP listener share a port number")
	require.Equal(t, 1, listeners["dns-tcp"].AttachedRoutes)
	require.EqualValues(t, gwapiv1.ListenerReasonUnsupportedValue,
		condOf(t, listeners["named"].Conditions, "Accepted").Reason)
	require.Empty(t, listeners["wrong-kind"].SupportedKinds)
	require.EqualValues(t, gwapiv1.ListenerReasonInvalidRouteKinds,
		condOf(t, listeners["wrong-kind"].Conditions, "ResolvedRefs").Reason)

	routes := make(map[string]ir.RouteStatus)
	for _, r := range report.Routes {
		routes[r.Source.Key()] = r
	}
	require.Len(t, routes, 7)
	accepted := func(key string) ir.Condition {
		t.Helper()
		require.Len(t, routes[key].Parents, 1, key)
		return condOf(t, routes[key].Parents[0].Conditions, "Accepted")
	}
	require.True(t, accepted("TCPRoute/data/db").Status)
	require.False(t, condOf(t, routes["TCPRoute/data/db"].Parents[0].Conditions, "ResolvedRefs").Status)
	require.Equal(t, routeConflictReason, accepted("TCPRoute/data/late").Reason)
	require.EqualValues(t, gwapiv1.RouteReasonUnsupportedValue, accepted("TCPRoute/data/two-rules").Reason)
	require.True(t, accepted("UDPRoute/data/dns").Status)
	require.EqualValues(t, gwapiv1.RouteReasonNotAllowedByListeners, accepted("UDPRoute/data/misplaced").Reason)
	require.EqualValues(t, gwapiv1.RouteReasonNotAllowedByListeners, accepted("HTTPRoute/data/web").Reason)
}

func TestTranslateTLSRoutes(t *testing.T) {
	// a TLS listener relays by server name in Passthrough mode only, and a route claims each
	// name once, the older first
	problems := golden(t, "tls")
	containing(t, problems, "Gateway/infra/gw", `listener "any"`, "certificateRefs are ignored")
	containing(t, problems, "Gateway/infra/gw", `listener "terminate"`, "Passthrough only")
	containing(t, problems, "Gateway/infra/gw", `listener "no-tls"`, "Passthrough only")
	containing(t, problems, "TLSRoute/shop/dup", `listener "exact"`, `host "shop.example.com"`,
		"already served by TLSRoute/shop/shop")
	require.Len(t, problems, 4, "%v", details(problems))

	model, report := translateReport(t, "tls")
	byKey := make(map[string]ir.Route)
	for _, r := range model.Routes {
		byKey[r.Source.Key()] = r
	}
	require.Len(t, byKey, 4)
	require.Equal(t, []string{"shop.example.com"}, byKey["TLSRoute/shop/shop"].Hostnames)
	require.Len(t, byKey["TLSRoute/shop/shop"].Listeners, 2,
		"admitted by both listeners on the port, served once on the socket they share")
	require.Equal(t, []string{"api.example.com"}, byKey["TLSRoute/shop/api"].Hostnames,
		"the hostname outside the listener's is dropped")
	require.Equal(t, []string{"**.example.com"}, byKey["TLSRoute/shop/rest"].Hostnames)
	require.Empty(t, byKey["TLSRoute/shop/catch-all"].Hostnames)
	for _, r := range model.Routes {
		require.Equal(t, ir.ProtocolTLS, r.Protocol)
	}

	listeners := make(map[string]ir.ListenerStatus)
	for _, l := range report.Gateways[0].Listeners {
		listeners[l.Name] = l
	}
	require.Equal(t, []string{"TLSRoute"}, listeners["wild"].SupportedKinds)
	require.Equal(t, 3, listeners["wild"].AttachedRoutes)
	require.Equal(t, 1, listeners["exact"].AttachedRoutes, "the refused duplicate does not count")
	require.Equal(t, 1, listeners["any"].AttachedRoutes)
	for _, name := range []string{"terminate", "no-tls"} {
		require.EqualValues(t, gwapiv1.ListenerReasonUnsupportedValue,
			condOf(t, listeners[name].Conditions, "Accepted").Reason, name)
	}
	routes := make(map[string]ir.RouteStatus)
	for _, r := range report.Routes {
		routes[r.Source.Key()] = r
	}
	require.Equal(t, routeConflictReason,
		condOf(t, routes["TLSRoute/shop/dup"].Parents[0].Conditions, "Accepted").Reason)
	require.True(t, condOf(t, routes["TLSRoute/shop/api"].Parents[0].Conditions, "Accepted").Status)
}

func TestTranslateEndpointStream(t *testing.T) {
	// in endpoint mode a stream member is a discovery pool judged by readiness, whatever
	// health mode the class asked for, since no probe speaks the stream's protocol
	require.Empty(t, golden(t, "endpoint-tcp"))
	model, _, o := translateFixture(t, "endpoint-tcp")
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	conf := decodeOverlay(t, overlay)
	front := conf.Backends["kgw--tcproute.data.db_r0"]
	require.NotNil(t, front)
	require.Len(t, front.ALBOptions.Pool, 2)
	member := conf.Backends["kgw--tcproute.data.db_r0_b0"]
	require.NotNil(t, member.ALBOptions.Discovery)
	require.Equal(t, "provider", member.ALBOptions.Discovery.HealthMode)
	require.Equal(t, "tcp", member.ALBOptions.Discovery.Query.Scheme)
	tmpl := conf.Backends[member.ALBOptions.Discovery.TemplateBackend]
	require.True(t, tmpl.IsTemplate)
	require.Zero(t, tmpl.HealthCheck.Interval, "a stream member runs no probe")
	udp := conf.Backends["kgw--udproute.data.dns_r0"]
	require.Equal(t, "udp", udp.ALBOptions.Discovery.Query.Scheme)
	require.Equal(t, "dns-udp", udp.ALBOptions.Discovery.Query.Port,
		"the UDP port's name selects the UDP endpoints, not the TCP port's")
}

// streamTable builds a stream listener's routing table from a loaded configuration the way the
// daemon does: every backend mapped to the listener, by its hosts on a tls listener
func streamTable(t *testing.T, conf *config.Config, clients backends.Backends, listener string,
	sni bool,
) *l4.Table {
	t.Helper()
	tbl := l4.NewTable()
	members := conf.Backends.PoolMembers()
	for name, o := range conf.Backends {
		if o.IsTemplate || members.Contains(name) || !o.UsesListener(listener) {
			continue
		}
		up := l4.FromBackend(clients.Get(name))
		require.NotNil(t, up, name)
		hosts := o.Hosts
		if !sni || len(hosts) == 0 {
			hosts = []string{""}
		}
		for _, h := range hosts {
			require.NoError(t, tbl.Add(h, up))
		}
	}
	return tbl
}

func startStream(t *testing.T, protocol string, tbl *l4.Table) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := l4.NewServer("test", protocol, &l4.Config{Table: tbl})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// throughStream sends one HTTP request over a raw connection and returns the body, or an empty
// string when the connection was refused
func throughStream(t *testing.T, conn net.Conn) string {
	t.Helper()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req, err := http.NewRequest(http.MethodGet, "http://stream.test/", nil)
	require.NoError(t, err)
	if err := req.Write(conn); err != nil {
		return ""
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestTCPRouteIsServed(t *testing.T) {
	// connections are apportioned by weight across the members, the unresolvable member's share
	// is refused, and the origin sees the bytes untouched
	model, _, o := translateFixture(t, "tcp")
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	conf, clients, _ := loadOverlay(t, overlay, baseConfig, nil, false)
	addr := startStream(t, l4.ProtocolTCP,
		streamTable(t, conf, clients, compile.ListenerName(5432, ir.ProtocolTCP), false))
	seen := make(map[string]int)
	for range 10 {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		require.NoError(t, err)
		seen[throughStream(t, conn)]++
	}
	require.Equal(t, map[string]int{"db-svc": 6, "replica-svc": 2, "": 2}, seen)
}

func TestTLSRouteIsServed(t *testing.T) {
	// the server name selects the backend, which terminates the session itself: a precise name,
	// the wildcard beneath the listener's, and the catch-all on the other listener
	model, _, o := translateFixture(t, "tls")
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	conf, clients, _ := loadOverlay(t, overlay, baseConfig, nil, true)
	wild := startStream(t, l4.ProtocolTLS,
		streamTable(t, conf, clients, compile.ListenerName(8443, ir.ProtocolTLS), true))
	any := startStream(t, l4.ProtocolTLS,
		streamTable(t, conf, clients, compile.ListenerName(9443, ir.ProtocolTLS), true))
	dial := func(addr, serverName string) net.Conn {
		t.Helper()
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: true,
		}) // #nosec G402 -- the origins present test certificates
		if err != nil {
			return nil
		}
		return conn
	}
	for serverName, want := range map[string]string{
		"shop.example.com": "shop-svc", "api.example.com": "api-svc",
		"other.example.com": "fallback-svc", "deep.sub.example.com": "fallback-svc",
	} {
		conn := dial(wild, serverName)
		require.NotNil(t, conn, serverName)
		require.Equal(t, want, throughStream(t, conn), serverName)
	}
	require.Nil(t, dial(wild, "api.other.org"), "a name outside the listener's is refused")
	conn := dial(any, "anything.at.all")
	require.NotNil(t, conn)
	require.Equal(t, "fallback-svc", throughStream(t, conn))
}

func TestTCPRouteIsServedThroughDiscoveredMembers(t *testing.T) {
	// in endpoint mode the pool is empty until endpoints are discovered; once they are, the
	// listener relays across them and the readiness of each decides whether it is dialed
	model, _, o := translateFixture(t, "endpoint-tcp")
	overlay, _, err := compile.CompileWith(model, o, prometheusPaths)
	require.NoError(t, err)
	conf, clients, _ := loadOverlay(t, overlay, baseConfig, nil, false)
	addr := startStream(t, l4.ProtocolTCP,
		streamTable(t, conf, clients, compile.ListenerName(5432, ir.ProtocolTCP), false))
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	require.Empty(t, throughStream(t, conn), "no endpoint is known yet")

	origins := map[string]string{
		"db-svc":      originFor(t, map[string]string{}, "tcp://db-svc.data.svc:5432", nil, false),
		"replica-svc": originFor(t, map[string]string{}, "tcp://replica-svc.data.svc:5432", nil, false),
	}
	discover := func(memberALB, tmplName string, ready bool, service string) {
		t.Helper()
		u, err := neturl.Parse(origins[service])
		require.NoError(t, err)
		opts, err := template.Instantiate(memberALB+"_ep", conf.Backends[tmplName],
			discovery.Member{Name: "ep", Address: u.Host, Scheme: "tcp", Ready: discovery.Ready})
		require.NoError(t, err)
		member, err := backends.New(memberALB+"_ep", opts, nil, lm.NewRouter(), nil)
		require.NoError(t, err)
		status := healthcheck.StatusFailing
		if ready {
			status = healthcheck.StatusPassing
		}
		st := healthcheck.NewStatus(member.Name(), "", "", status, time.Time{}, nil)
		target := pool.NewTarget(lm.NewRouter(), st, member).WithExternalHealth()
		albClient, ok := clients.Get(memberALB).(*alb.Client)
		require.True(t, ok)
		require.True(t, albClient.SetDynamicTargets(pool.Targets{target}))
	}
	discover("kgw--tcproute.data.db_r0_b0", "kgw--tcproute.data.db_r0_b0_tmpl", true, "db-svc")
	discover("kgw--tcproute.data.db_r0_b1", "kgw--tcproute.data.db_r0_b1_tmpl", true, "replica-svc")
	seen := make(map[string]int)
	for range 6 {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		require.NoError(t, err)
		seen[throughStream(t, conn)]++
	}
	require.Equal(t, map[string]int{"db-svc": 4, "replica-svc": 2}, seen,
		"the outer pool's weights apportion across the discovered members")

	// an endpoint that stops being ready is dialed no more, and its weight's share goes unserved
	discover("kgw--tcproute.data.db_r0_b1", "kgw--tcproute.data.db_r0_b1_tmpl", false, "replica-svc")
	seen = make(map[string]int)
	for range 6 {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		require.NoError(t, err)
		seen[throughStream(t, conn)]++
	}
	require.Equal(t, map[string]int{"db-svc": 4, "": 2}, seen)
}
