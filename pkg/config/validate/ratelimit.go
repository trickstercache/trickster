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
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rlopts "github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

const (
	planeHTTP = "http"
	planeTCP  = "tcp"
	planeUDP  = "udp"
)

// RateLimiters validates every named limiter before attachments are resolved.
func RateLimiters(c *config.Config) error {
	if c == nil || len(c.RateLimiters) == 0 {
		return nil
	}
	if err := c.RateLimiters.Validate(); err != nil {
		return err
	}
	return nil
}

func bindListenerRateLimit(c *config.Config, name string, options *listener.Options) error {
	if options == nil || options.RateLimiterName == "" {
		return nil
	}
	def := c.RateLimiters[options.RateLimiterName]
	if def == nil {
		return fmt.Errorf("listener %q references undefined rate limiter %q", name, options.RateLimiterName)
	}
	if providerregistry.NativeListeners().Get(options.Protocol) != nil {
		return fmt.Errorf("listener %q with protocol %q cannot use rate limiter %q: native listeners "+
			"have no rate-limit hook", name, options.Protocol, options.RateLimiterName)
	}
	options.RateLimiter = def
	return nil
}

func validateRateLimitPlacements(c *config.Config) error {
	if c == nil {
		return nil
	}
	planes := map[string]sets.Set[string]{}
	note := func(name, plane string) {
		if planes[name] == nil {
			planes[name] = sets.NewStringSet()
		}
		planes[name].Set(plane)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Listeners)) {
		options := c.Listeners[name]
		if options == nil || options.RateLimiter == nil {
			continue
		}
		plane, err := listenerPlane(options)
		if err != nil {
			return fmt.Errorf("listener %q: %w", name, err)
		}
		if err := checkAttachment(fmt.Sprintf("listener %q", name), options.RateLimiter, plane, true,
			limiterStream(options), false); err != nil {
			return err
		}
		if plane != planeHTTP && options.ProxyProtocol && len(options.TrustedProxies) == 0 &&
			keysOnClientIP(options.RateLimiter) {
			addWarning(c, fmt.Sprintf("listener %q uses rate limiter %q keyed on client_ip with proxy_protocol "+
				"and no trusted_proxies: every peer's header is believed", name, options.RateLimiterName))
		}
		note(options.RateLimiterName, plane)
	}
	httpBackends := aclBackendReachability(c, true)
	for _, backendName := range slices.Sorted(maps.Keys(c.Backends)) {
		backend := c.Backends[backendName]
		if backend == nil {
			continue
		}
		if err := checkRouteRateLimit(backendName, "", backend.RateLimiterName, backend.RateLimiter,
			backend, nil, httpBackends.Contains(backendName)); err != nil {
			return err
		}
		if backend.RateLimiter != nil {
			note(backend.RateLimiterName, planeHTTP)
		}
		for _, path := range backend.Paths {
			if path == nil || path.RateLimiterName == "" || path.RateLimiterName == reserved.ReferenceNone {
				continue
			}
			if err := checkRouteRateLimit(backendName, path.Path, path.RateLimiterName, path.RateLimiter,
				backend, path, httpBackends.Contains(backendName)); err != nil {
				return err
			}
			if path.RateLimiter != nil {
				note(path.RateLimiterName, planeHTTP)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(planes)) {
		opts := c.RateLimiters[name]
		if opts == nil || opts.Unit == "" {
			continue
		}
		if len(planes[name]) > 1 {
			return fmt.Errorf("rate limiter %q is attached on more than one plane and must leave unit unset", name)
		}
		var plane string
		for item := range planes[name] {
			plane = item
		}
		if !unitMatches(opts.Unit, plane) {
			return fmt.Errorf("rate limiter %q unit %q is not valid for this plane", name, opts.Unit)
		}
	}
	return nil
}

func checkRouteRateLimit(backendName, path, limiterName string, opts *rlopts.Options, backend *bo.Options,
	pathOpts *po.Options, httpServed bool,
) error {
	if limiterName == "" {
		return nil
	}
	where := fmt.Sprintf("backend %q", backendName)
	if path != "" {
		where = fmt.Sprintf("backend %q path %q", backendName, path)
	}
	if !httpServed {
		return fmt.Errorf("%s uses rate limiter %q but its traffic does not pass an HTTP route", where, limiterName)
	}
	if opts == nil {
		return fmt.Errorf("%s references undefined rate limiter %q", where, limiterName)
	}
	if err := checkAttachment(where, opts, planeHTTP, false, flowkey.StreamListener{}, routeHasAuthenticator(pathOpts, backend)); err != nil {
		return err
	}
	return nil
}

func checkAttachment(where string, opts *rlopts.Options, plane string, listenerScope bool, stream flowkey.StreamListener,
	hasAuth bool,
) error {
	for _, key := range opts.KeySources {
		readable := key.OnHTTP()
		if plane != planeHTTP {
			readable = key.OnStream(stream)
		}
		if !readable {
			return fmt.Errorf("%s cannot read rate limiter %q key on this listener", where, opts.Name)
		}
		if listenerScope && key.Requires() != 0 {
			return fmt.Errorf("%s rate limiter %q depends on request processing a listener does not do", where, opts.Name)
		}
		if !listenerScope && key.Requires().Has(flowkey.RequiresBody) {
			return fmt.Errorf("%s rate limiter %q reads the request body, which a route limiter cannot", where, opts.Name)
		}
		if !listenerScope && key.Requires().Has(flowkey.RequiresPrincipal) && !hasAuth {
			return fmt.Errorf("%s rate limiter %q reads the authenticated principal, but the route has no authenticator",
				where, opts.Name)
		}
	}
	if plane == planeHTTP && opts.Action == rlopts.ActionClose {
		return fmt.Errorf("%s uses rate limiter %q with action close, which is not valid on HTTP", where, opts.Name)
	}
	return nil
}

func unitMatches(unit, plane string) bool {
	switch plane {
	case planeHTTP:
		return unit == rlopts.UnitRequests
	case planeTCP:
		return unit == rlopts.UnitConnections
	case planeUDP:
		return unit == rlopts.UnitSessions || unit == rlopts.UnitDatagrams
	default:
		return false
	}
}

func listenerPlane(options *listener.Options) (string, error) {
	switch options.Protocol {
	case "", listener.ProtocolHTTP:
		return planeHTTP, nil
	case listener.ProtocolTCP, listener.ProtocolTLS:
		return planeTCP, nil
	case listener.ProtocolUDP:
		return planeUDP, nil
	default:
		return "", fmt.Errorf("protocol %q cannot carry a rate limiter", options.Protocol)
	}
}

func limiterStream(options *listener.Options) flowkey.StreamListener {
	return flowkey.StreamListener{
		TLS:           options.Protocol == listener.ProtocolTLS,
		ProxyProtocol: options.ProxyProtocol && options.Protocol != listener.ProtocolUDP,
	}
}

func keysOnClientIP(opts *rlopts.Options) bool {
	for _, key := range opts.KeySources {
		if key.Kind == flowkey.KeyClientIP {
			return true
		}
	}
	return false
}

func routeHasAuthenticator(path *po.Options, backend *bo.Options) bool {
	if path != nil && path.AuthenticatorName == reserved.ReferenceNone {
		return false
	}
	if path != nil && path.AuthOptions != nil {
		return true
	}
	return backend != nil && backend.AuthOptions != nil && (path == nil || path.AuthenticatorName == "")
}
