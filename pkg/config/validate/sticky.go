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

package validate

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// issuesTokens reports whether an ALB's sticky mode is set to one that issues tokens, which only
// an http listener can carry
func issuesTokens(o *ao.Options) bool {
	return o.Sticky != nil && (o.Sticky.Mode == so.ModeCookie || o.Sticky.Mode == so.ModeHeader)
}

// stickyOnRequests holds an ALB's sticky block to an http listener, which reads a table key from
// each request when the ALB keeps its sessions in a table there
func stickyOnRequests(backendName, listenerName string, o *ao.Options) error {
	if o.Sticky == nil || o.Sticky.ModeFor(true) != so.ModeTable || o.Sticky.Table.KeySource.OnHTTP() {
		return nil
	}
	return fmt.Errorf("alb backend %q: sticky.table.key %q cannot be read from a request, which http "+
		"listener %q serves", backendName, o.Sticky.Table.Key, listenerName)
}

// stickyOnFlows holds an ALB's sticky block to a stream or native listener, which keeps sessions
// only in a table, by a key that readable reports it can read
func stickyOnFlows(listenerName, protocol, backendName string, o *ao.Options, readable func() bool) error {
	switch {
	case o.Sticky == nil:
		return nil
	case issuesTokens(o):
		return fmt.Errorf("listener %q with protocol %q cannot carry the tokens of alb backend %q's "+
			"sticky.mode %q; leave sticky.mode unset to keep sessions in a table there", listenerName,
			protocol, backendName, o.Sticky.Mode)
	case !readable():
		return fmt.Errorf("listener %q with protocol %q cannot read alb backend %q's sticky.table.key %q",
			listenerName, protocol, backendName, o.Sticky.Table.Key)
	}
	return nil
}

// reservedDefaultBackend is the name of the backend routing makes the default when none is marked
const reservedDefaultBackend = "default"

// cookieID is what a browser tells cookies apart by; it sends a cookie to every port and scheme of
// a host, and one with no domain only to the host that set it, which cookieOwner carries
type cookieID struct {
	name, domain, path string
}

// cookieOwner is an ALB that sets a cookie, and the hosts it serves; none serves every host
type cookieOwner struct {
	backend string
	hosts   []string
}

// stickyCookies refuses two ALBs that set one cookie for a shared host on any listeners, since
// their tokens would thrash; visible says if a backend registers a listener path.
func stickyCookies(c *config.Config, visible func(string) bool) error {
	owners := make(map[cookieID][]cookieOwner)
	served := servedHosts(c, visible)
	for _, backendName := range slices.Sorted(maps.Keys(c.Backends)) {
		backend := c.Backends[backendName]
		if backend == nil || backend.Provider != providers.ALB || backend.ALBOptions == nil {
			continue
		}
		s := backend.ALBOptions.Sticky
		if s == nil || s.ModeFor(true) != so.ModeCookie || len(httpListenerNames(c, backend)) == 0 {
			continue
		}
		hosts, every := served(backendName)
		if !every && len(hosts) == 0 {
			// no client reaches it, as with a mirror's target, so no browser holds its cookie
			continue
		}
		owner := cookieOwner{backend: backendName, hosts: hosts}
		if every {
			owner.hosts = nil
		}
		id := cookieID{s.Cookie.Name, strings.ToLower(s.Cookie.Domain), s.Cookie.Path}
		for _, other := range owners[id] {
			// a domain cookie reaches every host under it, whatever hosts the ALBs serve
			if id.domain != "" || hostnames.ListsOverlap(owner.hosts, other.hosts) {
				return fmt.Errorf("alb backends %q and %q both set sticky cookie %q for a host they "+
					"both serve, which a browser shares across ports and schemes; give one of them its "+
					"own sticky.cookie.name", other.backend, backendName, s.Cookie.Name)
			}
		}
		owners[id] = append(owners[id], owner)
	}
	return nil
}

// servedHosts returns a lookup of the hosts a backend answers, or every: its own routes' plus its
// dispatchers', which must include every reference that can dispatch to it.
func servedHosts(c *config.Config, visible func(string) bool) func(string) ([]string, bool) {
	parents := dispatchers(c)
	defaultName := defaultBackend(c)
	// own returns the hosts a backend's own routes answer for, or every
	own := func(name string) ([]string, bool) {
		b := c.Backends[name]
		if b == nil || name == defaultName {
			return nil, true
		}
		// a backend's own routes exist only when a path registers on a listener; then path routing
		// answers /name/... on every host, whatever hosts the backend also names
		if !visible(name) {
			return nil, false
		}
		if b.AnyHostRouting || !b.PathRoutingDisabled {
			return nil, true
		}
		return b.Hosts, false
	}
	// a breadth-first walk up the dispatchers visits each backend once, however many paths lead
	// to it, so a shared or cyclic dispatch graph costs its size and not its number of paths
	return func(name string) ([]string, bool) {
		queue := []string{name}
		queued := sets.New([]string{name})
		found := sets.NewStringSet()
		var out []string
		for i := 0; i < len(queue); i++ {
			hosts, every := own(queue[i])
			if every {
				return nil, true
			}
			for _, h := range hosts {
				if !found.Contains(h) {
					found.Set(h)
					out = append(out, h)
				}
			}
			for _, parent := range parents[queue[i]] {
				if !queued.Contains(parent) {
					queued.Set(parent)
					queue = append(queue, parent)
				}
			}
		}
		return out, false
	}
}

// listenerVisible says whether a backend registers a listener path, as routing does: a path with
// methods and a client handler, not dispatch_only, over its defaults; or no client.
func listenerVisible(c *config.Config, clients backends.Backends) func(string) bool {
	memo := make(map[string]bool)
	return func(name string) bool {
		if v, ok := memo[name]; ok {
			return v
		}
		v := registersOnListener(c.Backends[name], clients[name])
		memo[name] = v
		return v
	}
}

func registersOnListener(b *bo.Options, client backends.Backend) bool {
	if b == nil || client == nil {
		return true
	}
	paths := b.Paths
	if !b.PathDefaultsDisabled {
		paths = client.DefaultPathConfigs(b).Overlay(b.Paths)
	}
	handlers := client.Handlers()
	for _, p := range paths {
		if p == nil || p.DispatchOnly || len(p.Methods) == 0 {
			continue
		}
		if p.Handler != nil || handlers[p.HandlerName] != nil {
			return true
		}
	}
	return false
}

// defaultBackend returns the backend that answers requests no other route claims, on every host:
// the one marked is_default, else the one named default
func defaultBackend(c *config.Config) string {
	for name, b := range c.Backends {
		if b != nil && b.IsDefault {
			return name
		}
	}
	if _, ok := c.Backends[reservedDefaultBackend]; ok {
		return reservedDefaultBackend
	}
	return ""
}

// dispatchers maps each backend to the ALBs (pool or user router) and rules that dispatch to it. A
// mirror is not one, as its response and any cookie in it are discarded.
func dispatchers(c *config.Config) map[string][]string {
	out := make(map[string][]string)
	add := func(child, parent string) {
		if child != "" && !slices.Contains(out[child], parent) {
			out[child] = append(out[child], parent)
		}
	}
	for name, b := range c.Backends {
		if b == nil {
			continue
		}
		if b.ALBOptions != nil {
			for _, m := range b.ALBOptions.Pool {
				add(m.Name, name)
			}
			if u := b.ALBOptions.UserRouter; u != nil {
				add(u.DefaultBackend, name)
				for _, m := range u.Users {
					if m != nil {
						add(m.ToBackend, name)
					}
				}
			}
		}
		if r := c.Rules[b.RuleName]; b.RuleName != "" && r != nil {
			add(r.NextRoute, name)
			for _, cs := range r.CaseOptions {
				if cs != nil {
					add(cs.NextRoute, name)
				}
			}
		}
	}
	return out
}
