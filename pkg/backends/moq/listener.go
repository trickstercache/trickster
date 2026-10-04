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
	"context"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/quicmux"

	"github.com/quic-go/quic-go"
)

const (
	rootPath       = "/"
	resultOK       = "ok"
	resultRefused  = "refused"
	resultNoRoute  = "no_route"
	resultBusy     = "busy"
	resultFailed   = "failed"
	transportQUIC  = "quic"
	roleDownstream = "downstream"
	logKeyClient   = "client"
	logKeyReason   = "reason"
	logKeyDuration = "duration"

	reasonShuttingDown = "shutting down"
)

type relayBackend interface {
	backends.Backend
	Relay() *relay.Relay
	Scope() relay.Scope
}

type routeEntry struct {
	name   string
	client backends.Backend
	hosts  []string
	paths  []string
}

type routeTable struct {
	entries []routeEntry
	goAway  string
}

var _ backends.RouteResolver = (*routeTable)(nil)

// ResolveRoute prefers a backend naming the session's host, then one matching it by wildcard, then one naming no
// host, and among those the longest path prefix; a listener with one backend serves every session with it
func (rt *routeTable) ResolveRoute(in backends.RouteInput) (backends.RouteDecision, bool) {
	if rt == nil || len(rt.entries) == 0 {
		return backends.RouteDecision{Outcome: backends.RouteOutcomeNoRoute}, false
	}
	if len(rt.entries) == 1 {
		return backends.RouteDecision{
			Target:  backends.RouteTarget{Backend: rt.entries[0].client},
			Outcome: backends.RouteOutcomeDefault,
		}, true
	}
	host := hostOnly(in.Authority)
	path := in.Path
	if path == "" {
		path = rootPath
	}
	best, bestScore := -1, -1
	for i, e := range rt.entries {
		rank := hostRank(e.hosts, host)
		if rank < 0 {
			continue
		}
		plen := longestPrefix(e.paths, path)
		if plen < 0 {
			continue
		}
		if score := plen + rank<<20; score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return backends.RouteDecision{Outcome: backends.RouteOutcomeNoRoute}, false
	}
	return backends.RouteDecision{
		Target:  backends.RouteTarget{Backend: rt.entries[best].client},
		Outcome: backends.RouteOutcomeSelected,
	}, true
}

func hostRank(hosts []string, host string) int {
	// 2 names the host, 1 matches it by wildcard, 0 names no host at all, and -1 does not match
	if len(hosts) == 0 {
		return 0
	}
	if slices.Contains(hosts, host) {
		return 2
	}
	for _, h := range hosts {
		if hostnames.IsWildcard(h) && hostnames.Overlap(h, host) {
			return 1
		}
	}
	return -1
}

func hostOnly(authority string) string {
	if h, _, err := net.SplitHostPort(authority); err == nil {
		authority = h
	}
	return strings.ToLower(strings.TrimSuffix(authority, "."))
}

func longestPrefix(prefixes []string, path string) int {
	// a prefix matches the path itself or a path below it, never a sibling that merely starts with it
	best := -1
	if len(prefixes) == 0 {
		prefixes = []string{rootPath}
	}
	for _, p := range prefixes {
		switch {
		case p == path, p == rootPath:
		case strings.HasPrefix(path, p) && (strings.HasSuffix(p, rootPath) || path[len(p)] == '/'):
		default:
			continue
		}
		best = max(best, len(p))
	}
	return best
}

type server struct {
	listenerName string
	opts         *mo.ListenerOptions
	restartKey   string
	ipacl        atomic.Pointer[ipacl.List]
	routes       atomic.Pointer[routeTable]
	gate         backends.SessionGateSlot

	mu       sync.Mutex
	sessions map[*session.Session]*routed
	closing  bool
	active   atomic.Int64
	wg       sync.WaitGroup
}

var _ quicmux.Handler = (*server)(nil)

func newServer(listenerName string, o *mo.ListenerOptions, restartKey string, rt *routeTable) *server {
	s := &server{
		listenerName: listenerName, opts: o, restartKey: restartKey, sessions: make(map[*session.Session]*routed),
	}
	s.routes.Store(rt)
	return s
}

// QUICConfig returns the transport settings a moq listener needs from the endpoint serving it
func QUICConfig(o *mo.ListenerOptions) *quic.Config {
	return &quic.Config{
		EnableDatagrams: true, EnableStreamResetPartialDelivery: true, MaxIdleTimeout: time.Duration(o.IdleTimeout),
	}
}

// ServeQUIC serves one MoQ session on a connection the listener's endpoint accepted
func (s *server) ServeQUIC(conn *quic.Conn) {
	client := clientip.FromNetAddr(conn.RemoteAddr())
	if list := s.ipacl.Load(); list != nil && list.Check(client) != ipacl.Allow {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeUnauthorized), "")
		observeSession(s.listenerName, "", transportQUIC, "", resultRefused)
		return
	}
	if d := s.gate.Admit(client); d != nil {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeUnauthorized), d.Message)
		observeSession(s.listenerName, "", transportQUIC, "", resultRefused)
		return
	}
	c, ok := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	if !ok {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeVersionNegotiation), "no supported draft")
		observeSession(s.listenerName, "", transportQUIC, "", resultRefused)
		return
	}
	if !s.admit() {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeInternalError), "session limit reached")
		observeSession(s.listenerName, "", transportQUIC, strconv.Itoa(c.Draft()), resultBusy)
		return
	}
	defer s.wg.Done()
	defer s.active.Add(-1)
	r := &routed{srv: s, client: client.String(), sni: conn.ConnectionState().TLS.ServerName}
	sess, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleServer, Codec: c, Handler: r, OnSetup: r.route,
		HandshakeTimeout: time.Duration(s.opts.HandshakeTimeout), MaxRequests: s.opts.MaxRequestsPerSession,
		Setup: message.Setup{
			Implementation:    implementation(s.opts.Implementation),
			MaxFilterRanges:   uint64(s.opts.MaxFilterRanges),   // #nosec G115 -- validated as not negative
			MaxRequestUpdates: uint64(s.opts.MaxRequestUpdates), // #nosec G115 -- validated as not negative
		},
	})
	if err != nil {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeInternalError), "")
		return
	}
	if !s.track(sess, r) {
		_ = sess.Close(message.CodeNoError, reasonShuttingDown)
		return
	}
	defer s.untrack(sess)
	draft := strconv.Itoa(c.Draft())
	if err = sess.Start(context.Background()); err != nil {
		observeSession(s.listenerName, r.backendName(), transportQUIC, draft, r.result(resultFailed))
		r.done()
		return
	}
	start := time.Now()
	backend := r.backendName()
	observeSession(s.listenerName, backend, transportQUIC, draft, resultOK)
	activeSessions(s.listenerName, backend, 1)
	if logger.DebugEnabled() {
		logger.Debug("moq session opened", logging.Pairs{
			keys.ListenerName: s.listenerName, keys.BackendName: backend, keys.Draft: draft, logKeyClient: r.client,
			keys.Transport: transportQUIC,
		})
	}
	<-sess.Done()
	activeSessions(s.listenerName, backend, -1)
	r.done()
	logger.Info("moq session closed", logging.Pairs{
		keys.ListenerName: s.listenerName, keys.BackendName: backend, keys.Draft: draft, logKeyClient: r.client,
		keys.Transport: transportQUIC, logKeyDuration: time.Since(start).String(), logKeyReason: sessionEnd(sess.Err()),
	})
}

func sessionEnd(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *server) admit() bool {
	if s.active.Add(1) > int64(s.opts.MaxSessions) {
		s.active.Add(-1)
		return false
	}
	s.wg.Add(1)
	return true
}

func (s *server) track(sess *session.Session, r *routed) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return false
	}
	s.sessions[sess] = r
	return true
}

func (s *server) untrack(sess *session.Session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

// Shutdown sends each session a GOAWAY naming the listener's goaway_uri, then waits until ctx ends for the clients
// to move before closing what remains; it is called only when the listener stops, never on a reload that keeps it
func (s *server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	sessions := maps.Clone(s.sessions)
	s.mu.Unlock()
	uri := ""
	if rt := s.routes.Load(); rt != nil {
		uri = rt.goAway
	}
	var timeout time.Duration
	deadline, bounded := ctx.Deadline()
	if bounded {
		timeout = max(time.Until(deadline), 0)
	}
	for sess, r := range sessions {
		r.leave(uri)
		if err := sess.GoAway(uri, timeout); err != nil || !bounded {
			// a session that cannot hear GOAWAY has nothing to migrate, and with no time to drain the GOAWAY only
			// tells the client where to reconnect
			_ = sess.Close(message.CodeNoError, reasonShuttingDown)
		}
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	// §6.6.1: a client still connected when the GOAWAY's timeout passes is closed with GOAWAY_TIMEOUT
	for sess := range sessions {
		_ = sess.Close(message.CodeGoAwayTimeout, reasonShuttingDown)
	}
	return ctx.Err()
}

// UpdateRouteResolver swaps the routes new sessions are matched with; sessions keep the backend they have
func (s *server) UpdateRouteResolver(r backends.RouteResolver) {
	if rt, ok := r.(*routeTable); ok {
		s.routes.Store(rt)
	}
}

// UpdateSessionGate swaps the gate new connections are judged by
func (s *server) UpdateSessionGate(g backends.SessionGate) {
	s.gate.Store(g)
}

// UpdateIPACL swaps the access list new connections are judged by
func (s *server) UpdateIPACL(list *ipacl.List) {
	s.ipacl.Store(list)
}

// ProtocolRestartKey identifies the settings that need a new listener to change
func (s *server) ProtocolRestartKey() string {
	return s.restartKey
}

type routed struct {
	departure
	srv     *server
	client  string
	sni     string
	handler session.Handler

	mu       sync.Mutex
	backend  string
	release  func()
	drop     func()
	refused  bool
	finished bool
}

func (r *routed) route(_ *session.Session, setup *message.Setup) error {
	authority := setup.Authority
	if !setup.HasAuthority {
		authority = r.sni
	}
	path := setup.Path
	if !setup.HasPath {
		path = rootPath
	}
	d, ok := r.srv.routes.Load().ResolveRoute(backends.RouteInput{Authority: authority, Path: path})
	rb, isRelay := d.Target.Backend.(relayBackend)
	// the connection can close while its SETUP is routed, so the session's teardown may already have run
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ok || !isRelay || !d.Target.Available() || r.finished {
		if d.Release != nil {
			d.Release()
		}
		r.refused = !r.finished
		return &message.SessionError{Code: message.CodeInvalidPath, Reason: "no backend serves this path"}
	}
	r.backend, r.release = rb.Name(), d.Release
	r.handler = rb.Relay().Handler(rb.Scope())
	if c, isClient := rb.(*Client); isClient && c.sh != nil {
		// the relay outlives a reload that retires it while this session uses it
		c.sh.hold()
		r.drop = c.sh.drop
	}
	return nil
}

func (r *routed) backendName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.backend
}

func (r *routed) result(otherwise string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refused {
		return resultNoRoute
	}
	return otherwise
}

func (r *routed) done() {
	r.mu.Lock()
	r.finished = true
	release, drop := r.release, r.drop
	r.release, r.drop = nil, nil
	r.mu.Unlock()
	if release != nil {
		release()
	}
	if drop != nil {
		drop()
	}
}

func (r *routed) HandleRequest(req *session.Request) {
	if !r.refuses(req) {
		r.handler.HandleRequest(req)
	}
}

func (r *routed) HandleGoAway(s *session.Session, m *message.GoAway) {
	r.handler.HandleGoAway(s, m)
}
