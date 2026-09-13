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

package ir

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func src(kind, ns, name string) Source {
	return Source{Kind: kind, Namespace: ns, Name: name}
}

// sample builds a populated IR; the shuffled flag emits every collection in
// a different order, which is what a watch cache does between rebuilds
func sample(shuffled bool) *IR {
	route := Route{
		Name:      "r1",
		Source:    src("HTTPRoute", "shop", "web"),
		Hostnames: []string{"a.example.com", "b.example.com"},
		Listeners: []string{"l-80", "l-443"},
		Rules: []Rule{{
			Matches: []Match{{
				Path:        PathMatch{Type: PathPrefix, Value: "/"},
				Methods:     []string{"GET", "POST"},
				Headers:     []KeyValueMatch{{Name: "x-a", Value: "1"}, {Name: "x-b", Value: "2"}},
				QueryParams: []KeyValueMatch{{Name: "q", Value: "v"}},
			}},
			BackendGroup: "g1",
		}},
	}
	group := BackendGroup{
		Name: "g1", Source: route.Source,
		Members: []BackendMember{
			{RefIndex: 0, Weight: 3, Service: ServiceTarget{
				Namespace: "shop", Name: "web", Port: 80,
			}},
			{RefIndex: 1, Weight: 1, Service: ServiceTarget{
				Namespace: "shop", Name: "canary", Port: 80,
			}},
		},
	}
	listeners := []Listener{
		{Name: "l-80", Port: 80, Protocol: ProtocolHTTP, Source: src("Gateway", "infra", "gw")},
		{
			Name: "l-443", Port: 443, Protocol: ProtocolHTTPS,
			CertRefs: []string{"c1", "c2"}, Source: src("Gateway", "infra", "gw"),
		},
	}
	certs := []CertRef{
		{Name: "c1", Namespace: "infra", SecretName: "a-tls", Source: src("Gateway", "infra", "gw")},
		{Name: "c2", Namespace: "infra", SecretName: "b-tls", Source: src("Gateway", "infra", "gw")},
	}
	if shuffled {
		listeners[0], listeners[1] = listeners[1], listeners[0]
		certs[0], certs[1] = certs[1], certs[0]
		route.Hostnames = []string{"b.example.com", "a.example.com"}
		route.Listeners = []string{"l-443", "l-80"}
		m := &route.Rules[0].Matches[0]
		m.Methods = []string{"POST", "GET"}
		m.Headers = []KeyValueMatch{{Name: "x-b", Value: "2"}, {Name: "x-a", Value: "1"}}
		group.Members[0], group.Members[1] = group.Members[1], group.Members[0]
	}
	return &IR{
		Listeners: listeners,
		Routes:    []Route{route},
		Backends:  []BackendGroup{group},
		Certs:     certs,
	}
}

// The hash exists so a watch event that changes nothing observable costs a
// comparison instead of a reload; cache ordering must therefore not affect it
func TestHashIsOrderIndependent(t *testing.T) {
	require.Equal(t, sample(false).Hash(), sample(true).Hash())
	require.Equal(t, sample(false).Hash(), sample(false).Hash())
}

// A generation bump with no spec change is exactly the churn the hash exists
// to absorb
func TestHashIgnoresGenerations(t *testing.T) {
	a, b := sample(false), sample(false)
	for i := range b.Routes {
		b.Routes[i].Source.Generation = 7
	}
	for i := range b.Listeners {
		b.Listeners[i].Source.Generation = 9
	}
	require.Equal(t, a.Hash(), b.Hash())
	require.EqualValues(t, 7, b.Routes[0].Source.Generation,
		"hashing must not mutate the caller's IR")
}

// Anything the compiler reads must change the hash, or a real change would
// be dropped as a no-op
func TestHashChangesWithContent(t *testing.T) {
	base := sample(false).Hash()
	cases := map[string]func(*IR){
		"hostname":     func(i *IR) { i.Routes[0].Hostnames[0] = "c.example.com" },
		"path":         func(i *IR) { i.Routes[0].Rules[0].Matches[0].Path.Value = "/other" },
		"path type":    func(i *IR) { i.Routes[0].Rules[0].Matches[0].Path.Type = PathExact },
		"method":       func(i *IR) { i.Routes[0].Rules[0].Matches[0].Methods = []string{"GET"} },
		"header":       func(i *IR) { i.Routes[0].Rules[0].Matches[0].Headers[0].Value = "9" },
		"weight":       func(i *IR) { i.Backends[0].Members[0].Weight = 5 },
		"service port": func(i *IR) { i.Backends[0].Members[0].Service.Port = 8080 },
		"invalid ref":  func(i *IR) { i.Backends[0].Members[0].Invalid = true },
		"listener":     func(i *IR) { i.Listeners[0].Port = 8443 },
		"cert secret":  func(i *IR) { i.Certs[0].SecretName = "other-tls" },
		"policy":       func(i *IR) { i.Policies = []Policy{{Name: "p", CacheName: "c"}} },
		"rule filter": func(i *IR) {
			i.Routes[0].Rules[0].Filters = []Filter{{
				Type:           FilterRequestHeaders,
				RequestHeaders: &HeaderFilter{Set: []Header{{Name: "X-A", Value: "1"}}},
			}}
		},
		"member filter": func(i *IR) {
			i.Backends[0].Members[0].Filters = []Filter{{
				Type:       FilterURLRewrite,
				URLRewrite: &URLRewriteFilter{Hostname: "x.internal"},
			}}
		},
		"member tls": func(i *IR) {
			i.Backends[0].Members[0].TLS = &BackendTLS{Hostname: "x.internal", System: true}
		},
		"member tls ca": func(i *IR) {
			i.Backends[0].Members[0].TLS = &BackendTLS{
				Hostname:       "x.internal",
				CACertificates: []string{"pem"},
			}
		},
		"rule order": func(i *IR) {
			i.Routes[0].Rules = append(i.Routes[0].Rules,
				Rule{BackendGroup: "g1"})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := sample(false)
			mutate(m)
			require.NotEqual(t, base, m.Hash())
		})
	}
}

// Rule order is the source's precedence, so it is content, not noise
func TestCanonicalPreservesRuleOrder(t *testing.T) {
	m := sample(false)
	m.Routes[0].Rules = []Rule{
		{BackendGroup: "b", Matches: []Match{{Path: PathMatch{Type: PathExact, Value: "/z"}}}},
		{BackendGroup: "a", Matches: []Match{{Path: PathMatch{Type: PathExact, Value: "/a"}}}},
	}
	c := m.Canonical()
	require.Equal(t, "/z", c.Routes[0].Rules[0].Matches[0].Path.Value)
	require.Equal(t, "/a", c.Routes[0].Rules[1].Matches[0].Path.Value)
}

// Canonical must not alias the caller's slices, or sorting one IR would
// reorder another that shares backing arrays
func TestCanonicalDoesNotMutateInput(t *testing.T) {
	m := sample(true)
	before := m.Listeners[0].Name
	beforeHost := m.Routes[0].Hostnames[0]
	beforeMember := m.Backends[0].Members[0].RefIndex
	c := m.Canonical()
	require.Equal(t, before, m.Listeners[0].Name)
	require.Equal(t, beforeHost, m.Routes[0].Hostnames[0])
	require.Equal(t, beforeMember, m.Backends[0].Members[0].RefIndex)

	c.Routes[0].Hostnames[0] = "mutated"
	require.NotEqual(t, "mutated", m.Routes[0].Hostnames[0])
	require.Nil(t, (*IR)(nil).Canonical())
}

// An absent collection and an empty one describe the same data plane
// Filters and backend TLS carry nested state; the canonical form must copy
// it rather than alias it, or hashing would read the caller's mutations
func TestCanonicalClonesFiltersAndTLS(t *testing.T) {
	m := sample(false)
	m.Routes[0].Rules[0].Filters = []Filter{
		{Type: FilterRequestHeaders, RequestHeaders: &HeaderFilter{
			Set: []Header{{Name: "X-A", Value: "1"}}, Add: []Header{{Name: "X-B", Value: "2"}},
			Remove: []string{"X-C"},
		}},
		{Type: FilterResponseHeaders, ResponseHeaders: &HeaderFilter{Remove: []string{"Server"}}},
		{Type: FilterRedirect, Redirect: &RedirectFilter{
			Scheme: "https",
			Path:   &PathModifier{Type: PathReplaceFull, Value: "/x"}, StatusCode: 301,
		}},
		{Type: FilterURLRewrite, URLRewrite: &URLRewriteFilter{
			Hostname: "h",
			Path:     &PathModifier{Type: PathReplacePrefix, Value: "/y"},
		}},
	}
	m.Backends[0].Members[0].TLS = &BackendTLS{Hostname: "h", CACertificates: []string{"a"}}
	c := m.Canonical()
	require.Equal(t, m.Routes[0].Rules[0].Filters, c.Routes[0].Rules[0].Filters)
	require.Equal(t, m.Backends[0].Members[0].TLS, c.Backends[0].Members[0].TLS)

	c.Routes[0].Rules[0].Filters[0].RequestHeaders.Set[0].Value = "changed"
	c.Routes[0].Rules[0].Filters[2].Redirect.Path.Value = "/changed"
	c.Routes[0].Rules[0].Filters[3].URLRewrite.Path.Value = "/changed"
	c.Backends[0].Members[0].TLS.CACertificates[0] = "changed"
	require.Equal(t, "1", m.Routes[0].Rules[0].Filters[0].RequestHeaders.Set[0].Value)
	require.Equal(t, "/x", m.Routes[0].Rules[0].Filters[2].Redirect.Path.Value)
	require.Equal(t, "/y", m.Routes[0].Rules[0].Filters[3].URLRewrite.Path.Value)
	require.Equal(t, "a", m.Backends[0].Members[0].TLS.CACertificates[0])

	require.Nil(t, Rule{}.Redirect(), "a rule with no filters forwards")
	require.Nil(t, Rule{Filters: []Filter{{Type: FilterRedirect}}}.Redirect(),
		"a redirect filter with no body is not a redirect")
	require.Equal(t, 301, m.Routes[0].Rules[0].Redirect().StatusCode)
}

func TestEmptyAndNilHashAlike(t *testing.T) {
	var nilIR *IR
	require.Equal(t, (&IR{}).Hash(), nilIR.Hash())
	require.True(t, nilIR.IsEmpty())
	require.True(t, (&IR{}).IsEmpty())
	require.False(t, sample(false).IsEmpty())

	a := &IR{Routes: []Route{{Name: "r", Hostnames: nil}}}
	b := &IR{Routes: []Route{{Name: "r", Hostnames: []string{}}}}
	require.Equal(t, a.Hash(), b.Hash())
}

// Status is written back per object, so the sources must be de-duplicated
// and stably ordered
func TestSources(t *testing.T) {
	got := sample(false).Sources()
	require.Len(t, got, 2)
	require.Equal(t, "Gateway/infra/gw", got[0].Key())
	require.Equal(t, "HTTPRoute/shop/web", got[1].Key())
	require.Nil(t, (*IR)(nil).Sources())

	// the same object reached through several collections appears once
	one := &IR{
		Listeners: []Listener{{Name: "l", Source: src("Gateway", "n", "g")}},
		Certs:     []CertRef{{Name: "c", Source: src("Gateway", "n", "g")}},
	}
	require.Len(t, one.Sources(), 1)
}

func TestCertRefKey(t *testing.T) {
	require.Equal(t, "infra/a-tls",
		CertRef{Namespace: "infra", SecretName: "a-tls"}.Key())
}

// An unhashable IR cannot arise from these types, but the reconcile loop
// must not panic if one ever does
func TestSourceCompareOrdersByKindThenName(t *testing.T) {
	require.Negative(t, src("Gateway", "a", "b").compare(src("HTTPRoute", "a", "b")))
	require.Negative(t, src("Gateway", "a", "b").compare(src("Gateway", "b", "a")))
	require.Negative(t, src("Gateway", "a", "a").compare(src("Gateway", "a", "b")))
	require.Zero(t, src("Gateway", "a", "b").compare(src("Gateway", "a", "b")))
}

// Every collection is sorted by identity, so two watch caches that yielded
// their objects in different orders produce the same canonical form
func TestCanonicalSortsEveryCollection(t *testing.T) {
	m := &IR{
		Listeners: []Listener{{Name: "z"}, {Name: "a"}},
		Routes:    []Route{{Name: "z"}, {Name: "a"}},
		Backends:  []BackendGroup{{Name: "z"}, {Name: "a"}},
		Certs:     []CertRef{{Name: "z"}, {Name: "a"}},
		Policies:  []Policy{{Name: "z"}, {Name: "a"}},
	}
	c := m.Canonical()
	require.Equal(t, "a", c.Listeners[0].Name)
	require.Equal(t, "a", c.Routes[0].Name)
	require.Equal(t, "a", c.Backends[0].Name)
	require.Equal(t, "a", c.Certs[0].Name)
	require.Equal(t, "a", c.Policies[0].Name)

	reversed := &IR{
		Listeners: []Listener{{Name: "a"}, {Name: "z"}},
		Routes:    []Route{{Name: "a"}, {Name: "z"}},
		Backends:  []BackendGroup{{Name: "a"}, {Name: "z"}},
		Certs:     []CertRef{{Name: "a"}, {Name: "z"}},
		Policies:  []Policy{{Name: "a"}, {Name: "z"}},
	}
	require.Equal(t, reversed.Hash(), m.Hash())
}

// Repeated headers of one name are ordered by value, so a route declaring
// the same header twice still hashes stably
func TestCanonicalSortsRepeatedHeaderNamesByValue(t *testing.T) {
	mk := func(first, second string) *IR {
		return &IR{Routes: []Route{{Name: "r", Rules: []Rule{{
			Matches: []Match{{
				Path: PathMatch{Type: PathPrefix, Value: "/"},
				Headers: []KeyValueMatch{
					{Name: "x", Value: first}, {Name: "x", Value: second},
				},
			}},
		}}}}}
	}
	require.Equal(t, mk("1", "2").Hash(), mk("2", "1").Hash())
	c := mk("2", "1").Canonical()
	require.Equal(t, "1", c.Routes[0].Rules[0].Matches[0].Headers[0].Value)
}

func TestNormalizePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"/foo": "/foo", "/foo/": "/foo", "/a/b/": "/a/b", "/": "/", "": "/",
	} {
		require.Equal(t, want, NormalizePrefix(in), in)
	}
}

func TestCloneIsDeep(t *testing.T) {
	f := Filter{
		Type:            FilterRequestHeaders,
		RequestHeaders:  &HeaderFilter{Set: []Header{{Name: "X-A", Value: "1"}}, Remove: []string{"X-B"}},
		ResponseHeaders: &HeaderFilter{Add: []Header{{Name: "X-C", Value: "2"}}},
		Redirect:        &RedirectFilter{Scheme: "https", Path: &PathModifier{Type: PathReplaceFull, Value: "/x"}},
		URLRewrite:      &URLRewriteFilter{Hostname: "h", Path: &PathModifier{Type: PathReplacePrefix, Value: "/y"}},
	}
	c := f.Clone()
	require.Equal(t, f, c)
	c.RequestHeaders.Set[0].Value = "changed"
	c.RequestHeaders.Remove[0] = "changed"
	c.ResponseHeaders.Add[0].Value = "changed"
	c.Redirect.Path.Value = "changed"
	c.URLRewrite.Path.Value = "changed"
	require.Equal(t, "1", f.RequestHeaders.Set[0].Value)
	require.Equal(t, "X-B", f.RequestHeaders.Remove[0])
	require.Equal(t, "2", f.ResponseHeaders.Add[0].Value)
	require.Equal(t, "/x", f.Redirect.Path.Value)
	require.Equal(t, "/y", f.URLRewrite.Path.Value)
	require.Equal(t, Filter{Type: FilterRedirect}, Filter{Type: FilterRedirect}.Clone())
	require.Nil(t, CloneFilters(nil))

	p := Policy{
		Name: "p", RequestHeaders: map[string]string{"X-A": "1"},
		ResponseHeaders: map[string]string{"X-B": "2"}, CORSHeaders: map[string]string{"X-C": "3"},
	}
	pc := p.Clone()
	require.Equal(t, p, pc)
	pc.RequestHeaders["X-A"], pc.ResponseHeaders["X-B"], pc.CORSHeaders["X-C"] = "x", "x", "x"
	require.Equal(t, "1", p.RequestHeaders["X-A"])
	require.Equal(t, "2", p.ResponseHeaders["X-B"])
	require.Equal(t, "3", p.CORSHeaders["X-C"])

	tls := &BackendTLS{Hostname: "h", CACertificates: []string{"a"}}
	tc := tls.Clone()
	require.Equal(t, tls, tc)
	tc.CACertificates[0] = "b"
	require.Equal(t, "a", tls.CACertificates[0])
	var none *BackendTLS
	require.Nil(t, none.Clone())
}

// Merge is a plain union: each translator names its objects from the kinds
// they came from, so nothing collides and nothing is deduplicated
func TestMerge(t *testing.T) {
	a := sample(false)
	b := &IR{
		Routes:   []Route{{Name: "ingress-route", Source: src("Ingress", "shop", "web")}},
		Policies: []Policy{{Name: "p"}},
	}
	m := Merge(a, b)
	require.Len(t, m.Routes, 2)
	require.Len(t, m.Listeners, 2)
	require.Len(t, m.Backends, 1)
	require.Len(t, m.Certs, 2)
	require.Len(t, m.Policies, 1)
	require.Len(t, a.Routes, 1, "merging must not mutate its inputs")

	require.Equal(t, a, Merge(a, nil))
	require.Len(t, Merge(nil, b).Routes, 1)
	require.True(t, Merge(nil, nil).IsEmpty())
}

func TestProblemString(t *testing.T) {
	p := Problem{Source: src("HTTPRoute", "shop", "web"), Detail: "backendRef 0: not found"}
	require.Equal(t, "HTTPRoute/shop/web: backendRef 0: not found", p.String())
}

func TestIsStream(t *testing.T) {
	for _, p := range []string{ProtocolTCP, ProtocolTLS, ProtocolUDP} {
		if !IsStream(p) {
			t.Errorf("IsStream(%q) = false", p)
		}
	}
	for _, p := range []string{ProtocolHTTP, ProtocolHTTPS, ProtocolGRPC, ""} {
		if IsStream(p) {
			t.Errorf("IsStream(%q) = true", p)
		}
	}
}
