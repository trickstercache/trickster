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
	"fmt"

	albnames "github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
)

// unresolvedStreamOriginURL is the origin a stream member that could not be resolved carries: a
// name that never resolves, so every connection its share receives is refused after a failed dial
const unresolvedStreamOriginURL = "tcp://unresolved.kgw.invalid:1"

// splitRoutes separates the routes stream listeners relay from those the HTTP planner places
func splitRoutes(routes []ir.Route) (http, stream []ir.Route) {
	for _, r := range routes {
		if ir.IsStream(r.Protocol) {
			stream = append(stream, r)
			continue
		}
		http = append(http, r)
	}
	return http, stream
}

func compileStreamRoutes(doc *document, routes []ir.Route, idx index, opts *kubecfg.Options) error {
	// a stream route is one rule relayed whole: one backend to dial, or a pool of them, bound to
	// the route's listeners and, on a tls listener, its server names
	if len(routes) == 0 {
		return nil
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
		if err := compileStreamRule(doc, r, group, listeners, eff, opts); err != nil {
			return err
		}
	}
	return nil
}

func compileStreamRule(doc *document, r ir.Route, group ir.BackendGroup, listeners []string,
	eff effective, opts *kubecfg.Options,
) error {
	name := GroupName(group)
	attach := func(b *backendDoc) {
		b.ListenerNames = listeners
		b.PathRoutingDisabled = true
		b.PathDefaultsDisabled = true
		b.Hosts = r.Hostnames
		b.AnyHostRouting = len(r.Hostnames) == 0
	}
	if len(group.Members) == 1 && !group.Members[0].Invalid {
		front, err := streamMember(doc, r, group, group.Members[0], eff, opts, listeners)
		if err != nil {
			return err
		}
		attach(front)
		doc.Backends[name] = front
		return nil
	}
	pool := make([]*albPoolDoc, 0, len(group.Members))
	for _, m := range group.Members {
		memberName := GroupMemberName(group, m)
		var front *backendDoc
		if m.Invalid {
			front = unresolvedStreamBackend()
		} else {
			var err error
			if front, err = streamMember(doc, r, group, m, eff, opts, listeners); err != nil {
				return err
			}
		}
		// a pool member registers no route of its own; it carries the pool's listener names so
		// validation does not bind it to the default frontend
		front.ListenerNames = listeners
		front.PathRoutingDisabled = true
		front.PathDefaultsDisabled = true
		doc.Backends[memberName] = front
		pool = append(pool, &albPoolDoc{Name: memberName, Weight: m.Weight})
	}
	alb := &backendDoc{Provider: providers.ALB, ALB: &albDoc{Mechanism: albnames.MechanismRR, Pool: pool}}
	attach(alb)
	doc.Backends[name] = alb
	return nil
}

func streamMember(doc *document, r ir.Route, g ir.BackendGroup, m ir.BackendMember, eff effective,
	opts *kubecfg.Options, listeners []string,
) (*backendDoc, error) {
	// the member is a reverse proxy backend for its origin alone: the stream listener dials the
	// origin's host and port and reads nothing, so no cache, handler or path applies
	origin := &backendDoc{
		Provider:  providers.ReverseProxyShort,
		OriginURL: ServiceURL(m.Service),
	}
	switch eff.routingMode {
	case kubecfg.RoutingModeService:
		return origin, nil
	case kubecfg.RoutingModeEndpoint:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedRoutingMode, eff.routingMode)
	}
	// the Service's ready endpoints are relayed to directly, each cloned from a template; their
	// readiness is the EndpointSlice's, since no probe speaks the protocol they carry
	origin.OriginURL = ""
	origin.IsTemplate = true
	origin.PathRoutingDisabled = true
	origin.PathDefaultsDisabled = true
	tmplName := TemplateName(g.Source, g.RuleIndex, m.RefIndex)
	doc.Backends[tmplName] = origin
	scheme := do.SchemeTCP
	if m.Service.Scheme == ir.ProtocolUDP {
		scheme = do.SchemeUDP
	}
	return &backendDoc{
		Provider:      providers.ALB,
		ListenerNames: listeners,
		// the endpoints are balanced by the policy's mechanism; a key must be one the route's
		// listener can read, which is the client address, or the server name on a tls route
		ALB: eff.endpointALB(&albDiscoveryDoc{
			DiscovererName:  doc.discoverer(opts),
			TemplateBackend: tmplName,
			HealthMode:      ao.HealthModeProvider,
			Query: &queryDoc{
				Kind: do.KindEndpointSlices, Namespace: m.Service.Namespace,
				Service: m.Service.Name, Port: m.Service.PortName, Scheme: scheme,
			},
		}, func(ks ao.KeySource) bool {
			return ks.OnStream(ao.StreamListener{TLS: r.Protocol == ir.ProtocolTLS})
		}),
	}, nil
}

func unresolvedStreamBackend() *backendDoc {
	return &backendDoc{
		Provider:  providers.ReverseProxyShort,
		OriginURL: unresolvedStreamOriginURL,
	}
}
