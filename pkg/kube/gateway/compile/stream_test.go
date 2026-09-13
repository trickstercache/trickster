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
