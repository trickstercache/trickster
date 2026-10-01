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

package setup

import (
	"maps"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	geostream "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/stream"
	georegistry "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/registry"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

func buildGeo(si *instance.ServerInstance, c *config.Config) (georegistry.Set, error) {
	// only locators that attached geo ACLs name are opened; on failure nothing new is left open
	acls := attachedGeoACLs(c)
	if len(acls) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(acls))
	for _, o := range acls {
		if !slices.Contains(names, o.LocatorName()) {
			names = append(names, o.LocatorName())
		}
	}
	set, err := georegistry.Build(names, c.GeoLocators, si.GeoLocators)
	if err != nil {
		return nil, err
	}
	for _, o := range acls {
		a, err := acl.Compile(o, set.Locator(o.LocatorName()), o.LocatorName())
		if err != nil {
			set.CloseExcept(si.GeoLocators)
			return nil, err
		}
		o.Compiled = a
	}
	return set, nil
}

func attachedGeoACLs(c *config.Config) []*geoaclopts.Options {
	if c == nil || len(c.GeoACLs) == 0 {
		return nil
	}
	attached := make(map[string]*geoaclopts.Options)
	// the controller may name any geo ACL, and a template's reaches the members built from it
	if c.Kubernetes.IsEnabled() {
		for name, o := range c.GeoACLs {
			if o != nil {
				attached[name] = o
			}
		}
	}
	for _, b := range c.Backends {
		if b == nil {
			continue
		}
		if b.GeoACLOptions != nil {
			attached[b.GeoACLOptions.Name] = b.GeoACLOptions
		}
		for _, p := range b.Paths {
			if p != nil && p.GeoACLOptions != nil && p.GeoACLName != reserved.ReferenceNone {
				attached[p.GeoACLOptions.Name] = p.GeoACLOptions
			}
		}
	}
	out := make([]*geoaclopts.Options, 0, len(attached))
	for _, name := range slices.Sorted(maps.Keys(attached)) {
		out = append(out, attached[name])
	}
	return out
}

func sessionGateFor(c *config.Config, listenerName string) backends.SessionGate {
	if c == nil {
		return nil
	}
	for _, b := range c.Backends {
		if b == nil || b.IsTemplate || !b.UsesListener(listenerName) {
			continue
		}
		if a := compiledGeoACL(b); a != nil {
			return a.SessionGate()
		}
	}
	return nil
}

func compiledGeoACL(b *bo.Options) *acl.ACL {
	if b == nil || b.GeoACLOptions == nil {
		return nil
	}
	a, _ := b.GeoACLOptions.Compiled.(*acl.ACL)
	return a
}

type geoStreamAdmission struct {
	tls   bool
	gated bool
	peer  *acl.ACL
	hosts *l4.HostTable[*acl.ACL]
}

func newGeoStreamAdmission(protocol string) *geoStreamAdmission {
	g := &geoStreamAdmission{tls: protocol == listenerconfig.ProtocolTLS}
	if g.tls {
		g.hosts = l4.NewHostTable[*acl.ACL]()
	}
	return g
}

func (g *geoStreamAdmission) add(b *bo.Options, hosts []string) {
	// an ungated backend's hosts are recorded too, so a gated backend's wildcard never judges them
	a := compiledGeoACL(b)
	g.gated = g.gated || a != nil
	if !g.tls {
		g.peer = a
		return
	}
	for _, h := range hosts {
		// a duplicate is refused by validation, and logged when the relay's table refuses it
		_ = g.hosts.Add(h, a)
	}
}

func (g *geoStreamAdmission) admission() l4.Admission {
	switch {
	case !g.gated:
		return nil
	case g.tls:
		return geostream.ForHosts(g.hosts)
	}
	return geostream.ForBackend(g.peer)
}

func setSessionGate(svr listener.ProtocolServer, gate backends.SessionGate) bool {
	// a server that takes no gate must not serve a gated listener, so a geo ACL is never silently skipped
	if u, ok := svr.(listener.SessionGateUpdater); ok {
		u.UpdateSessionGate(gate)
		return true
	}
	return gate == nil
}
