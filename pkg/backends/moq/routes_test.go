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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
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
	single := &routeTable{entries: []routeEntry{{name: "a", client: a, hosts: []string{"a.example.com"}}}}
	if d, ok := single.ResolveRoute(backends.RouteInput{Authority: "elsewhere"}); !ok || d.Target.Backend != a {
		t.Error("a listener with one backend serves every session with it")
	}
	var empty *routeTable
	if _, ok := empty.ResolveRoute(backends.RouteInput{}); ok {
		t.Error("no routes")
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
