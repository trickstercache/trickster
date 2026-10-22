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
	"crypto/tls"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	checksum "github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/quicmux"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
)

var (
	errNoBackend = errors.New("no moq backend uses this listener")
	errNotPacket = errors.New("a moq listener joins a QUIC endpoint and is never built as a TCP server")
)

type nativeListenerAdapter struct{}

var (
	_ native.Adapter       = (*nativeListenerAdapter)(nil)
	_ native.PacketAdapter = (*nativeListenerAdapter)(nil)
)

// NativeListenerAdapter returns the adapter that serves moq listeners with moq backends
func NativeListenerAdapter() native.Adapter {
	return &nativeListenerAdapter{}
}

func (*nativeListenerAdapter) Protocol() string { return listenerconfig.ProtocolMoQ }

func (*nativeListenerAdapter) ServesProvider(provider string) bool { return provider == providers.MoQ }

func (*nativeListenerAdapter) Providers() []string { return []string{providers.MoQ} }

// SupportsHTTP is true because a moq backend accepts WebTransport sessions on HTTP listeners with HTTP/3
func (*nativeListenerAdapter) SupportsHTTP(string) bool { return true }

func (*nativeListenerAdapter) Configured(o *listenerconfig.Options) bool {
	return o != nil && o.MoQ != nil
}

func (*nativeListenerAdapter) ValidateListener(o *listenerconfig.Options) error {
	if o == nil {
		return errors.New("nil moq listener options")
	}
	if o.MoQ == nil {
		o.MoQ = mo.NewListener()
	}
	return o.MoQ.Validate()
}

func (*nativeListenerAdapter) ValidateBackend(o *bo.Options) error {
	if o == nil {
		return errors.New("nil moq backend options")
	}
	if o.MoQ == nil {
		o.MoQ = mo.New()
	}
	if err := o.MoQ.Validate(); err != nil {
		return err
	}
	webTransportPaths(o)
	if o.OriginURL != "" && o.MoQ.Upstream.ALB != "" {
		return errors.New("origin_url and moq.upstream.alb are exclusive")
	}
	if o.OriginURL == "" {
		return nil
	}
	u, err := parseOrigin(o.OriginURL)
	if err != nil {
		return fmt.Errorf("moq origin_url: %w", err)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("moq origin_url %q names no host", o.OriginURL)
	}
	return nil
}

func webTransportPaths(o *bo.Options) {
	// a path accepts sessions under it, or on an HTTP listener those its pattern matches; an HTTP proxy path has no
	// meaning without an HTTP origin, while a local handler keeps its own route
	for _, p := range o.Paths {
		if p == nil {
			continue
		}
		switch p.HandlerName {
		case "", providers.Proxy, providers.MoQ:
			p.HandlerName, p.Methods = providers.MoQ, []string{http.MethodConnect}
			if p.MatchType != matching.PathMatchTypeRegex {
				p.MatchType, p.MatchTypeName = matching.PathMatchTypeSegment, matching.PathMatchNameSegment
			}
		}
	}
}

func (*nativeListenerAdapter) ValidateUserRouter(*config.Config, string, *bo.Options) error {
	return errors.New("moq user routing is not supported")
}

func (*nativeListenerAdapter) ValidateBalancer(*config.Config, string, *bo.Options) error {
	return errors.New("moq session balancing is not supported")
}

func (a *nativeListenerAdapter) Describe(c *config.Config, listenerName string) (native.Descriptor, error) {
	// the listener's restart identity is its moq block alone, since backends hot-swap under sessions
	if len(boundBackends(c, listenerName)) == 0 {
		return native.Descriptor{}, errNoBackend
	}
	o := c.Listeners[listenerName]
	if o == nil {
		return native.Descriptor{}, errNoBackend
	}
	return native.Descriptor{RestartKey: restartKey(o.MoQ)}, nil
}

func restartKey(o *mo.ListenerOptions) string {
	if o == nil {
		o = mo.NewListener()
	}
	var b strings.Builder
	for _, d := range o.Drafts {
		b.WriteString(strconv.Itoa(d))
		b.WriteByte(',')
	}
	fmt.Fprintf(&b, "|%d|%d|%d|%d|%d|%d|%s", o.HandshakeTimeout, o.IdleTimeout, o.MaxSessions,
		o.MaxRequestsPerSession, o.MaxFilterRanges, o.MaxRequestUpdates, o.Implementation)
	return checksum.Checksum(b.String())
}

func (*nativeListenerAdapter) Build(native.BuildRequest) (listener.ProtocolServer, error) {
	return nil, errNotPacket
}

func (*nativeListenerAdapter) RouteResolver(request native.BuildRequest) backends.RouteResolver {
	return routesFor(request)
}

// Join adds a moq listener to the QUIC endpoint at its address, sharing it with any HTTP/3 or other moq listener
func (*nativeListenerAdapter) Join(request native.BuildRequest, name string, certs tr.CertSwapper,
	tlsConfig *tls.Config,
) (listener.PacketMember, error) {
	o := request.Listener
	if o == nil {
		return nil, errNoBackend
	}
	if o.MoQ == nil {
		o.MoQ = mo.NewListener()
	}
	srv := newServer(request.ListenerName, o.MoQ, restartKey(o.MoQ), routesFor(request))
	srv.ipacl.Store(o.IPACL)
	return quicmux.Join(o.ListenAddress, o.ListenPort, quicmux.MemberOptions{
		Name: name, ALPNs: codec.ALPNs(o.MoQ.Drafts), Handler: srv, Certs: certs, TLS: tlsConfig,
		QUIC: QUICConfig(o.MoQ),
	})
}

// ServesWebTransport reports whether moq backends use the HTTP listener, so its HTTP/3 endpoint must accept
// WebTransport sessions
func ServesWebTransport(c *config.Config, listenerName string) bool {
	return len(boundBackends(c, listenerName)) > 0
}

func boundBackends(c *config.Config, listenerName string) []string {
	// an alb's pool member may carry the listener's name too, but is reached through its pool, never by routing
	if c == nil {
		return nil
	}
	members := c.Backends.PoolMembers()
	var out []string
	for _, name := range slices.Sorted(maps.Keys(c.Backends)) {
		o := c.Backends[name]
		if o != nil && strings.EqualFold(o.Provider, providers.MoQ) && o.UsesListener(listenerName) &&
			!members.Contains(name) {
			out = append(out, name)
		}
	}
	return out
}

type sessionGater interface {
	SessionGate() backends.SessionGate
}

func geoGate(o *geoaclopts.Options) backends.SessionGate {
	// a geo ACL is compiled before listeners are built or updated
	if o == nil {
		return nil
	}
	if g, ok := o.Compiled.(sessionGater); ok {
		return g.SessionGate()
	}
	return nil
}

func pathJudge(backend sessionJudge, p *po.Options) sessionJudge {
	// a path's acl replaces its backend's, and none clears it
	j := backend
	switch {
	case p.IPACLName == reserved.ReferenceNone:
		j.ipacl = nil
	case p.IPACL != nil:
		j.ipacl = p.IPACL
	}
	switch {
	case p.GeoACLName == reserved.ReferenceNone:
		j.geo = nil
	case p.GeoACLOptions != nil:
		j.geo = geoGate(p.GeoACLOptions)
	}
	return j
}

func routesFor(request native.BuildRequest) *routeTable {
	rt := &routeTable{}
	if l := request.Listener; l != nil && l.MoQ != nil {
		// read when the listener stops, so a reload changes it without restarting the listener
		rt.goAway = l.MoQ.GoAwayURI
	}
	for _, name := range boundBackends(request.Config, request.ListenerName) {
		client := request.BackendClients.Get(name)
		if client == nil {
			continue
		}
		o := request.Config.Backends[name]
		e := routeEntry{name: name, client: client, judge: sessionJudge{ipacl: o.IPACL, geo: geoGate(o.GeoACLOptions)}}
		for _, h := range o.Hosts {
			e.hosts = append(e.hosts, strings.ToLower(strings.TrimSuffix(h, ".")))
		}
		var patterns int
		for _, p := range o.Paths {
			// a local handler's path answers HTTP requests and routes no session, nor does a pattern routing
			// WebTransport sessions alone
			switch {
			case p == nil || p.Path == "" || handlers.IsLocal(p.HandlerName):
			case p.MatchType == matching.PathMatchTypeRegex:
				patterns++
			default:
				e.paths = append(e.paths, p.Path)
				e.judges = append(e.judges, pathJudge(e.judge, p))
			}
		}
		if patterns > 0 && len(e.paths) == 0 {
			continue
		}
		rt.entries = append(rt.entries, e)
	}
	return rt
}
