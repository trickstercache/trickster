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

	albnames "github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

const moqListenerName = "kgw--listener-moq-443"

func moqMember(refIndex int, name string, weight int) ir.BackendMember {
	m := svcMember(refIndex, "media", name, 4443, weight)
	m.Service.Scheme = "moqt"
	return m
}

func moqModel(policy *ir.Policy, groups ...ir.BackendGroup) *ir.IR {
	m := &ir.IR{Listeners: []ir.Listener{
		{Name: "moq", Port: 443, Protocol: ir.ProtocolMoQ, Source: src("Gateway", "infra", "gw")},
	}}
	if policy != nil {
		m.Policies = []ir.Policy{*policy}
	}
	for i, g := range groups {
		r := ir.Route{
			Name: g.Name, Source: g.Source, Listeners: []string{"moq"}, Protocol: ir.ProtocolMoQ,
			Hostnames: []string{"live.example.com"},
			Rules:     []ir.Rule{{BackendGroup: g.Name}},
		}
		if policy != nil {
			r.Rules[0].Policy = policy.Name
		}
		if i == 0 {
			m.Routes = append(m.Routes, ir.Route{Name: "unbound", Listeners: []string{"absent"}, Protocol: ir.ProtocolMoQ})
			m.Routes = append(m.Routes, ir.Route{Name: "ruleless", Listeners: []string{"moq"}, Protocol: ir.ProtocolMoQ})
			m.Routes = append(m.Routes, ir.Route{
				Name: "groupless", Listeners: []string{"moq"}, Protocol: ir.ProtocolMoQ,
				Rules: []ir.Rule{{BackendGroup: "absent"}},
			})
		}
		m.Routes = append(m.Routes, r)
		m.Backends = append(m.Backends, g)
	}
	return m
}

func udpGroup(name string, members ...ir.BackendMember) ir.BackendGroup {
	g := group("media", name, 0, members...)
	g.Source.Kind = ir.KindUDPRoute
	return g
}

func TestCompileMoQUDPRoute(t *testing.T) {
	opts := serviceOpts(t)
	opts.Defaults.IPACLName = "office"
	policy := &ir.Policy{Name: "relay", MaxTTLMS: 30000, LoadBalancing: albnames.MechanismP2C}
	verified := moqMember(0, "verified", 1)
	verified.TLS = &ir.BackendTLS{Hostname: "origin.example.com", CACertificates: []string{"a", "b"}}
	system := moqMember(1, "system", 1)
	system.TLS = &ir.BackendTLS{Hostname: "public.example.com", System: true}
	lost := ir.BackendMember{RefIndex: 2, Weight: 2, Invalid: true}
	m := moqModel(policy,
		udpGroup("live", moqMember(0, "origin", 1)),
		udpGroup("vod", verified, system, lost),
	)
	doc, err := buildDocument(m, opts, nil, nil)
	require.NoError(t, err)
	require.Equal(t, &listenerDoc{Protocol: ir.ProtocolMoQ, Port: 443, TLSRuntimeCerts: true, IPACLName: "office"},
		doc.Listeners[moqListenerName], "a moq listener judges its sessions by the class's access list")

	live := doc.Backends[GroupName(m.Backends[0])]
	require.NotNil(t, live, "backends: %v", doc.Backends)
	require.Equal(t, providers.MoQ, live.Provider)
	require.Equal(t, "moqt://origin.media.svc:4443/", live.OriginURL)
	require.Equal(t, []string{moqListenerName}, live.ListenerNames)
	require.Equal(t, []string{"live.example.com"}, live.Hosts)
	require.Equal(t, "30s", live.MaxTTL)
	require.True(t, live.TLS.InsecureSkipVerify, "an origin no BackendTLSPolicy names is trusted to the cluster")
	require.Empty(t, live.IPACLName, "the access list is the listener's")

	vod := doc.Backends[GroupName(m.Backends[1])]
	require.Empty(t, vod.OriginURL)
	origins := GroupName(m.Backends[1]) + moqOriginsSuffix
	require.Equal(t, origins, vod.MoQ.Upstream.ALB)
	alb := doc.Backends[origins]
	require.Equal(t, providers.ALB, alb.Provider)
	require.True(t, alb.PathRoutingDisabled)
	require.Equal(t, albnames.MechanismP2C, alb.ALB.Mechanism)
	require.Len(t, alb.ALB.Pool, 3)
	byName := map[string]*backendDoc{}
	for _, p := range alb.ALB.Pool {
		byName[p.Name] = doc.Backends[p.Name]
		require.NotNil(t, byName[p.Name], p.Name)
		require.Equal(t, []string{moqListenerName}, byName[p.Name].ListenerNames)
	}
	first := doc.Backends[alb.ALB.Pool[0].Name]
	require.Equal(t, "moqt://verified.media.svc:4443/", first.OriginURL)
	require.Equal(t, &tlsDoc{ServerName: "origin.example.com", CertificateAuthorityPEM: "a\nb",
		ExcludeSystemRoots: true}, first.TLS)
	require.Equal(t, &tlsDoc{ServerName: "public.example.com"}, doc.Backends[alb.ALB.Pool[1].Name].TLS)
	third := doc.Backends[alb.ALB.Pool[2].Name]
	require.Equal(t, unresolvedMoQOriginURL, third.OriginURL, "an unresolved ref holds its weight")
	require.Equal(t, 2, alb.ALB.Pool[2].Weight)

	// a mechanism no native listener balances by is left at hrw
	policy.LoadBalancing = albnames.MechanismLT
	doc, err = buildDocument(moqModel(policy, udpGroup("vod", moqMember(0, "a", 1), moqMember(1, "b", 1))),
		opts, nil, nil)
	require.NoError(t, err)
	require.Equal(t, albnames.MechanismHRW, doc.Backends[GroupName(udpGroup("vod"))+moqOriginsSuffix].ALB.Mechanism)
}

func webTransportModel(rule, member *ir.Policy, members ...ir.BackendMember) *ir.IR {
	g := group("media", "live", 0, members...)
	r := route("media", "live", ir.Rule{
		Matches:      []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/live"}}},
		BackendGroup: g.Name,
	})
	r.Listeners = []string{"l443"}
	m := &ir.IR{
		Listeners: []ir.Listener{{Name: "l443", Port: 443, Protocol: ir.ProtocolHTTPS, HTTP3: true,
			Source: src("Gateway", "infra", "gw")}},
		Routes:   []ir.Route{r},
		Backends: []ir.BackendGroup{g},
	}
	if rule != nil {
		m.Policies = append(m.Policies, *rule)
		m.Routes[0].Rules[0].Policy = rule.Name
	}
	if member != nil {
		m.Policies = append(m.Policies, *member)
		for i := range m.Backends[0].Members {
			if m.Backends[0].Members[i].Service.Name == "origin" {
				m.Backends[0].Members[i].Policy = member.Name
			}
		}
	}
	return m
}

func TestCompileMoQHTTPRoute(t *testing.T) {
	opts := serviceOpts(t)
	relay := &ir.Policy{Name: "relay", Provider: providers.MoQ}
	name := GroupName(group("media", "live", 0))

	doc, err := buildDocument(webTransportModel(relay, nil, svcMember(0, "media", "origin", 443, 1)), opts, nil, nil)
	require.NoError(t, err)
	require.Equal(t, &http3Doc{Enabled: true}, doc.Listeners["kgw--listener-https-443"].HTTP3)
	edge := doc.Backends[name]
	require.NotNil(t, edge, "backends: %v", doc.Backends)
	require.Equal(t, providers.MoQ, edge.Provider)
	require.Equal(t, "https://origin.media.svc:443/", edge.OriginURL, "a WebTransport origin is dialed over HTTP/3")
	require.Len(t, edge.Paths, 1)
	require.Equal(t, providers.MoQ, edge.Paths[0].Handler)
	require.Empty(t, edge.CacheKeyPrefix)

	// a policy on the Service alone makes a relay of a rule reaching only it
	doc, err = buildDocument(webTransportModel(nil, relay, svcMember(0, "media", "origin", 443, 1),
		ir.BackendMember{RefIndex: 1, Weight: 1, Invalid: true}), opts, nil, nil)
	require.NoError(t, err)
	edge = doc.Backends[name]
	require.Equal(t, providers.MoQ, edge.Provider)
	alb := doc.Backends[name+moqOriginsSuffix]
	require.NotNil(t, alb)
	require.Equal(t, unresolvedOriginURL+"/", doc.Backends[alb.ALB.Pool[1].Name].OriginURL)

	// one that also reaches another Service is no relay
	doc, err = buildDocument(webTransportModel(nil, relay, svcMember(0, "media", "origin", 443, 1),
		svcMember(1, "media", "web", 8080, 1)), opts, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, providers.MoQ, doc.Backends[name].Provider)
	doc, err = buildDocument(webTransportModel(nil, nil, svcMember(0, "media", "origin", 443, 1)), opts, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, providers.MoQ, doc.Backends[name].Provider)

	https := &ir.IR{Listeners: []ir.Listener{{Name: "l", Port: 8443, Protocol: ir.ProtocolHTTPS}}}
	require.Nil(t, compileListeners(https.Listeners)["kgw--listener-https-8443"].HTTP3,
		"an https listener without HTTP/3 serves TCP alone")
}
