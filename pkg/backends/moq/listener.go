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
	"net/netip"
	"net/url"
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
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/format"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/quicmux"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/normalize"

	"github.com/quic-go/quic-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

	// a native session's access log line names the message that opened it, as an HTTP one names its method
	methodSetup = "SETUP"

	spanSession       = "moq session"
	attrMoQBackend    = "moq.backend"
	attrMoQAuthority  = "moq.authority"
	attrMoQPath       = "moq.path"
	attrMoQEnd        = "moq.end"
	attrClientAddress = "client.address"
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
	judge  sessionJudge
	log    *accesslog.Logger
	tracer *tracing.Tracer
}

type sessionJudge struct {
	ipacl     *ipacl.List
	decisions *metrics.IPACLDecision
	geo       backends.SessionGate
}

func (j sessionJudge) refusal(client netip.Addr) (string, bool) {
	// a session routed to a backend passes the acls its HTTP requests would
	if j.ipacl != nil {
		allowed := j.ipacl.Check(client) == ipacl.Allow
		j.decisions.Observe(allowed)
		if !allowed {
			return "", true
		}
	}
	if j.geo != nil {
		if d := j.geo.Admit(client); d != nil {
			return d.Message, true
		}
	}
	return "", false
}

type routeTable struct {
	entries    []routeEntry
	goAway     string
	sessions   *limiter // the listener's, counting sessions per client
	decisions  *metrics.IPACLDecision
	normalizer *normalize.Normalizer
}

func (rt *routeTable) entry(backend string) *routeEntry {
	if rt == nil {
		return nil
	}
	for i := range rt.entries {
		if rt.entries[i].name == backend {
			return &rt.entries[i]
		}
	}
	return nil
}

var _ backends.RouteResolver = (*routeTable)(nil)

func (rt *routeTable) sessionLimiter() *limiter {
	if rt == nil {
		return nil
	}
	return rt.sessions
}

func (rt *routeTable) ResolveRoute(in backends.RouteInput) (backends.RouteDecision, bool) {
	_, d, ok := rt.resolve(in)
	return d, ok
}

func (rt *routeTable) resolve(in backends.RouteInput) (*routeEntry, backends.RouteDecision, bool) {
	// a backend naming the session's host wins, then one matching it by wildcard, then one naming no host, each by
	// its longest path prefix; a listener with one backend serves every session with it
	if rt == nil || len(rt.entries) == 0 {
		return nil, backends.RouteDecision{Outcome: backends.RouteOutcomeNoRoute}, false
	}
	if len(rt.entries) == 1 {
		return &rt.entries[0], backends.RouteDecision{
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
		return nil, backends.RouteDecision{Outcome: backends.RouteOutcomeNoRoute}, false
	}
	return &rt.entries[best], backends.RouteDecision{
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

func (s *server) ServeQUIC(conn *quic.Conn) {
	client := clientip.FromNetAddr(conn.RemoteAddr())
	// the endpoint refused a listed client before its handshake; this catches a list a reload swapped since
	if list := s.ipacl.Load(); list != nil && list.Check(client) != ipacl.Allow {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeUnauthorized), "")
		observeSession(s.listenerName, "", transportQUIC, "", resultRefused)
		return
	}
	if lim := s.routes.Load().sessionLimiter(); lim != nil {
		if ok, _ := lim.admit(lim.keyFor(client)); !ok {
			_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeInternalError), reasonRateLimited)
			observeSession(s.listenerName, "", transportQUIC, "", resultRefused)
			return
		}
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
	r := &routed{srv: s, addr: client, client: client.String(), sni: conn.ConnectionState().TLS.ServerName}
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
	start := time.Now()
	if err = sess.Start(context.Background()); err != nil {
		observeSession(s.listenerName, r.backendName(), transportQUIC, draft, r.result(resultFailed))
		r.done()
		// a session routed before its start failed still ends its span and is logged
		s.record(r, conn, sess, c, start)
		return
	}
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
	s.record(r, conn, sess, c, start)
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

func (s *server) admitPeer(remote net.Addr) bool {
	// a client the listener's list refuses is turned away before its handshake, counted and logged; nothing else is
	// judged there, since the address is not yet verified
	list := s.ipacl.Load()
	if list == nil {
		return true
	}
	client := clientip.FromNetAddr(remote)
	allowed := list.Check(client) == ipacl.Allow
	if rt := s.routes.Load(); rt != nil {
		rt.decisions.Observe(allowed)
	}
	if !allowed {
		observeSession(s.listenerName, "", transportQUIC, "", resultRefused)
		if logger.DebugEnabled() {
			logger.Debug("moq connection refused by the listener's ip acl", logging.Pairs{
				keys.ListenerName: s.listenerName, logKeyClient: client.String(),
			})
		}
	}
	return allowed
}

func (s *server) record(r *routed, conn *quic.Conn, sess *session.Session, c *codec.Codec, start time.Time) {
	// one access log line and one span per session, through the backend's newest logger, since a reload closes
	// the one the session began with
	r.mu.Lock()
	backend, authority, path, span := r.backend, r.authority, r.path, r.span
	r.span = nil
	r.mu.Unlock()
	if span != nil {
		if err := sess.Err(); err != nil {
			span.SetAttributes(attribute.String(attrMoQEnd, err.Error()))
		}
		span.End()
	}
	if backend == "" {
		return
	}
	e := s.routes.Load().entry(backend)
	if e == nil || e.log == nil {
		return
	}
	f := &format.Fields{
		StartTime: start, Duration: time.Since(start), ClientIP: r.client, RemoteIP: r.client, Method: methodSetup,
		RequestURI: path, Path: path, Host: authority, Proto: c.ALPN(),
	}
	if la := conn.LocalAddr(); la != nil {
		f.LocalIP, f.LocalPort, _ = net.SplitHostPort(la.String())
	}
	e.log.Log(f)
}

func (s *server) rejudge() {
	// a reload's lists reach the sessions already open: one they no longer admit is closed
	list, rt := s.ipacl.Load(), s.routes.Load()
	s.mu.Lock()
	sessions := maps.Clone(s.sessions)
	s.mu.Unlock()
	for sess, r := range sessions {
		if reason, denied := r.rejudged(list, rt); denied {
			_ = sess.Close(message.CodeUnauthorized, reason)
		}
	}
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

func (s *server) Shutdown(ctx context.Context) error {
	// each session gets a GOAWAY naming the goaway_uri, and what remains when ctx ends is closed; a reload that keeps
	// the listener never calls this
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

func (s *server) UpdateRouteResolver(r backends.RouteResolver) {
	// only new sessions are matched against the new routes; open ones keep the backend they have, and are judged
	// again by its new lists
	if rt, ok := r.(*routeTable); ok {
		s.routes.Store(rt)
		s.rejudge()
	}
}

func (s *server) UpdateSessionGate(g backends.SessionGate) {
	s.gate.Store(g)
}

func (s *server) UpdateIPACL(list *ipacl.List) {
	s.ipacl.Store(list)
	s.rejudge()
}

func (s *server) ProtocolRestartKey() string {
	return s.restartKey
}

type routed struct {
	departure
	srv     *server
	addr    netip.Addr
	client  string
	sni     string
	handler session.Handler

	mu        sync.Mutex
	backend   string
	authority string
	path      string
	span      trace.Span
	release   func()
	drop      func()
	refused   bool
	denied    bool
	finished  bool
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
	rt := r.srv.routes.Load()
	path, ok := rt.normalized(path)
	if !ok {
		r.mu.Lock()
		r.refused = !r.finished
		r.mu.Unlock()
		return &message.SessionError{Code: message.CodeMalformedPath, Reason: "the path is refused"}
	}
	e, d, ok := rt.resolve(backends.RouteInput{Authority: authority, Path: path})
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
	// the session is judged by the acls of the backend it reached, which a listener serving several backends cannot
	// judge before SETUP names one
	if reason, denied := e.judge.refusal(r.addr); denied {
		if d.Release != nil {
			d.Release()
		}
		r.denied = true
		if logger.DebugEnabled() {
			logger.Debug("moq session refused by its backend's acls", logging.Pairs{
				keys.ListenerName: r.srv.listenerName, keys.BackendName: e.name, logKeyClient: r.client,
			})
		}
		return &message.SessionError{Code: message.CodeUnauthorized, Reason: reason}
	}
	r.authority, r.path = authority, path
	if e.tracer != nil {
		_, r.span = e.tracer.Start(context.Background(), spanSession, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String(attrMoQBackend, e.name), attribute.String(attrMoQAuthority, authority),
				attribute.String(attrMoQPath, path), attribute.String(attrClientAddress, r.client)))
	}
	r.backend, r.release = rb.Name(), d.Release
	r.handler = rb.Relay().Handler(rb.Scope())
	if c, isClient := rb.(*Client); isClient {
		r.handler = limitRequests(r.handler, c.limiter, r.addr)
		if c.sh != nil {
			// the relay outlives a reload that retires it while this session uses it
			c.sh.hold()
			r.drop = c.sh.drop
		}
	}
	return nil
}

func (r *routed) rejudged(list *ipacl.List, rt *routeTable) (string, bool) {
	if list != nil && list.Check(r.addr) != ipacl.Allow {
		return "", true
	}
	// a session still being routed, or one whose backend the reload removed, has no new lists to meet
	if e := rt.entry(r.backendName()); e != nil {
		return e.judge.refusal(r.addr)
	}
	return "", false
}

func (rt *routeTable) normalized(path string) (string, bool) {
	// a SETUP PATH is normalized as the listener normalizes an HTTP request's, before it is routed
	if rt == nil || rt.normalizer == nil || !strings.HasPrefix(path, rootPath) {
		return path, true
	}
	// parsed as a request's target is, so a leading // stays part of the path rather than naming a host
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return path, true
	}
	if !rt.normalizer.Normalize(u) {
		return "", false
	}
	return u.Path, true
}

func (r *routed) backendName() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.backend
}

func (r *routed) result(otherwise string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.refused:
		return resultNoRoute
	case r.denied:
		return resultRefused
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
