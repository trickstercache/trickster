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
	"strings"
	"time"

	albnames "github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
)

const (
	unresolvedMoQOriginURL = "moqt://unresolved.kgw.invalid:1/"
	moqOriginsSuffix       = "-origins"
	schemeHTTPS            = "https"
)

type moqDoc struct {
	Upstream *moqUpstreamDoc `yaml:"upstream,omitempty"`
}

type moqUpstreamDoc struct {
	ALB string `yaml:"alb,omitempty"`
}

var moqMechanisms = map[string]bool{
	albnames.MechanismRR: true, albnames.MechanismRoundRobin: true,
	albnames.MechanismP2C: true, albnames.MechanismPowerOfTwoChoices: true,
	albnames.MechanismHRW: true, albnames.MechanismHighestRandomWeight: true,
	albnames.MechanismLC: true, albnames.MechanismLeastConnections: true,
}

func splitMoQRoutes(routes []ir.Route) (other, moq []ir.Route) {
	for _, r := range routes {
		if r.Protocol == ir.ProtocolMoQ {
			moq = append(moq, r)
			continue
		}
		other = append(other, r)
	}
	return other, moq
}

func compileMoQRoutes(doc *document, routes []ir.Route, idx index, opts *kubecfg.Options) {
	// a route served as MoQ Transport sessions is one moq backend on its listeners, relaying from the Service
	// at its cluster address, or from several through an alb picking one per track
	if len(routes) == 0 {
		return
	}
	if doc.Backends == nil {
		doc.Backends = make(map[string]*backendDoc)
	}
	for _, r := range routes {
		listeners := idx.listenerNames(r)
		if len(listeners) == 0 || len(r.Rules) == 0 {
			continue
		}
		rule := r.Rules[0]
		group, ok := idx.groups[rule.BackendGroup]
		if !ok || len(group.Members) == 0 {
			continue
		}
		eff := resolve(opts, idx.policy(rule))
		compileMoQRule(doc, r, group, listeners, eff)
		for _, name := range listeners {
			// a moq listener judges its sessions by its own ip acl; one on a backend would go unread
			if l := doc.Listeners[name]; l != nil && l.IPACLName == "" {
				l.IPACLName = eff.ipACLName
			}
		}
	}
}

func moqEffective(opts *kubecfg.Options, idx index, r *ir.Route, rule ir.Rule, group ir.BackendGroup) (effective, bool) {
	// an HTTPRoute rule relays MoQ when its own policy names the provider, or when that of every Service it reaches does
	eff := resolve(opts, idx.policy(rule)).forRoute(r)
	if eff.tsProvider == providers.MoQ {
		return eff, true
	}
	var found bool
	for _, m := range group.Members {
		if m.Invalid {
			continue
		}
		meff := resolveMember(opts, idx.policy(rule), idx.named(m.Policy)).forRoute(r)
		if meff.tsProvider != providers.MoQ {
			return eff, false
		}
		if !found {
			eff, found = meff, true
		}
	}
	return eff, found
}

func compileMoQRule(doc *document, r ir.Route, group ir.BackendGroup, listeners []string, eff effective) {
	name := GroupName(group)
	edge := &backendDoc{
		Provider: providers.MoQ, ListenerNames: listeners, Hosts: r.Hostnames, CacheName: eff.cacheName,
		GeoACLName: eff.geoACLName,
	}
	moqOrigins(doc.Backends, name, group, listeners, eff, "", edge)
	doc.Backends[name] = edge
}

func moqOrigins(out map[string]*backendDoc, name string, group ir.BackendGroup, listeners []string, eff effective,
	scheme string, edge *backendDoc,
) {
	// the edge relays from its one origin, or through an alb picking one per track; scheme overrides the members'
	if eff.policy != nil && eff.policy.MaxTTLMS > 0 {
		edge.MaxTTL = (time.Duration(eff.policy.MaxTTLMS) * time.Millisecond).String()
	}
	if len(group.Members) == 1 {
		m := group.Members[0]
		edge.OriginURL, edge.TLS = moqOriginURL(m, scheme), moqOriginTLS(m)
		return
	}
	pool := make([]*albPoolDoc, 0, len(group.Members))
	for _, m := range group.Members {
		memberName := GroupMemberName(group, m)
		// a pool member carries the edge's listeners so it binds no other port, though it is never routed
		out[memberName] = &backendDoc{
			Provider: providers.MoQ, OriginURL: moqOriginURL(m, scheme), TLS: moqOriginTLS(m),
			ListenerNames: listeners, PathRoutingDisabled: true, PathDefaultsDisabled: true,
		}
		pool = append(pool, &albPoolDoc{Name: memberName, Weight: m.Weight})
	}
	mechanism := albnames.MechanismHRW
	if moqMechanisms[eff.loadBalancing] {
		mechanism = eff.loadBalancing
	}
	origins := name + moqOriginsSuffix
	// the alb is reached only through the edge, so it registers no route of its own on any listener
	out[origins] = &backendDoc{
		Provider: providers.ALB, PathRoutingDisabled: true, PathDefaultsDisabled: true,
		ALB: &albDoc{Mechanism: mechanism, Pool: pool},
	}
	edge.MoQ = &moqDoc{Upstream: &moqUpstreamDoc{ALB: origins}}
}

func moqOriginURL(m ir.BackendMember, scheme string) string {
	// an unresolvable ref holds its share with an origin that never answers, so its tracks move to the others
	if m.Invalid {
		if scheme == schemeHTTPS {
			return unresolvedOriginURL + "/"
		}
		return unresolvedMoQOriginURL
	}
	svc := m.Service
	if scheme != "" {
		svc.Scheme = scheme
	}
	return ServiceURL(svc) + "/"
}

func moqOriginTLS(m ir.BackendMember) *tlsDoc {
	// QUIC is always TLS: a Service's BackendTLSPolicy verifies it, else it is trusted to the cluster network as
	// plaintext HTTP to a Service is
	if m.TLS == nil {
		return &tlsDoc{InsecureSkipVerify: true}
	}
	t := &tlsDoc{ServerName: m.TLS.Hostname}
	if !m.TLS.System {
		t.CertificateAuthorityPEM = strings.Join(m.TLS.CACertificates, "\n")
		t.ExcludeSystemRoots = true
	}
	return t
}
