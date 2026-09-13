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

func TestPolicyOverlay(t *testing.T) {
	// A more specific policy writes only what it sets over a less specific one: header maps
	// merge with the specific entries winning, lists are replaced, identity is kept
	base := Policy{
		Name: "base", Source: Source{Kind: KindGatewayClass, Name: "gc"},
		Handler: HandlerProxy, CacheName: "objects", TimeoutMS: 1000, MaxTTLMS: 5000,
		RequestHeaders:    map[string]string{"X-A": "1", "X-B": "1"},
		ResponseHeaders:   map[string]string{"X-R": "1"},
		CacheKeyParams:    []string{"a"},
		AuthenticatorName: "auth", Provider: "prometheus", ResultHeader: ResultHeaderHide,
	}
	over := &Policy{
		Name: "over", Source: Source{Kind: KindCachePolicy, Name: "cp"},
		Handler: HandlerProxyCache, TimeoutMS: 2000, MaxTTLMS: 7000,
		RequestHeaders: map[string]string{"X-B": "2", "X-C": "2"},
		CORSHeaders:    map[string]string{"Access-Control-Allow-Origin": "*"},
		CacheKeyParams: []string{"b", "c"}, CacheKeyHeaders: []string{"X-Tenant"},
		ResultHeader: ResultHeaderExpose,
	}
	got := base.Overlay(over)
	require.Equal(t, "base", got.Name)
	require.Equal(t, KindGatewayClass, got.Source.Kind)
	require.Equal(t, HandlerProxyCache, got.Handler)
	require.Equal(t, "objects", got.CacheName, "an unset field keeps the base value")
	require.Equal(t, int64(2000), got.TimeoutMS)
	require.Equal(t, int64(7000), got.MaxTTLMS)
	require.Equal(t, map[string]string{"X-A": "1", "X-B": "2", "X-C": "2"}, got.RequestHeaders)
	require.Equal(t, map[string]string{"X-R": "1"}, got.ResponseHeaders)
	require.Equal(t, map[string]string{"Access-Control-Allow-Origin": "*"}, got.CORSHeaders)
	require.Equal(t, []string{"b", "c"}, got.CacheKeyParams)
	require.Equal(t, []string{"X-Tenant"}, got.CacheKeyHeaders)
	require.Equal(t, "auth", got.AuthenticatorName)
	require.Equal(t, "prometheus", got.Provider)
	require.Equal(t, ResultHeaderExpose, got.ResultHeader)

	// the inputs are untouched, and a nil overlay is a clone
	require.Equal(t, map[string]string{"X-A": "1", "X-B": "1"}, base.RequestHeaders)
	require.Equal(t, []string{"a"}, base.CacheKeyParams)
	same := base.Overlay(nil)
	require.Equal(t, base, same)
	same.RequestHeaders["X-A"] = "changed"
	same.CacheKeyParams[0] = "changed"
	require.Equal(t, "1", base.RequestHeaders["X-A"])
	require.Equal(t, "a", base.CacheKeyParams[0])
}

func TestPolicyOverlayFoldsHeaderOperations(t *testing.T) {
	// a more specific policy's operation on a header is the last word on that header whatever
	// the spelling or the operator the less specific one used
	base := Policy{
		RequestHeaders:  map[string]string{"Authorization": "Bearer inherited", "x-trace": "a"},
		ResponseHeaders: map[string]string{"Vary": "Accept"},
		CORSHeaders:     map[string]string{"Access-Control-Allow-Origin": "https://a.example.com"},
	}
	over := &Policy{
		RequestHeaders:  map[string]string{"-authorization": "", "+X-Trace": "b"},
		ResponseHeaders: map[string]string{"+vary": "Accept-Encoding"},
		CORSHeaders:     map[string]string{"access-control-allow-origin": "*"},
	}
	got := base.Overlay(over)
	require.Equal(t, map[string]string{"-Authorization": "", "x-trace": "a,b"}, got.RequestHeaders)
	require.Equal(t, map[string]string{"Vary": "Accept,Accept-Encoding"}, got.ResponseHeaders)
	require.Equal(t, map[string]string{"Access-Control-Allow-Origin": "*"}, got.CORSHeaders)

	// a delete followed by an append is a set, and a set after a delete restores the header
	got = Policy{RequestHeaders: map[string]string{"-X-A": ""}}.Overlay(
		&Policy{RequestHeaders: map[string]string{"+x-a": "1"}})
	require.Equal(t, map[string]string{"X-A": "1"}, got.RequestHeaders)
	got = Policy{RequestHeaders: map[string]string{"-X-A": ""}}.Overlay(
		&Policy{RequestHeaders: map[string]string{"X-A": "2"}})
	require.Equal(t, map[string]string{"X-A": "2"}, got.RequestHeaders)
}

func TestPolicyOverlayListsInheritOrReplace(t *testing.T) {
	// an omitted list inherits, and an explicitly empty one clears what was inherited
	base := Policy{CacheKeyParams: []string{"a"}, CacheKeyHeaders: []string{"X-Tenant"}}
	inherited := base.Overlay(&Policy{})
	require.Equal(t, []string{"a"}, inherited.CacheKeyParams)
	require.Equal(t, []string{"X-Tenant"}, inherited.CacheKeyHeaders)
	cleared := base.Overlay(&Policy{CacheKeyParams: []string{}, CacheKeyHeaders: []string{}})
	require.NotNil(t, cleared.CacheKeyParams)
	require.Empty(t, cleared.CacheKeyParams)
	require.NotNil(t, cleared.CacheKeyHeaders)
	require.Empty(t, cleared.CacheKeyHeaders)
	// clearing survives a clone and changes the hash, so the change reaches the data plane
	c := cleared.Clone()
	require.NotNil(t, c.CacheKeyParams)
	require.Empty(t, c.CacheKeyParams)
	with := &IR{Policies: []Policy{{Name: "p", CacheKeyHeaders: []string{"X-Tenant"}}}}
	none := &IR{Policies: []Policy{{Name: "p"}}}
	empty := &IR{Policies: []Policy{{Name: "p", CacheKeyHeaders: []string{}}}}
	require.NotEqual(t, with.Hash(), empty.Hash())
	require.NotEqual(t, none.Hash(), empty.Hash())
}

func TestPolicyOverlayFillsEveryStringField(t *testing.T) {
	// every string field an overlay sets must reach the result; a field left out of the
	// overlay list would be silently ignored
	over := &Policy{
		Handler: "h", CacheName: "c", RoutingMode: "r", NegativeCacheName: "n", CORSMode: "m",
		CollapsedForwarding: "cf", RewriteTarget: "/t", TracingName: "tr",
		ReqRewriterName: "rw", AuthenticatorName: "a", HealthMode: "probe",
		Provider: "graphite", ResultHeader: ResultHeaderHide,
	}
	got := Policy{}.Overlay(over)
	over.Name, over.Source = got.Name, got.Source
	require.Equal(t, *over, got)
}

func TestReportPolicies(t *testing.T) {
	// policy status merges and counts like the other report entries
	a := &Report{Policies: []PolicyStatus{{Source: Source{Kind: KindCachePolicy, Name: "a"}}}}
	b := &Report{Policies: []PolicyStatus{{Source: Source{Kind: KindCachePolicy, Name: "b"}}}}
	require.Len(t, a.Merge(b).Policies, 2)
	require.False(t, a.IsEmpty())
	require.True(t, (&Report{}).IsEmpty())
}
