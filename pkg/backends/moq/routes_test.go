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

package moq

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

type namedBackend struct {
	backends.Backend
	name string
}

func (n namedBackend) Name() string { return n.name }

func TestLongestPrefix(t *testing.T) {
	tests := []struct {
		prefixes []string
		path     string
		want     int
	}{
		{nil, "/anything", 1},
		{[]string{"/live"}, "/live", 5},
		{[]string{"/live"}, "/live/sports", 5},
		{[]string{"/live"}, "/lively", -1},
		{[]string{"/live/"}, "/live/x", 6},
		{[]string{"/", "/live"}, "/live/x", 5},
		{[]string{"/vod"}, "/live", -1},
	}
	for _, tc := range tests {
		if got := longestPrefix(tc.prefixes, tc.path); got != tc.want {
			t.Errorf("longestPrefix(%v, %q) = %d, want %d", tc.prefixes, tc.path, got, tc.want)
		}
	}
}

func TestRouteTable(t *testing.T) {
	a, b, c := namedBackend{name: "a"}, namedBackend{name: "b"}, namedBackend{name: "c"}
	rt := &routeTable{entries: []routeEntry{
		{name: "a", client: a, hosts: []string{"live.example.com"}},
		{name: "b", client: b, paths: []string{"/vod"}},
		{name: "c", client: c},
	}}
	resolve := func(authority, path string) string {
		d, ok := rt.ResolveRoute(backends.RouteInput{Authority: authority, Path: path})
		if !ok {
			return ""
		}
		return d.Target.Backend.Name()
	}
	tests := []struct{ authority, path, want string }{
		{"live.example.com:443", "/", "a"},
		{"LIVE.example.com.", "/vod/x", "a"},
		{"other.example.com", "/vod/x", "b"},
		{"other.example.com", "/live", "c"},
		{"other.example.com", "", "c"},
	}
	for _, tc := range tests {
		if got := resolve(tc.authority, tc.path); got != tc.want {
			t.Errorf("%s%s routed to %q, want %q", tc.authority, tc.path, got, tc.want)
		}
	}
	strict := &routeTable{entries: []routeEntry{
		{name: "a", client: a, hosts: []string{"a.example.com"}},
		{name: "b", client: b, hosts: []string{"b.example.com"}},
	}}
	if _, ok := strict.ResolveRoute(backends.RouteInput{Authority: "c.example.com", Path: "/"}); ok {
		t.Error("a host no backend names is refused when several backends share the listener")
	}
	// a wildcard host covers what no backend names exactly, a one-label wildcard one label only
	d, e := namedBackend{name: "d"}, namedBackend{name: "e"}
	wild := &routeTable{entries: []routeEntry{
		{name: "a", client: a, hosts: []string{"live.example.com"}},
		{name: "d", client: d, hosts: []string{"**.example.com"}},
		{name: "e", client: e, hosts: []string{"*.example.net"}},
	}}
	for authority, want := range map[string]namedBackend{
		"live.example.com": a, "eu.live.example.com": d, "vod.example.net": e,
	} {
		if r, ok := wild.ResolveRoute(backends.RouteInput{Authority: authority}); !ok || r.Target.Backend != want {
			t.Errorf("%s routed to %v, want %s", authority, r.Target.Backend, want.name)
		}
	}
	if _, ok := wild.ResolveRoute(backends.RouteInput{Authority: "a.vod.example.net"}); ok {
		t.Error("a one-label wildcard does not match two labels")
	}
	single := &routeTable{entries: []routeEntry{{name: "a", client: a, hosts: []string{"a.example.com"}}}}
	if d, ok := single.ResolveRoute(backends.RouteInput{Authority: "elsewhere"}); !ok || d.Target.Backend != a {
		t.Error("a listener with one backend serves every session with it")
	}
	var empty *routeTable
	if _, ok := empty.ResolveRoute(backends.RouteInput{}); ok {
		t.Error("no routes")
	}
}

func TestRoutesForPatterns(t *testing.T) {
	// a pattern routes WebTransport sessions alone, so a backend with nothing else stays out of the native table
	c := config.NewConfig()
	c.Backends = bo.Lookup{}
	all := backends.Backends{}
	for name, paths := range map[string]po.List{
		"prefix":  {{Path: "/live", MatchType: matching.PathMatchTypeSegment}},
		"pattern": {{Path: "^/vod/[a-z]+$", MatchType: matching.PathMatchTypeRegex}},
		"mixed":   {{Path: "^/x$", MatchType: matching.PathMatchTypeRegex}, {Path: "/mixed"}},
	} {
		o := bo.New()
		o.Name, o.Provider, o.ListenerNames, o.Paths = name, "moq", []string{"moq1"}, paths
		c.Backends[name] = o
		all[name] = namedBackend{name: name}
	}
	rt := routesFor(native.BuildRequest{Config: c, ListenerName: "moq1", BackendClients: all})
	got := map[string][]string{}
	for _, e := range rt.entries {
		got[e.name] = e.paths
	}
	if len(got) != 2 || !slices.Equal(got["prefix"], []string{"/live"}) || !slices.Equal(got["mixed"], []string{"/mixed"}) {
		t.Errorf("native routes: %v", got)
	}
}

func TestValidateBackendRefusals(t *testing.T) {
	a := NativeListenerAdapter()
	backend := func(origin string, mutate func(*bo.Options)) *bo.Options {
		o := bo.New()
		o.Name, o.Provider, o.OriginURL = "b", "moq", origin
		if mutate != nil {
			mutate(o)
		}
		return o
	}
	for name, o := range map[string]*bo.Options{
		"an origin and an alb": backend("moqt://origin.example.com/", func(o *bo.Options) {
			o.MoQ = mo.New()
			o.MoQ.Upstream.ALB = "origins"
		}),
		"an unsupported scheme": backend("http://origin.example.com/", nil),
		"no host":               backend("moqt:///live", nil),
		"an invalid moq block": backend("moqt://origin.example.com/", func(o *bo.Options) {
			o.MoQ = mo.New()
			o.MoQ.LiveEdge.Groups = 0
		}),
	} {
		if err := a.ValidateBackend(o); err == nil {
			t.Errorf("%s is refused", name)
		}
	}
	// every path of a moq backend reaches the same tracks, so a path's own list would guard nothing
	for field, set := range map[string]func(*po.Options){
		"ip_acl_name":        func(p *po.Options) { p.IPACLName = "office" },
		"geo_acl_name":       func(p *po.Options) { p.GeoACLName = reserved.ReferenceNone },
		"authenticator_name": func(p *po.Options) { p.AuthenticatorName = "basic" },
		"rate_limiter_name":  func(p *po.Options) { p.RateLimiterName = "edge" },
	} {
		o := backend("moqt://origin.example.com/", func(o *bo.Options) {
			p := &po.Options{Path: "/private"}
			set(p)
			o.Paths = po.List{p}
		})
		if err := a.ValidateBackend(o); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("a path's %s is refused: %v", field, err)
		}
	}
	local := backend("moqt://origin.example.com/", func(o *bo.Options) {
		o.Paths = po.List{{Path: "/health", HandlerName: "health", IPACLName: "office"}}
	})
	if err := a.ValidateBackend(local); err != nil {
		t.Errorf("a local handler's path answers HTTP requests, and keeps its own lists: %v", err)
	}
	o := backend("", nil)
	o.MoQ = nil
	if err := a.ValidateBackend(o); err != nil || o.MoQ == nil {
		t.Errorf("a backend without a moq block takes the defaults: %v", err)
	}
	lo := &listener.Options{Protocol: listener.ProtocolMoQ}
	if err := a.ValidateListener(lo); err != nil || lo.MoQ == nil {
		t.Errorf("a listener without a moq block takes the defaults: %v", err)
	}
}

func TestAccessFor(t *testing.T) {
	m := mo.New()
	m.Publish.Enabled, m.Publish.Namespaces = true, []string{"live/studio", "vod"}
	a, err := accessFor("b", m)
	if err != nil || !a.Publish || len(a.Namespaces) != 2 || string(a.Namespaces[0][1]) != "studio" {
		t.Errorf("publishing namespaces reach the relay's access policy: %+v %v", a, err)
	}
	m.Publish.Namespaces = []string{"live//studio"}
	if _, err = accessFor("b", m); err == nil {
		t.Error("an unparsable namespace fails the backend")
	}
}

func TestRestartKey(t *testing.T) {
	if restartKey(nil) == "" || restartKey(nil) != restartKey(nil) {
		t.Error("restart keys are stable")
	}
}

func TestAdapterContract(t *testing.T) {
	a := NativeListenerAdapter()
	if a.Protocol() != "moq" || !a.ServesProvider("moq") || a.ServesProvider("rpc") || len(a.Providers()) != 1 ||
		!a.SupportsHTTP("moq") || a.Configured(nil) {
		t.Error("adapter identity")
	}
	if a.ValidateListener(nil) == nil || a.ValidateBackend(nil) == nil || a.ValidateUserRouter(nil, "", nil) == nil ||
		a.ValidateBalancer(nil, "", nil) == nil {
		t.Error("unsupported shapes are refused")
	}
	if _, err := a.Describe(nil, "moq1"); err == nil {
		t.Error("a listener no backend uses has no description")
	}
	if a.RouteResolver(native.BuildRequest{}) == nil {
		t.Error("an empty route table still resolves, to nothing")
	}
	o := mo.NewListener()
	o.MaxSessions = 7
	if restartKey(o) == restartKey(nil) {
		t.Error("a listener setting changes the restart key")
	}
	if _, err := a.(native.PacketAdapter).Join(native.BuildRequest{}, "x", nil, nil); err == nil {
		t.Error("joining needs listener options")
	}
}

func TestRouteJudges(t *testing.T) {
	// a session is judged by the acls of the backend it is routed to
	list, _, err := ipacl.Compile(ipacl.Options{Name: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	geo := &geoaclopts.Options{Compiled: compiledGeo{denyAll{}}}
	o := bo.New()
	o.Name, o.Provider, o.ListenerNames, o.Hosts = "live", "moq", []string{"moq1"}, []string{"live.example.com"}
	o.IPACLName, o.IPACL, o.GeoACLName, o.GeoACLOptions = "backend", list, "geo", geo
	other := bo.New()
	other.Name, other.Provider, other.ListenerNames, other.Hosts = "other", "moq", []string{"moq1"}, []string{"x"}
	c := config.NewConfig()
	c.Backends = bo.Lookup{"live": o, "other": other}
	rt := routesFor(native.BuildRequest{Config: c, ListenerName: "moq1",
		BackendClients: backends.Backends{"live": namedBackend{name: "live"}, "other": namedBackend{name: "other"}}})
	judged := func(authority string) sessionJudge {
		e, _, ok := rt.resolve(backends.RouteInput{Authority: authority, Path: "/"})
		if !ok {
			t.Fatalf("%s did not route", authority)
		}
		return e.judge
	}
	if j := judged("live.example.com"); j.ipacl != list || j.geo == nil {
		t.Errorf("the backend's acls judge its sessions: %+v", j)
	}
	if j := judged("x"); j.ipacl != nil || j.geo != nil {
		t.Errorf("a backend naming no acl admits every session: %+v", j)
	}
	client := netip.MustParseAddr("192.0.2.1")
	if reason, denied := judged("live.example.com").refusal(client); !denied || reason != "" {
		t.Errorf("the backend's list refuses first, without a reason: %v %q", denied, reason)
	}
	if reason, denied := (sessionJudge{geo: denyAll{}}).refusal(client); !denied || reason != "not here" {
		t.Errorf("a geo refusal carries its message: %v %q", denied, reason)
	}
	if _, denied := judged("x").refusal(client); denied {
		t.Error("no acl refuses nothing")
	}
	if geoGate(nil) != nil || geoGate(&geoaclopts.Options{}) != nil {
		t.Error("an absent or uncompiled geo ACL gates nothing")
	}
}
