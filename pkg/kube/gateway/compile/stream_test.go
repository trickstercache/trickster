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

package compile

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

func streamShape(protocol string, members ...ir.BackendMember) *ir.IR {
	src := ir.Source{Kind: ir.KindTCPRoute, Namespace: "data", Name: "db"}
	listener := ir.Listener{
		Name: "Gateway/infra/gw/db", Section: "db", Port: 5432, Protocol: protocol,
		Source: ir.Source{Kind: ir.KindGateway, Namespace: "infra", Name: "gw"},
	}
	return &ir.IR{
		Listeners: []ir.Listener{listener},
		Routes: []ir.Route{{
			Name: "TCPRoute/data/db|" + listener.Name, Source: src, Protocol: protocol,
			Listeners: []string{listener.Name},
			Rules:     []ir.Rule{{BackendGroup: "TCPRoute/data/db|r0"}},
		}},
		Backends: []ir.BackendGroup{{
			Name: "TCPRoute/data/db|r0", Source: src, Members: members,
		}},
	}
}

func tcpMember(ref int, name string, weight int) ir.BackendMember {
	return ir.BackendMember{RefIndex: ref, Weight: weight, Service: ir.ServiceTarget{
		Namespace: "data", Name: name, Port: 5432, PortName: "pg", Scheme: ir.ProtocolTCP,
	}}
}

func TestCompileStreamListeners(t *testing.T) {
	// a stream listener is emitted with its protocol, and a udp listener apart from a tcp one on
	// the same port number
	listeners := []ir.Listener{
		{Name: "a", Port: 53, Protocol: ir.ProtocolTCP},
		{Name: "b", Port: 53, Protocol: ir.ProtocolUDP},
		{Name: "c", Port: 53, Protocol: ir.ProtocolTCP},
		{Name: "d", Port: 8443, Protocol: ir.ProtocolTLS},
		{Name: "e", Port: 80, Protocol: ir.ProtocolHTTP},
	}
	docs := compileListeners(listeners)
	require.Len(t, docs, 4)
	require.Equal(t, &listenerDoc{Protocol: "tcp", Port: 53}, docs[ListenerName(53, ir.ProtocolTCP)])
	require.Equal(t, &listenerDoc{Protocol: "udp", Port: 53}, docs[ListenerName(53, ir.ProtocolUDP)])
	require.Equal(t, &listenerDoc{Protocol: "tls", Port: 8443}, docs[ListenerName(8443, ir.ProtocolTLS)])
	require.Equal(t, &listenerDoc{Port: 80}, docs[ListenerName(80, ir.ProtocolHTTP)])
}

func TestCompileStreamSingleMember(t *testing.T) {
	// one resolvable member is a reverse proxy backend for its origin, bound to the listener
	doc, err := buildDocument(streamShape(ir.ProtocolTCP, tcpMember(0, "db-svc", 1)), serviceOpts(t), nil)
	require.NoError(t, err)
	require.Len(t, doc.Backends, 1)
	b := doc.Backends["kgw--tcproute.data.db_r0"]
	require.NotNil(t, b)
	require.Equal(t, providers.ReverseProxyShort, b.Provider)
	require.Equal(t, "tcp://db-svc.data.svc:5432", b.OriginURL)
	require.Equal(t, []string{ListenerName(5432, ir.ProtocolTCP)}, b.ListenerNames)
	require.True(t, b.AnyHostRouting)
	require.True(t, b.PathRoutingDisabled)
	require.True(t, b.PathDefaultsDisabled)
	require.Empty(t, b.Paths, "a stream backend serves no HTTP path")
	require.Empty(t, b.CacheName)
	require.Nil(t, b.ALB)
}

func TestCompileStreamWeightedAndInvalid(t *testing.T) {
	// several members are a round robin pool with their weights; an unresolvable one holds its
	// share with an origin that never resolves, and a tls route's hostnames bind the pool
	m := streamShape(ir.ProtocolTLS, tcpMember(0, "a-svc", 3), tcpMember(1, "b-svc", 1),
		ir.BackendMember{RefIndex: 2, Weight: 2, Invalid: true, InvalidReason: "gone"})
	m.Routes[0].Hostnames = []string{"shop.example.com", "**.api.example.com"}
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	front := doc.Backends["kgw--tcproute.data.db_r0"]
	require.NotNil(t, front)
	require.Equal(t, providers.ALB, front.Provider)
	require.Equal(t, "rr", front.ALB.Mechanism)
	require.Equal(t, []string{"**.api.example.com", "shop.example.com"}, front.Hosts, "hostnames are emitted in canonical order")
	require.False(t, front.AnyHostRouting)
	require.Len(t, front.ALB.Pool, 3)
	require.Equal(t, &albPoolDoc{Name: "kgw--tcproute.data.db_r0_b0", Weight: 3}, front.ALB.Pool[0])
	require.Equal(t, &albPoolDoc{Name: "kgw--tcproute.data.db_r0_b2", Weight: 2}, front.ALB.Pool[2])
	invalid := doc.Backends["kgw--tcproute.data.db_r0_b2"]
	require.Equal(t, unresolvedStreamOriginURL, invalid.OriginURL)
	require.Equal(t, front.ListenerNames, invalid.ListenerNames)
	require.Empty(t, invalid.Hosts, "a pool member registers no host of its own")
	member := doc.Backends["kgw--tcproute.data.db_r0_b1"]
	require.Equal(t, "tcp://b-svc.data.svc:5432", member.OriginURL)
	require.True(t, member.PathRoutingDisabled)
}

func TestCompileStreamEndpointMode(t *testing.T) {
	// in endpoint mode a member is a discovery pool over a template, judged by readiness rather
	// than a probe, since no probe speaks the stream's protocol; udp members query a udp scheme
	opts := endpointOpts(t)
	opts.Defaults.HealthMode = "probe"
	m := streamShape(ir.ProtocolUDP, ir.BackendMember{RefIndex: 0, Weight: 1, Service: ir.ServiceTarget{
		Namespace: "data", Name: "dns-svc", Port: 53, Scheme: ir.ProtocolUDP,
	}})
	doc, err := buildDocument(m, opts, nil)
	require.NoError(t, err)
	require.Len(t, doc.Discovery, 1)
	front := doc.Backends["kgw--tcproute.data.db_r0"]
	require.NotNil(t, front)
	require.Equal(t, providers.ALB, front.Provider)
	require.Equal(t, "kgw--tcproute.data.db_r0_b0_tmpl", front.ALB.Discovery.TemplateBackend)
	require.Equal(t, "provider", front.ALB.Discovery.HealthMode)
	require.Equal(t, &queryDoc{
		Kind: "endpointslices", Namespace: "data", Service: "dns-svc",
		Scheme: "udp",
	}, front.ALB.Discovery.Query)
	tmpl := doc.Backends[front.ALB.Discovery.TemplateBackend]
	require.True(t, tmpl.IsTemplate)
	require.Empty(t, tmpl.OriginURL)
	require.Nil(t, tmpl.HealthCheck)
	require.Empty(t, tmpl.Paths)

	// an unsupported routing mode is an error, as it is for an HTTP route
	m.Policies = []ir.Policy{{Name: "p", RoutingMode: "elsewhere"}}
	m.Routes[0].Rules[0].Policy = "p"
	_, err = buildDocument(m, opts, nil)
	require.ErrorIs(t, err, ErrUnsupportedRoutingMode)
}

func TestCompileStreamSkipsWhatItCannotServe(t *testing.T) {
	// a route naming no known listener or group, or a group with no member, compiles to nothing
	m := streamShape(ir.ProtocolTCP)
	doc, err := buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Empty(t, doc.Backends)
	m = streamShape(ir.ProtocolTCP, tcpMember(0, "db-svc", 1))
	m.Routes[0].Rules[0].BackendGroup = "unknown"
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Empty(t, doc.Backends)
	m = streamShape(ir.ProtocolTCP, tcpMember(0, "db-svc", 1))
	m.Routes[0].Listeners = []string{"unknown"}
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Empty(t, doc.Backends)
	m = streamShape(ir.ProtocolTCP, tcpMember(0, "db-svc", 1))
	m.Routes[0].Rules = nil
	doc, err = buildDocument(m, serviceOpts(t), nil)
	require.NoError(t, err)
	require.Empty(t, doc.Backends)
}

// a policy's mechanism balances each Service's endpoints; the weights between a rule's
// backendRefs stay with round robin, since Gateway API makes them an exact apportionment
func TestCompileLoadBalancingPolicy(t *testing.T) {
	withPolicy := func(m *ir.IR, p ir.Policy) *ir.IR {
		p.Name = "lb"
		m.Policies = []ir.Policy{p}
		for i := range m.Routes {
			for j := range m.Routes[i].Rules {
				m.Routes[i].Rules[j].Policy = "lb"
			}
		}
		return m
	}
	t.Run("tcp endpoints, weighted rule", func(t *testing.T) {
		m := withPolicy(streamShape(ir.ProtocolTCP, tcpMember(0, "a-svc", 3), tcpMember(1, "b-svc", 1)),
			ir.Policy{LoadBalancing: "p2c"})
		doc, err := buildDocument(m, endpointOpts(t), nil)
		require.NoError(t, err)
		outer := doc.Backends["kgw--tcproute.data.db_r0"]
		require.Equal(t, "rr", outer.ALB.Mechanism, "backendRef weights are apportioned exactly")
		require.Len(t, outer.ALB.Pool, 2)
		for _, name := range []string{"kgw--tcproute.data.db_r0_b0", "kgw--tcproute.data.db_r0_b1"} {
			inner := doc.Backends[name]
			require.Equal(t, "p2c", inner.ALB.Mechanism, name)
			require.NotNil(t, inner.ALB.Discovery, name)
			require.Nil(t, inner.ALB.HRW, name)
		}
	})
	t.Run("stream keys are what the listener can read", func(t *testing.T) {
		for _, test := range []struct {
			protocol, key, want string
		}{
			{ir.ProtocolTCP, "client_ip", "client_ip"},
			{ir.ProtocolTLS, "sni", "sni"},
			// there is no server name on a tcp or udp route, and no header on any of them
			{ir.ProtocolTCP, "sni", ""},
			{ir.ProtocolUDP, "sni", ""},
			{ir.ProtocolTLS, "header:X-Tenant", ""},
			{ir.ProtocolTCP, "", ""},
		} {
			m := withPolicy(streamShape(test.protocol, tcpMember(0, "a-svc", 1)),
				ir.Policy{LoadBalancing: "hrw", LoadBalancingKey: test.key})
			doc, err := buildDocument(m, endpointOpts(t), nil)
			require.NoError(t, err)
			front := doc.Backends["kgw--tcproute.data.db_r0"]
			require.Equal(t, "hrw", front.ALB.Mechanism)
			if test.want == "" {
				require.Nil(t, front.ALB.HRW, "%s key %q", test.protocol, test.key)
				continue
			}
			require.Equal(t, &albHRWDoc{Key: test.want}, front.ALB.HRW, "%s key %q", test.protocol, test.key)
		}
	})
	t.Run("a key without hrw is not compiled", func(t *testing.T) {
		m := withPolicy(streamShape(ir.ProtocolTCP, tcpMember(0, "a-svc", 1)),
			ir.Policy{LoadBalancing: "lc", LoadBalancingKey: "client_ip"})
		doc, err := buildDocument(m, endpointOpts(t), nil)
		require.NoError(t, err)
		front := doc.Backends["kgw--tcproute.data.db_r0"]
		require.Equal(t, "lc", front.ALB.Mechanism)
		require.Nil(t, front.ALB.HRW)
	})
	t.Run("http endpoints", func(t *testing.T) {
		g := group("shop", "web", 0, svcMember(0, "shop", "a", 80, 3), svcMember(1, "shop", "b", 80, 1))
		m := withPolicy(&ir.IR{
			Listeners: []ir.Listener{httpListener()},
			Routes:    []ir.Route{route("shop", "web", ir.Rule{BackendGroup: g.Name})},
			Backends:  []ir.BackendGroup{g},
		}, ir.Policy{LoadBalancing: "hrw", LoadBalancingKey: "header:X-Tenant"})
		doc, err := buildDocument(m, endpointOpts(t), nil)
		require.NoError(t, err)
		require.Equal(t, "rr", doc.Backends["kgw--httproute.shop.web_r0"].ALB.Mechanism)
		inner := doc.Backends["kgw--httproute.shop.web_r0_b0"]
		require.Equal(t, "hrw", inner.ALB.Mechanism)
		require.Equal(t, &albHRWDoc{Key: "header:X-Tenant"}, inner.ALB.HRW)
		// a request has no server name to key on
		m.Policies[0].LoadBalancingKey = "sni"
		doc, err = buildDocument(m, endpointOpts(t), nil)
		require.NoError(t, err)
		require.Nil(t, doc.Backends["kgw--httproute.shop.web_r0_b0"].ALB.HRW)
	})
	t.Run("service mode has no endpoints to balance", func(t *testing.T) {
		m := withPolicy(streamShape(ir.ProtocolTCP, tcpMember(0, "a-svc", 1)), ir.Policy{LoadBalancing: "p2c"})
		doc, err := buildDocument(m, serviceOpts(t), nil)
		require.NoError(t, err)
		require.Nil(t, doc.Backends["kgw--tcproute.data.db_r0"].ALB)
	})
}
