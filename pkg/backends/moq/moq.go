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

// Package moq provides the MoQ Transport backend provider: a relay serving moq listener and WebTransport sessions
// from an origin reached over native QUIC or WebTransport, or as the origin itself when it has none.
package moq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// Implementation is the MOQT_IMPLEMENTATION Trickster sends, with its version, when a listener names none
var Implementation = "trickster"

func implementation(configured string) string {
	switch {
	case configured != "":
		return configured
	case appinfo.Version != "":
		return Implementation + "/" + appinfo.Version
	}
	return Implementation
}

var errNoOrigin = errors.New("moq backend has no origin to probe")

const poolProbeText = "moq backend reaches its origins through alb backend %q, whose members are probed instead"

// Client implements the MoQ backend provider
type Client struct {
	backends.Backend
	sh     *shared
	scope  relay.Scope
	access relay.Access
}

var (
	_ backends.Backend           = (*Client)(nil)
	_ types.NewBackendClientFunc = NewClient
)

// NewClient returns a new MoQ backend Client
func NewClient(name string, o *bo.Options, router http.Handler, c cache.Cache, _ backends.Backends,
	_ types.Lookup,
) (backends.Backend, error) {
	cl := &Client{}
	b, err := backends.New(name, o, cl.RegisterHandlers, router, c)
	cl.Backend = b
	if err != nil {
		return cl, err
	}
	m := o.MoQ
	if m == nil {
		m = mo.New()
	}
	if o.OriginURL != "" {
		origin, err := parseOrigin(o.OriginURL)
		if err != nil {
			return cl, err
		}
		cl.scope = relay.Scope{Authority: origin.Host, Path: origin.Path}
	}
	if cl.access, err = accessFor(name, m); err != nil {
		return cl, err
	}
	sh, err := sharedFor(name, relayKey(o), func() (*shared, error) {
		sh := &shared{}
		var up relay.Upstream
		switch {
		case m.Upstream.ALB != "":
			// the alb's picker is bound when the configuration commits, in RetireClients
			sh.pool = newPoolUpstream(m.Upstream.ALB)
			up = sh.pool
		case o.OriginURL != "":
			origin, err := parseOrigin(o.OriginURL)
			if err != nil {
				return nil, err
			}
			var tc *tls.Config
			if o.TLS != nil {
				if tc, err = o.TLS.ToClientTLSConfig(); err != nil {
					return nil, err
				}
			}
			defaultDraft := 0
			if m.WebTransport != nil {
				defaultDraft = m.WebTransport.DefaultDraft
			}
			if sh.dialer, err = newDialer(origin, tc, m.Upstream.Drafts, defaultDraft, implementation("")); err != nil {
				return nil, err
			}
			sh.dialer.connKey = connKey(origin, o, m.Upstream.Drafts)
			up = sh.dialer
		}
		sh.relay = relay.New(relay.Options{
			LiveEdgeGroups: m.LiveEdge.Groups, LiveEdgeBytes: m.LiveEdge.MaxSizeBytes, Spill: m.Spill,
			MaxTTL: time.Duration(o.MaxTTL), FillTimeout: time.Duration(m.FillTimeout), Prewarm: m.Upstream.Prewarm,
			Linger: time.Duration(m.Upstream.Linger), MaxObjectSize: m.MaxObjectSizeBytes,
			MaxGapFetches: m.Upstream.MaxGapFetches, ReconnectBackoff: time.Duration(m.Upstream.ReconnectBackoff),
			Metrics: relayMetrics(name), CacheNamespace: name,
		}, up, sh)
		if sh.dialer != nil {
			sh.unwatch = append(sh.unwatch, sh.dialer.origin.watch(sh.relay))
		}
		if sh.pool != nil {
			sh.pool.sink = sh.relay
		}
		// a relay that outlives a reload takes the new policy when the reload commits, in RetireClients
		sh.relay.SetAccess(cl.access)
		return sh, nil
	})
	if err != nil {
		return cl, err
	}
	sh.cache.Store(cl)
	cl.sh = sh
	return cl, nil
}

func accessFor(backend string, m *mo.Options) (relay.Access, error) {
	var a relay.Access
	if p := m.Publish; p != nil {
		a.Publish, a.MaxPublishersPerTrack = p.Enabled, p.MaxPublishersPerTrack
		for _, s := range p.Namespaces {
			ns, err := mo.ParseNamespace(s)
			if err != nil {
				return a, err
			}
			a.Namespaces = append(a.Namespaces, ns)
		}
	}
	if az := m.Authorization; az != nil && az.Mode == mo.AuthorizationToken {
		tf, err := newTokenFile(backend, az.TokenFile)
		if err != nil {
			return a, err
		}
		a.Authorizer = tf
	}
	return a, nil
}

// Relay returns the relay serving the backend's sessions
func (c *Client) Relay() *relay.Relay {
	return c.sh.relay
}

// Scope returns the scope every session routed to the backend shares, so its tracks are shared too
func (c *Client) Scope() relay.Scope {
	return c.scope
}

// RegisterHandlers registers the handler that accepts WebTransport sessions on HTTP listeners
func (c *Client) RegisterHandlers(handlers.Lookup) {
	c.Backend.RegisterHandlers(handlers.Lookup{providers.MoQ: http.HandlerFunc(c.serveWebTransport)})
}

// DefaultPathConfigs returns the path that accepts WebTransport sessions on HTTP listeners when the backend
// configures none, so its configured paths alone decide which sessions it serves
func (c *Client) DefaultPathConfigs(o *bo.Options) po.List {
	if o != nil && len(o.Paths) > 0 {
		return nil
	}
	return po.List{WebTransportPath(rootPath)}
}

// WebTransportPath returns a path that accepts WebTransport sessions at and below p, the only route a moq
// backend serves on an HTTP listener
func WebTransportPath(p string) *po.Options {
	o := po.New()
	o.Path, o.HandlerName, o.Methods = p, providers.MoQ, []string{http.MethodConnect}
	o.MatchType, o.MatchTypeName = matching.PathMatchTypeSegment, matching.PathMatchNameSegment
	return o
}

// DefaultHealthCheckConfig returns the protocol-neutral health-check defaults
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	return ho.New()
}

// HealthCheckUnsupported says why a backend with no origin of its own is left unprobed, and is empty for one with
// an origin; the members of an alb it reaches its origins through are probed as backends of their own
func (c *Client) HealthCheckUnsupported() string {
	switch {
	case c.sh.pool != nil:
		return fmt.Sprintf(poolProbeText, c.sh.pool.alb)
	case c.sh.dialer == nil:
		return errNoOrigin.Error()
	}
	return ""
}

// HealthCheckProbe returns a probe that completes a SETUP exchange with the origin on a fresh session
func (c *Client) HealthCheckProbe() healthcheck.Probe {
	d := c.sh.dialer
	return func(ctx context.Context) error {
		if d == nil {
			return errNoOrigin
		}
		return d.probe(ctx)
	}
}

// Close ends the backend's upstream subscriptions and its session toward the origin
func (c *Client) Close() {
	if c.sh != nil {
		c.sh.close()
	}
}
