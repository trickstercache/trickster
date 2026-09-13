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

package translate

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
)

func TestPoliciesMerge(t *testing.T) {
	model := &ir.IR{}
	m := NewPolicies(model, nil, nil)
	class := ir.Policy{Name: "GatewayClass//gc", Source: ir.Source{Kind: ir.KindGatewayClass},
		CacheName: "objects", AuthenticatorName: "auth"}
	route := ir.Policy{Name: "TricksterCachePolicy/shop/r", Source: ir.Source{Kind: ir.KindCachePolicy,
		Name: "r"}, TimeoutMS: 2000, Provider: "prometheus"}
	require.Equal(t, class.Name, m.Add(class))
	require.Equal(t, class.Name, m.Add(class), "a name is added once")
	require.Len(t, model.Policies, 1)
	m.Add(route)

	// one name is returned as is, empty and unknown names are skipped
	name, p := m.Merge("", class.Name, "absent")
	require.Equal(t, class.Name, name)
	require.Equal(t, "objects", p.CacheName)
	name, p = m.Merge()
	require.Empty(t, name)
	require.Nil(t, p)

	name, p = m.Merge(class.Name, route.Name)
	require.Equal(t, class.Name+"+"+route.Name, name)
	require.Equal(t, "objects", p.CacheName)
	require.Equal(t, "auth", p.AuthenticatorName)
	require.Equal(t, int64(2000), p.TimeoutMS)
	require.Equal(t, "r", p.Source.Name, "the most specific part is the source")
	require.Len(t, model.Policies, 3)
	again, _ := m.Merge(class.Name, route.Name)
	require.Equal(t, name, again)
	require.Len(t, model.Policies, 3, "a combination is minted once")
	require.Same(t, m.Get(name), p)
	require.Nil(t, m.Get("absent"))

	// the provider is withheld into a distinct policy, once, and one without is itself
	stripped, sp := m.WithoutProvider(name)
	require.Equal(t, name+"+noprovider", stripped)
	require.Empty(t, sp.Provider)
	require.Equal(t, int64(2000), sp.TimeoutMS)
	require.Equal(t, "prometheus", m.Get(name).Provider, "the original is untouched")
	require.Len(t, model.Policies, 4)
	m.WithoutProvider(name)
	require.Len(t, model.Policies, 4)
	same, cp := m.WithoutProvider(class.Name)
	require.Equal(t, class.Name, same)
	require.Same(t, m.Get(class.Name), cp)
	none, np := m.WithoutProvider("absent")
	require.Equal(t, "absent", none)
	require.Nil(t, np)
}

func TestPolicyValueParsers(t *testing.T) {
	for _, v := range []string{"prometheus", "influxdb", "clickhouse", "graphite"} {
		p, err := Provider(v)
		require.NoError(t, err)
		require.Equal(t, v, p)
	}
	_, err := Provider("rpc")
	require.ErrorContains(t, err, "time series provider")
	_, err = Provider("mysql")
	require.ErrorContains(t, err, "time series provider",
		"mysql is served over its own protocol and predefines no HTTP paths")

	r, err := ResultHeader("Hide")
	require.NoError(t, err)
	require.Equal(t, ir.ResultHeaderHide, r)
	r, err = ResultHeader("expose")
	require.NoError(t, err)
	require.Equal(t, ir.ResultHeaderExpose, r)
	_, err = ResultHeader("maybe")
	require.Error(t, err)

	h, err := HeaderMap(map[string]string{" X-A ": " 1 ", "-X-B": "", "+X-C": "2"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"X-A": "1", "-X-B": "", "+X-C": "2"}, h)
	none, err := HeaderMap(nil)
	require.NoError(t, err)
	require.Nil(t, none)
	_, err = HeaderMap(map[string]string{"bad name": "1"})
	require.ErrorContains(t, err, "not a valid header name")
	_, err = HeaderMap(map[string]string{"X-A": "bad\x00value"})
	require.ErrorContains(t, err, "not a valid value")
	_, err = HeaderMap(map[string]string{"X-A": "1", "-x-a": ""})
	require.ErrorContains(t, err, "named more than once",
		"a map is applied in no order, so one header may be named once")

	names, err := HeaderNames([]string{" X-Tenant ", "Accept"})
	require.NoError(t, err)
	require.Equal(t, []string{"X-Tenant", "Accept"}, names)
	_, err = HeaderNames([]string{"not valid"})
	require.ErrorContains(t, err, "header name")
	_, err = HeaderNames([]string{""})
	require.ErrorContains(t, err, "empty")
	empty, err := HeaderNames(nil)
	require.NoError(t, err)
	require.Nil(t, empty)
	cleared, err := HeaderNames([]string{})
	require.NoError(t, err)
	require.NotNil(t, cleared, "an explicitly empty list clears what was inherited")
	require.Empty(t, cleared)

	params, err := ParamNames([]string{"query", " step "})
	require.NoError(t, err)
	require.Equal(t, []string{"query", "step"}, params)
	_, err = ParamNames([]string{"a b"})
	require.ErrorContains(t, err, "parameter name")
}

// source is a PolicySource over fixed policies, conflicting on one path
type source struct {
	policies map[string]ir.Policy
	reserved string
}

func (s source) Lookup(kind, ns, name, section string) (*ir.Policy, bool) {
	p, ok := s.policies[kind+"/"+ns+"/"+name+"/"+section]
	if !ok {
		return nil, false
	}
	return &p, true
}

func (s source) ProviderConflict(p *ir.Policy, matches []ir.Match) (string, bool) {
	for _, m := range matches {
		if p.Provider != "" && m.Path.Value == s.reserved {
			return s.reserved, true
		}
	}
	return "", false
}

func TestPoliciesBind(t *testing.T) {
	src := ir.Source{Kind: ir.KindHTTPRoute, Namespace: "shop", Name: "web"}
	policySrc := ir.Source{Kind: ir.KindCachePolicy, Namespace: "shop", Name: "cp"}
	s := source{reserved: "/api/v1/query_range", policies: map[string]ir.Policy{
		"HTTPRoute/shop/web/": {Name: "TricksterCachePolicy/shop/cp", Source: policySrc,
			Provider: "prometheus", CacheName: "objects"},
		"Service/shop/svc/":     {Name: "TricksterCachePolicy/shop/svc", TimeoutMS: 1000},
		"Service/shop/svc/http": {Name: "TricksterCachePolicy/shop/port", TimeoutMS: 2000},
	}}
	problems := NewProblems(false)
	model := &ir.IR{}
	m := NewPolicies(model, s, problems)

	require.Empty(t, m.Attach(TargetGateway, "shop", "gw", ""))
	route := m.Attach(TargetHTTPRoute, "shop", "web", "")
	require.Equal(t, "TricksterCachePolicy/shop/cp", route)
	require.Len(t, model.Policies, 1)

	// a rule whose paths the provider does not predefine keeps the provider
	plain := []ir.Match{{Path: ir.PathMatch{Type: ir.PathPrefix, Value: "/"}}}
	bound := m.Bind(src, 0, plain, route)
	require.Equal(t, route, bound)
	require.Empty(t, problems.List())

	// one that declares a predefined path loses the provider, and both objects are told
	clash := []ir.Match{{Path: ir.PathMatch{Type: ir.PathExact, Value: "/api/v1/query_range"}}}
	bound = m.Bind(src, 1, clash, route)
	require.Equal(t, route+"+noprovider", bound)
	require.Empty(t, m.Get(bound).Provider)
	require.Equal(t, "objects", m.Get(bound).CacheName)
	list := problems.List()
	require.Len(t, list, 2)
	require.Equal(t, src, list[0].Source)
	require.Contains(t, list[0].Detail, "rule 1: provider \"prometheus\" is not applied")
	require.Equal(t, policySrc, list[1].Source)
	require.Contains(t, list[1].Detail, "HTTPRoute/shop/web rule 1 declares path")

	// a member is governed by its Service's policy with the port's over it
	target := ir.ServiceTarget{Namespace: "shop", Name: "svc", PortName: "http"}
	member := m.BindMember(src, 0, plain, target)
	require.Equal(t, "TricksterCachePolicy/shop/svc+TricksterCachePolicy/shop/port", member)
	require.Equal(t, int64(2000), m.Get(member).TimeoutMS)
	target.PortName = ""
	require.Equal(t, "TricksterCachePolicy/shop/svc", m.BindMember(src, 0, plain, target))
	require.Empty(t, m.BindMember(src, 0, plain, ir.ServiceTarget{Namespace: "shop", Name: "none"}))

	// nothing binds without a source, and nothing is reported without a collector
	none := NewPolicies(&ir.IR{}, nil, nil)
	require.Empty(t, none.Attach(TargetHTTPRoute, "shop", "web", ""))
	require.Empty(t, none.Bind(src, 0, clash))
	quiet := NewPolicies(&ir.IR{}, s, nil)
	require.Equal(t, "TricksterCachePolicy/shop/cp+noprovider",
		quiet.Bind(src, 0, clash, quiet.Attach(TargetHTTPRoute, "shop", "web", "")))
}
