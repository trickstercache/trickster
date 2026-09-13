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
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
)

// Prefix is the reserved name prefix every generated object carries; file configuration may not
// use it and the overlay may not omit it, so generated and hand-written objects never collide
const Prefix = reserved.NamePrefixKubeGateway

// Generated names append suffixes to the source's identity, kgw--<kind>.<namespace>.<name>; the kind
// leads because an Ingress and an HTTPRoute may share a name, and no Kubernetes name has an underscore.
const (
	ruleInfix      = "_r"
	memberInfix    = "_b"
	templateSuffix = "_tmpl"
	rewriterInfix  = "_w"
	mirrorSuffix   = "_mirror"
	// placeholderSuffix names the backend that binds a listener no route reaches yet
	placeholderSuffix = "_none"
)

// PlaceholderName is the generated name of the backend that binds a listener no route reaches
func PlaceholderName(listener string) string {
	return listener + placeholderSuffix
}

// SourceName is the generated name for an object derived from a Kubernetes
// object as a whole
func SourceName(s ir.Source) string {
	kind := strings.ToLower(s.Kind)
	var sb strings.Builder
	sb.Grow(len(Prefix) + len(kind) + len(s.Namespace) + len(s.Name) + 2)
	sb.WriteString(Prefix)
	sb.WriteString(kind)
	sb.WriteByte('.')
	sb.WriteString(s.Namespace)
	sb.WriteByte('.')
	sb.WriteString(s.Name)
	return sb.String()
}

// RuleName is the generated name for a route rule's terminal backend
func RuleName(s ir.Source, ruleIndex int) string {
	return SourceName(s) + ruleInfix + strconv.Itoa(ruleIndex)
}

// MemberName is the generated name for one backendRef of a route rule
func MemberName(s ir.Source, ruleIndex, refIndex int) string {
	return RuleName(s, ruleIndex) + memberInfix + strconv.Itoa(refIndex)
}

// TemplateName is the generated name of the is_template backend an
// endpoint-mode rule clones per discovered member
func TemplateName(s ir.Source, ruleIndex, refIndex int) string {
	return MemberName(s, ruleIndex, refIndex) + templateSuffix
}

// DiscovererName is the generated name of the discovery entry an endpoint-mode member watches;
// one entry per connection is enough, so every generated ALB shares it
func DiscovererName() string {
	return Prefix + "discovery"
}

// GroupName returns the generated terminal backend name for a backend group
func GroupName(g ir.BackendGroup) string {
	return RuleName(g.Source, g.RuleIndex)
}

// GroupMemberName returns the generated backend name for one group member
func GroupMemberName(g ir.BackendGroup, m ir.BackendMember) string {
	return MemberName(g.Source, g.RuleIndex, m.RefIndex)
}

// CacheKeyPrefix is the cache key namespace for objects a route caches: the source's identity without
// the rule index, since inserting a rule shifts later indices and would invalidate their cached objects
func CacheKeyPrefix(s ir.Source) string {
	return strings.ToLower(s.Kind) + "." + s.Namespace + "." + s.Name
}

// RewriterName is the generated name of the request rewriter serving one match of a rule; a
// rewriter depends on the path it matched, so there is one per match rather than per rule
func RewriterName(g ir.BackendGroup, matchIndex int) string {
	return GroupName(g) + rewriterInfix + strconv.Itoa(matchIndex)
}

// MemberRewriterName is the generated name of the request rewriter one pool
// member's own filters need, on the catch-all path the ALB dispatches to
func MemberRewriterName(g ir.BackendGroup, m ir.BackendMember) string {
	return GroupMemberName(g, m) + rewriterInfix
}

// MirrorName is the generated name of the backend that receives a rule's mirrored requests
func MirrorName(g ir.BackendGroup) string {
	return GroupName(g) + mirrorSuffix
}

// MemberMirrorName is the generated name of the backend that receives one pool member's
// mirrored requests
func MemberMirrorName(g ir.BackendGroup, m ir.BackendMember) string {
	return GroupMemberName(g, m) + mirrorSuffix
}
