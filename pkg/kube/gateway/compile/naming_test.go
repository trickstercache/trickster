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
	"fmt"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

// src builds a Source for the naming helpers
func src(kind, ns, name string) ir.Source {
	return ir.Source{Kind: kind, Namespace: ns, Name: name}
}

// Names must be derivable from the Kubernetes object alone, so a restart
// regenerates exactly the configuration it had before
func TestNamesAreStableAndPrefixed(t *testing.T) {
	s := src("HTTPRoute", "shop", "web")
	names := []string{
		SourceName(s),
		RuleName(s, 0),
		MemberName(s, 0, 1),
		TemplateName(s, 0, 1),
		DiscovererName(),
		ListenerName(443, ir.ProtocolHTTPS),
	}
	for _, n := range names {
		require.Equal(t, Prefix, reserved.MatchNamePrefix(n), "name %q", n)
	}
	require.Equal(t, "kgw--httproute.shop.web", SourceName(s))
	require.Equal(t, "kgw--httproute.shop.web_r0", RuleName(s, 0))
	require.Equal(t, "kgw--httproute.shop.web_r0_b1", MemberName(s, 0, 1))
	require.Equal(t, "kgw--httproute.shop.web_r0_b1_tmpl", TemplateName(s, 0, 1))
	require.Equal(t, "kgw--discovery", DiscovererName())

	for range 4 {
		require.Equal(t, "kgw--httproute.shop.web_r2_b3", MemberName(s, 2, 3))
	}
}

// An Ingress and an HTTPRoute may legally share a namespace and a name. If
// the kind were not part of the identity, rule 0 of each would compile to
// one map key and whichever was processed last would overwrite the other.
func TestNamesDistinguishKind(t *testing.T) {
	ing := src("Ingress", "shop", "web")
	route := src("HTTPRoute", "shop", "web")
	require.NotEqual(t, SourceName(ing), SourceName(route))
	require.NotEqual(t, RuleName(ing, 0), RuleName(route, 0))
	require.NotEqual(t, MemberName(ing, 0, 0), MemberName(route, 0, 0))
	require.NotEqual(t, CacheKeyPrefix(ing), CacheKeyPrefix(route),
		"two kinds sharing a cache prefix would share cached objects")

	// kind is matched case-insensitively, so a translator setting "ingress"
	// and one setting "Ingress" name the same object
	require.Equal(t, SourceName(ing), SourceName(src("ingress", "shop", "web")))
}

// Two different Kubernetes objects must never generate the same name, or one
// would silently overwrite the other's configuration.
func TestNamesAreInjective(t *testing.T) {
	kinds := []string{"HTTPRoute", "Ingress"}
	objects := [][2]string{
		{"a-b", "c"},
		{"a", "b-c"},
		{"shop", "web"},
		{"shop", "web-r0"},
		{"shop", "web-r0-b0"},
		{"shop", "web-b0"},
		{"shop", "web.r0"},
	}
	indices := []int{0, 1, 10}

	// seen maps a generated name to the description of what produced it, so
	// a collision names both sides
	seen := make(map[string]string)
	claim := func(t *testing.T, name, by string) {
		t.Helper()
		if prev, dup := seen[name]; dup {
			require.Failf(t, "generated name collision",
				"%q produced by both %s and %s", name, prev, by)
		}
		seen[name] = by
	}
	for _, kind := range kinds {
		for _, o := range objects {
			s := src(kind, o[0], o[1])
			claim(t, SourceName(s), fmt.Sprintf("source(%+v)", s))
			for _, r := range indices {
				claim(t, RuleName(s, r), fmt.Sprintf("rule(%+v,%d)", s, r))
				for _, b := range indices {
					claim(t, MemberName(s, r, b),
						fmt.Sprintf("member(%+v,%d,%d)", s, r, b))
					claim(t, TemplateName(s, r, b),
						fmt.Sprintf("template(%+v,%d,%d)", s, r, b))
				}
			}
		}
	}
	require.Len(t, seen,
		len(kinds)*len(objects)*(1+len(indices)+2*len(indices)*len(indices)),
		"every input should have produced a distinct name")
}

// Listener names distinguish protocol as well as port, because an http and
// an https listener on one port are two Trickster listeners
func TestListenerNames(t *testing.T) {
	require.Equal(t, "kgw--listener-http-80", ListenerName(80, ir.ProtocolHTTP))
	require.Equal(t, "kgw--listener-https-443", ListenerName(443, ir.ProtocolHTTPS))
	require.NotEqual(t, ListenerName(8443, ir.ProtocolHTTP),
		ListenerName(8443, ir.ProtocolHTTPS))
}

// The cache key prefix deliberately excludes the rule index: rules are
// positional, so a prefix carrying the index would dump the cached objects
// of every rule below an insertion
func TestCacheKeyPrefixSurvivesRuleEdits(t *testing.T) {
	s := src("HTTPRoute", "shop", "web")
	before := CacheKeyPrefix(s)
	require.Equal(t, "httproute.shop.web", before)
	require.Equal(t, before, CacheKeyPrefix(s))
	require.NotEqual(t, before, CacheKeyPrefix(src("HTTPRoute", "shop", "other")))
	require.NotEqual(t, before, CacheKeyPrefix(src("HTTPRoute", "other", "web")))

	// two routes of the same name in different namespaces stay distinct
	require.NotEqual(t, CacheKeyPrefix(src("HTTPRoute", "a", "b")),
		CacheKeyPrefix(src("HTTPRoute", "b", "a")))
}

// The IR-shaped helpers must agree with the primitive ones, so a translator
// and the compiler cannot drift apart
func TestGroupNameHelpers(t *testing.T) {
	s := src("HTTPRoute", "shop", "web")
	g := ir.BackendGroup{Source: s, RuleIndex: 2}
	m := ir.BackendMember{RefIndex: 3}
	require.Equal(t, RuleName(s, 2), GroupName(g))
	require.Equal(t, MemberName(s, 2, 3), GroupMemberName(g, m))
}

// A generated name is a Trickster object name; it must stay within what a
// Kubernetes object can produce and carry no characters that would need
// quoting in YAML
func TestNamesAreYAMLSafe(t *testing.T) {
	n := MemberName(src("HTTPRoute", "a-namespace", "a.route-name"), 12, 34)
	require.NotContains(t, n, " ")
	require.NotContains(t, n, ":")
	for _, r := range strings.TrimPrefix(n, Prefix) {
		require.True(t,
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
				r == '-' || r == '.' || r == '_',
			"unexpected character %q in %q", r, n)
	}
}
