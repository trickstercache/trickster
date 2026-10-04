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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	geolocopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"
)

// Geo validates the geo_locators and geo_acls sections, and the locator each geo ACL names. It builds nothing:
// a locator's files are checked for being readable, never loaded.
func Geo(c *config.Config) error {
	if c == nil {
		return nil
	}
	if err := c.GeoLocators.Validate(); err != nil {
		return err
	}
	if err := c.GeoACLs.Validate(); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(c.GeoACLs)) {
		o := c.GeoACLs[name]
		if o == nil {
			continue
		}
		if c.GeoLocators[o.LocatorName()] == nil {
			if o.GeoLocatorName == "" {
				return fmt.Errorf("geo ACL %q names no geo_locator_name, and no geo locator is named %q: "+
					"define one, or name one", name, geolocopts.DefaultName)
			}
			return fmt.Errorf("geo ACL %q names undefined geo locator %q", name, o.GeoLocatorName)
		}
		for _, w := range o.Warnings() {
			addWarning(c, w)
		}
	}
	return nil
}

func geoListeners(c *config.Config, nativeTargets map[string]bool) error {
	// Listeners has derived which listeners serve each backend by now
	for _, name := range slices.Sorted(maps.Keys(c.Backends)) {
		b := c.Backends[name]
		if b == nil || b.IsTemplate {
			continue
		}
		if b.GeoACLOptions != nil {
			if nativeTargets[name] {
				return fmt.Errorf("backend %q: geo_acl_name is not supported on a backend that a native protocol "+
					"listener's alb sends sessions to, since a session is judged by the alb's geo ACL before it is "+
					"routed; set it on the alb, or on this backend's paths to judge its HTTP requests", name)
			}
			if !c.GeoLocators.ReadsAddresses(b.GeoACLOptions.LocatorName()) &&
				(len(b.NativeListenerProtocols) > 0 || servesStreamListener(c, b)) {
				return fmt.Errorf("backend %q: geo ACL %q uses a %s geo locator, which judges HTTP requests only, "+
					"but the backend serves a native protocol or stream listener", name, b.GeoACLName, providers.Header)
			}
		}
		warnGeoListeners(c, b)
	}
	return nil
}

func warnGeoListeners(c *config.Config, b *bo.Options) {
	acls := gatingACLs(b)
	if len(acls) == 0 {
		return
	}
	readsHeaders := false
	for _, o := range acls {
		readsHeaders = readsHeaders || !c.GeoLocators.ReadsAddresses(o.LocatorName())
	}
	for _, ln := range b.ListenerNames {
		lo := c.Listeners[ln]
		if lo == nil || len(lo.TrustedProxies) > 0 {
			continue
		}
		if lo.ProxyProtocol {
			addWarning(c, fmt.Sprintf("backend %q is gated by a geo ACL on listener %q, which takes the PROXY "+
				"protocol from any peer since it has no trusted_proxies, so a client can name its own source", b.Name, ln))
		}
		if readsHeaders && lo.Protocol == listener.ProtocolHTTP {
			addWarning(c, fmt.Sprintf("backend %q is gated by a geo ACL whose %s geo locator believes headers only "+
				"from trusted_proxies, which listener %q has none of, so no request there has a location",
				b.Name, providers.Header, ln))
		}
	}
}

func gatingACLs(b *bo.Options) []*geoaclopts.Options {
	var out []*geoaclopts.Options
	if b.GeoACLOptions != nil {
		out = append(out, b.GeoACLOptions)
	}
	for _, p := range b.Paths {
		if p != nil && p.GeoACLOptions != nil && p.GeoACLName != reserved.ReferenceNone {
			out = append(out, p.GeoACLOptions)
		}
	}
	return out
}

func servesStreamListener(c *config.Config, b *bo.Options) bool {
	for _, ln := range b.ListenerNames {
		if lo := c.Listeners[ln]; lo != nil && lo.IsStream() {
			return true
		}
	}
	return false
}
