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
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	tstrings "github.com/trickstercache/trickster/v2/pkg/util/strings"

	corev1 "k8s.io/api/core/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const schemeMoQT = "moqt"

// streamInput is a TCPRoute, TLSRoute or UDPRoute in one shape: its metadata, parents and
// hostnames in an HTTPRoute shell, so attachment is decided as for every other route kind
type streamInput struct {
	*gwapiv1.HTTPRoute
	kind     string
	protocol string
	rules    [][]gwapiv1.BackendRef
}

func streamShell(meta *gwapiv1.HTTPRoute, common gwapiv1.CommonRouteSpec,
	hosts []gwapiv1.Hostname,
) *gwapiv1.HTTPRoute {
	meta.Spec.CommonRouteSpec = common
	meta.Spec.Hostnames = hosts
	return meta
}

func (t *translator) streamInputs() []streamInput {
	var out []streamInput
	for _, r := range t.cfg.Cache.TCPRoutes() {
		rules := make([][]gwapiv1.BackendRef, 0, len(r.Spec.Rules))
		for _, rule := range r.Spec.Rules {
			rules = append(rules, rule.BackendRefs)
		}
		out = append(out, streamInput{
			HTTPRoute: streamShell(&gwapiv1.HTTPRoute{ObjectMeta: r.ObjectMeta}, r.Spec.CommonRouteSpec, nil),
			kind:      ir.KindTCPRoute, protocol: ir.ProtocolTCP, rules: rules,
		})
	}
	for _, r := range t.cfg.Cache.TLSRoutes() {
		rules := make([][]gwapiv1.BackendRef, 0, len(r.Spec.Rules))
		for _, rule := range r.Spec.Rules {
			rules = append(rules, rule.BackendRefs)
		}
		out = append(out, streamInput{
			HTTPRoute: streamShell(&gwapiv1.HTTPRoute{ObjectMeta: r.ObjectMeta}, r.Spec.CommonRouteSpec,
				r.Spec.Hostnames),
			kind: ir.KindTLSRoute, protocol: ir.ProtocolTLS, rules: rules,
		})
	}
	for _, r := range t.cfg.Cache.UDPRoutes() {
		rules := make([][]gwapiv1.BackendRef, 0, len(r.Spec.Rules))
		for _, rule := range r.Spec.Rules {
			rules = append(rules, rule.BackendRefs)
		}
		out = append(out, streamInput{
			HTTPRoute: streamShell(&gwapiv1.HTTPRoute{ObjectMeta: r.ObjectMeta}, r.Spec.CommonRouteSpec, nil),
			kind:      ir.KindUDPRoute, protocol: ir.ProtocolUDP, rules: rules,
		})
	}
	return out
}

// streamClaimKey addresses what a stream route claims on the listener the data plane binds,
// which is one per transport and port however many Gateway listeners share it: the whole
// listener for a TCP or UDP route, and one server name on it for a TLS route
func streamClaimKey(protocol string, port int, host string) string {
	return "stream\x00" + transport(protocol) + "\x00" + strconv.Itoa(port) + "\x00" + host
}

func (t *translator) buildStreamRoutes() {
	// the three kinds are ranked by age together: a TCP or UDP listener carries one route, and a
	// TLS listener one route per server name, so the older route keeps what both asked for
	for rank, in := range translate.ByAge(t.streamInputs()) {
		t.streamRoute(rank, in)
	}
}

func (t *translator) streamRoute(rank int, in streamInput) {
	src := translate.Source(in.kind, in.HTTPRoute)
	attachments, report := t.attachments(src, in.HTTPRoute)
	if report == nil {
		return
	}
	t.routeReports = append(t.routeReports, report)
	if len(attachments) == 0 {
		return
	}
	if len(in.rules) != 1 {
		// nothing in a stream selects among rules, so a route is one rule
		msg := fmt.Sprintf("a %s is served with exactly one rule; the route declares %d",
			in.kind, len(in.rules))
		t.reject(src, "%s; the route is not served", msg)
		report.refuseAll(gwapiv1.RouteReasonUnsupportedValue, msg)
		return
	}
	// one unit per port: listeners on one port merge onto one Trickster listener, so a route
	// claims each server name there once, however many Gateway listeners admitted it, and is
	// governed by the policy of the first Gateway that did
	type unit struct {
		listeners []string
		hosts     []string
		anyHost   bool
		policy    string
		protocol  string
	}
	var units []*unit
	byPort := make(map[int]*unit)
	var lost []string
	for _, a := range attachments {
		hosts := a.hostnames
		if len(hosts) == 0 {
			hosts = []string{""}
		}
		protocol := in.protocol
		if a.listener.protocol == ir.ProtocolMoQ {
			// served as MoQ Transport sessions, claimed per authority as a TLS route is per server name
			protocol = ir.ProtocolMoQ
		}
		var won []string
		for _, h := range hosts {
			key := streamClaimKey(protocol, a.listener.port, h)
			owner, taken := t.claims[key]
			switch {
			case taken && owner.Key() == src.Key():
				// already this route's through another listener on the port
				won = append(won, h)
				continue
			case taken:
				lost = append(lost, fmt.Sprintf("listener %q host %q is already served by %s",
					a.listener.section, h, owner.Key()))
				continue
			}
			t.claims[key] = src
			won = append(won, h)
		}
		if len(won) == 0 {
			continue
		}
		a.listener.attached++
		u, ok := byPort[a.listener.port]
		if !ok {
			u = &unit{policy: a.gateway.policy, protocol: protocol}
			byPort[a.listener.port] = u
			units = append(units, u)
		}
		u.listeners = append(u.listeners, a.listener.name)
		for _, h := range won {
			if h == "" {
				u.anyHost = true
				continue
			}
			u.hosts = append(u.hosts, hostnames.ToAnyDepth(h))
		}
	}
	for _, l := range lost {
		t.reject(src, "%s", l)
	}
	if len(units) == 0 {
		msg := "every listener the route names is already served by an older route"
		report.refuseAll(routeConflictReason, msg)
		return
	}
	// a stream carries no headers, so a class's geo ACL that reads them cannot gate it, and the route
	// is refused rather than served ungated
	units = slices.DeleteFunc(units, func(u *unit) bool {
		name := t.geoACLOf(u.policy)
		if name == "" || t.known.StreamGeoACLs.Contains(name) {
			return false
		}
		t.reject(src, "geo ACL %q judges HTTP requests only, so the route is not served on listener %q",
			name, u.listeners[0])
		return true
	})
	if len(units) == 0 {
		report.refuseAll(gwapiv1.RouteReasonUnsupportedValue,
			"the class's geo ACL judges HTTP requests only, and cannot gate a stream route")
		return
	}
	// a MoQ session carries no HTTP credentials, so a class's authenticator cannot guard it, and the route is
	// refused rather than served unauthenticated
	units = slices.DeleteFunc(units, func(u *unit) bool {
		name := t.authenticatorOf(u.policy)
		if u.protocol != ir.ProtocolMoQ || name == "" {
			return false
		}
		t.reject(src, "authenticator %q judges HTTP requests only, so the route is not served on MoQ listener %q",
			name, u.listeners[0])
		return true
	})
	if len(units) == 0 {
		report.refuseAll(gwapiv1.RouteReasonUnsupportedValue,
			"the class's authenticator judges HTTP requests only, and cannot guard a MoQ session")
		return
	}
	for i, u := range units {
		group := t.streamBackendGroup(src, i, in.HTTPRoute.Namespace, u.protocol, in.rules[0],
			report, t.routingModeOf(u.policy))
		t.model.Backends = append(t.model.Backends, group)
		governing := []string{u.policy}
		switch {
		case u.protocol == ir.ProtocolMoQ:
			governing = append(governing, t.moqPolicies(in, group)...)
		case u.protocol == ir.ProtocolUDP &&
			t.policies.Provider(translate.TargetUDPRoute, in.Namespace, in.Name, "") == providers.MoQ:
			t.reject(src, "provider %s is not applied on %s: a UDP listener relays datagrams, and only a %s "+
				"listener serves MoQ sessions", providers.MoQ, strings.Join(u.listeners, ", "), ir.GatewayProtocolMoQ)
		}
		r := ir.Route{
			Name:      src.Key() + "|" + u.listeners[0],
			Source:    src,
			Listeners: slices.Clone(u.listeners),
			Rank:      rank,
			Protocol:  u.protocol,
			Rules: []ir.Rule{{
				BackendGroup: group.Name,
				Policy:       t.policies.Bind(src, 0, nil, governing...),
			}},
		}
		if !u.anyHost {
			r.Hostnames = tstrings.Unique(u.hosts)
		}
		t.model.Routes = append(t.model.Routes, r)
	}
}

func (t *translator) moqPolicies(in streamInput, group ir.BackendGroup) []string {
	// a relay's cache settings come from the policy on its UDPRoute and, for a single origin, on that Service
	out := []string{t.policies.Attach(translate.TargetUDPRoute, in.Namespace, in.Name, "")}
	if len(group.Members) == 1 && !group.Members[0].Invalid {
		svc := group.Members[0].Service
		out = append(out, t.policies.Attach(translate.TargetService, svc.Namespace, svc.Name, ""))
		if svc.PortName != "" {
			out = append(out, t.policies.Attach(translate.TargetService, svc.Namespace, svc.Name, svc.PortName))
		}
	}
	return out
}

func (t *translator) streamBackendGroup(src ir.Source, ruleIndex int, namespace, protocol string,
	refs []gwapiv1.BackendRef, report *routeReport, mode string,
) ir.BackendGroup {
	// an unresolvable ref keeps its slot and weight as a member that refuses its connections, so
	// its share of the traffic is not shifted onto its siblings
	g := ir.BackendGroup{
		Name:      fmt.Sprintf("%s|r%d", src.Key(), ruleIndex),
		Source:    src,
		RuleIndex: ruleIndex,
	}
	scheme, transport := ir.ProtocolTCP, corev1.ProtocolTCP
	switch protocol {
	case ir.ProtocolUDP:
		scheme, transport = ir.ProtocolUDP, corev1.ProtocolUDP
	case ir.ProtocolMoQ:
		// a MoQ origin is reached by native QUIC on the Service's UDP port
		scheme, transport = schemeMoQT, corev1.ProtocolUDP
	}
	for j, ref := range refs {
		weight := 1
		if ref.Weight != nil {
			weight = int(*ref.Weight)
		}
		if weight <= 0 {
			continue
		}
		m := ir.BackendMember{RefIndex: j, Weight: weight}
		target, reason, why := t.resolveService(src.Kind, namespace, ref.BackendObjectReference,
			transport)
		if reason == "" {
			reason, why = unreachable(target, mode)
		}
		if reason != "" {
			m.Invalid = true
			m.InvalidReason = reason
			t.reject(src, "backendRef %d: %s", j, reason)
			report.unresolved(why, fmt.Sprintf("backendRef %d: %s", j, reason))
		} else {
			target.Scheme = scheme
			m.Service = target
		}
		g.Members = append(g.Members, m)
	}
	if len(g.Members) == 0 {
		reason := "rule has no backendRefs"
		if len(refs) > 0 {
			reason = "every backendRef has weight 0"
		}
		t.reject(src, "rule 0: %s; its connections are refused", reason)
		g.Members = []ir.BackendMember{{Weight: 1, Invalid: true, InvalidReason: reason}}
	}
	return g
}
