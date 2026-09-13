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

package gateway

import (
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Kinds a reference may name
const (
	kindGateway   = "Gateway"
	kindHTTPRoute = "HTTPRoute"
	kindGRPCRoute = "GRPCRoute"
	kindTCPRoute  = "TCPRoute"
	kindTLSRoute  = "TLSRoute"
	kindUDPRoute  = "UDPRoute"
	kindService   = "Service"
	kindSecret    = "Secret"
	kindConfigMap = "ConfigMap"
)

// grantIndex answers whether a cross-namespace reference is permitted by a
// ReferenceGrant in the referenced namespace
type grantIndex struct {
	byNamespace map[string][]*gwapiv1.ReferenceGrant
}

func indexGrants(grants []*gwapiv1.ReferenceGrant) *grantIndex {
	g := &grantIndex{byNamespace: make(map[string][]*gwapiv1.ReferenceGrant)}
	for _, grant := range grants {
		g.byNamespace[grant.Namespace] = append(g.byNamespace[grant.Namespace], grant)
	}
	return g
}

// reference is one side of a cross-namespace reference
type reference struct {
	group     string
	kind      string
	namespace string
	name      string
}

func (g *grantIndex) permits(from, to reference) bool {
	// a to entry naming no object permits its whole kind
	for _, grant := range g.byNamespace[to.namespace] {
		var fromOK bool
		for _, f := range grant.Spec.From {
			if string(f.Group) == from.group && string(f.Kind) == from.kind &&
				string(f.Namespace) == from.namespace {
				fromOK = true
				break
			}
		}
		if !fromOK {
			continue
		}
		for _, entry := range grant.Spec.To {
			if string(entry.Group) != to.group || string(entry.Kind) != to.kind {
				continue
			}
			if entry.Name == nil || *entry.Name == "" || string(*entry.Name) == to.name {
				return true
			}
		}
	}
	return false
}
