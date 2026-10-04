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

// Package quicmux shares one QUIC endpoint (a UDP socket and its transport) among the listeners bound to one
// address, handing each connection to the listener whose protocol the TLS handshake negotiated (by ALPN).
package quicmux

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"

	"github.com/quic-go/quic-go"
)

const (
	codeNoProtocol   quic.ApplicationErrorCode = 0
	reasonNoProtocol                           = "protocol not served"
)

var (
	// ErrNoALPN is returned when a member names no protocol to serve
	ErrNoALPN = errors.New("quicmux: a member must serve at least one protocol")
	// ErrNoHandler is returned when a member has no handler
	ErrNoHandler = errors.New("quicmux: a member must have a handler")
	// ErrNoCerts is returned when a member has no certificate source; QUIC is TLS-only
	ErrNoCerts = errors.New("quicmux: a member must have a certificate source")
	// ErrALPNInUse is returned when a protocol is already served by another member of the endpoint
	ErrALPNInUse = errors.New("quicmux: protocol already served at this address")
)

// Handler serves the connections an endpoint hands to one listener
type Handler interface {
	// ServeQUIC serves one connection and returns when it is done; the handler owns and closes the connection
	ServeQUIC(*quic.Conn)
	// Shutdown asks the handler's connections to finish, closing whatever remains once ctx is done
	Shutdown(context.Context) error
}

// MemberOptions describes one listener's share of an endpoint
type MemberOptions struct {
	// Name identifies the listener in errors and logs
	Name string
	// ALPNs are the protocols the listener serves, most preferred first; no two members may share one
	ALPNs []string
	// Handler serves the member's connections
	Handler Handler
	// Certs selects the member's certificates
	Certs sw.CertSwapper
	// TLS carries the listener's TLS settings; certificates come from Certs and protocols from ALPNs
	TLS *tls.Config
	// QUIC carries the transport settings the listener needs; an endpoint serves the widest of its members'
	QUIC *quic.Config
}

func (o *MemberOptions) validate() error {
	switch {
	case len(o.ALPNs) == 0:
		return ErrNoALPN
	case o.Handler == nil:
		return ErrNoHandler
	case o.Certs == nil:
		return ErrNoCerts
	}
	return nil
}

// Member is one listener's share of an endpoint
type Member struct {
	opts      MemberOptions
	ep        *endpoint
	left      chan struct{}
	leaveOnce sync.Once
	leaveErr  error
}

// Join adds a member to the endpoint bound at address:port, binding one when none is; a port of 0 always binds
// a new, unshared endpoint
func Join(address string, port int, o MemberOptions) (*Member, error) {
	return endpoints.join(address, port, o, func() (net.PacketConn, error) {
		return listener.NewPacketListener(address, port)
	})
}

// Serve blocks until the member leaves the endpoint, returning the endpoint's error if it failed first
func (m *Member) Serve() error {
	select {
	case <-m.left:
		return nil
	case <-m.ep.done:
	}
	select {
	case <-m.left:
		return nil
	default:
		// a last member that is still draining has already stopped the accept loop, which is no failure
		return m.ep.err
	}
}

// Shutdown leaves the endpoint, draining the member's connections until ctx is done; the endpoint releases
// its socket when its last member leaves
func (m *Member) Shutdown(ctx context.Context) error {
	return m.leave(ctx)
}

// Close leaves the endpoint at once, closing the member's connections
func (m *Member) Close() error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return m.leave(ctx)
}

// Handler returns the member's handler, so a reload can reach a running protocol server
func (m *Member) Handler() Handler {
	return m.opts.Handler
}

// LocalAddr returns the address the member's endpoint is bound to
func (m *Member) LocalAddr() net.Addr {
	return m.ep.conn.LocalAddr()
}

func (m *Member) leave(ctx context.Context) error {
	m.leaveOnce.Do(func() {
		last := endpoints.remove(m)
		if last {
			// stop accepting before the drain; open connections keep running until the endpoint closes
			_ = m.ep.ln.Close()
		}
		m.leaveErr = m.opts.Handler.Shutdown(ctx)
		if last {
			m.ep.close()
		}
		close(m.left)
	})
	return m.leaveErr
}

type registry struct {
	mu        sync.Mutex // guards endpoints and every endpoint's members
	endpoints map[string]*endpoint
}

var endpoints = &registry{endpoints: make(map[string]*endpoint)}

func endpointKey(address string, port int) string {
	// an ephemeral port is never shared: two listeners asking for port 0 want two sockets
	if port <= 0 {
		return ""
	}
	return net.JoinHostPort(address, strconv.Itoa(port))
}

func (r *registry) join(address string, port int, o MemberOptions,
	bind func() (net.PacketConn, error),
) (*Member, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := endpointKey(address, port)
	if e := r.endpoints[key]; key != "" && e != nil {
		if err := e.conflicts(o); err != nil {
			return nil, err
		}
		return e.add(o), nil
	}
	conn, err := bind()
	if err != nil {
		return nil, err
	}
	e, err := newEndpoint(key, conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	m := e.add(o)
	if key != "" {
		r.endpoints[key] = e
	}
	go e.serve()
	return m, nil
}

func (r *registry) remove(m *Member) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := m.ep
	e.members = slices.DeleteFunc(e.members, func(x *Member) bool { return x == m })
	e.publish()
	if len(e.members) > 0 {
		return false
	}
	// the last member's endpoint is unregistered at once, so no later listener joins a closing socket
	if e.key != "" && r.endpoints[e.key] == e {
		delete(r.endpoints, e.key)
	}
	return true
}

type endpoint struct {
	key       string
	conn      net.PacketConn
	transport *quic.Transport
	ln        *quic.EarlyListener
	members   []*Member // guarded by registry.mu
	snap      atomic.Pointer[snapshot]
	done      chan struct{}
	err       error // read only after done is closed
	closeOnce sync.Once
}

type snapshot struct {
	tls     *tls.Config
	quic    *quic.Config
	byALPN  map[string]*Member
	members []*Member
}

func newEndpoint(key string, conn net.PacketConn) (*endpoint, error) {
	e := &endpoint{
		key:       key,
		conn:      conn,
		transport: &quic.Transport{Conn: conn},
		done:      make(chan struct{}),
	}
	e.publish()
	// every handshake reads the current snapshot, so membership can change without rebinding the socket
	ln, err := e.transport.ListenEarly(&tls.Config{
		MinVersion:         tls.VersionTLS13,
		GetConfigForClient: e.tlsConfigFor,
	}, &quic.Config{GetConfigForClient: e.quicConfigFor})
	if err != nil {
		_ = e.transport.Close()
		return nil, err
	}
	e.ln = ln
	return e, nil
}

func (e *endpoint) conflicts(o MemberOptions) error {
	for _, m := range e.members {
		for _, p := range o.ALPNs {
			if slices.Contains(m.opts.ALPNs, p) {
				return ErrALPNInUse
			}
		}
	}
	return nil
}

func (e *endpoint) add(o MemberOptions) *Member {
	m := &Member{opts: o, ep: e, left: make(chan struct{})}
	e.members = append(e.members, m)
	e.publish()
	return m
}

func (e *endpoint) publish() {
	// a published snapshot is never modified, since handshakes and dispatches in flight may still read it
	s := &snapshot{
		byALPN:  make(map[string]*Member),
		members: slices.Clone(e.members),
		quic:    mergeQUIC(e.members),
	}
	tc := &tls.Config{}
	if len(e.members) > 0 && e.members[0].opts.TLS != nil {
		tc = e.members[0].opts.TLS.Clone()
	}
	tc.MinVersion = tls.VersionTLS13
	tc.Certificates = nil
	tc.NextProtos = nil
	tc.GetConfigForClient = nil
	tc.GetCertificate = e.getCertificate
	for _, m := range e.members {
		for _, p := range m.opts.ALPNs {
			s.byALPN[p] = m
			tc.NextProtos = append(tc.NextProtos, p)
		}
	}
	s.tls = tc
	e.snap.Store(s)
}

func (e *endpoint) tlsConfigFor(*tls.ClientHelloInfo) (*tls.Config, error) {
	return e.snap.Load().tls, nil
}

func (e *endpoint) quicConfigFor(*quic.ClientInfo) (*quic.Config, error) {
	return e.snap.Load().quic, nil
}

func (e *endpoint) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s := e.snap.Load()
	switch len(s.members) {
	case 0:
		return nil, sw.ErrNoCertificates
	case 1:
		return s.members[0].opts.Certs.GetCert(hello)
	}
	// a certificate names a host rather than a protocol, so the member whose store names the requested host
	// serves it; then the member of the first protocol the client offers, then the first member
	for _, m := range s.members {
		if store, ok := m.opts.Certs.(sw.CertStore); ok {
			if c := store.Match(hello.ServerName); c != nil {
				return c, nil
			}
		}
	}
	for _, p := range hello.SupportedProtos {
		if m := s.byALPN[p]; m != nil {
			return m.opts.Certs.GetCert(hello)
		}
	}
	return s.members[0].opts.Certs.GetCert(hello)
}

func (e *endpoint) serve() {
	defer close(e.done)
	for {
		conn, err := e.ln.Accept(context.Background())
		if err != nil {
			if !errors.Is(err, quic.ErrServerClosed) {
				e.err = err
			}
			return
		}
		m := e.snap.Load().byALPN[conn.ConnectionState().TLS.NegotiatedProtocol]
		if m == nil {
			// its listener left between the handshake and now
			_ = conn.CloseWithError(codeNoProtocol, reasonNoProtocol)
			continue
		}
		go m.opts.Handler.ServeQUIC(conn)
	}
}

func (e *endpoint) close() {
	e.closeOnce.Do(func() {
		_ = e.ln.Close()
		_ = e.transport.Close()
		// a transport closes only the sockets it opened itself, and this one was handed to it
		_ = e.conn.Close()
		<-e.done
	})
}

func mergeQUIC(members []*Member) *quic.Config {
	// an endpoint serves the widest settings across its members: the largest limits and windows, any feature
	// one needs, and the most frequent keep-alive; 0-RTT is never accepted, since it permits replay
	out := &quic.Config{}
	for _, m := range members {
		q := m.opts.QUIC
		if q == nil {
			continue
		}
		out.HandshakeIdleTimeout = max(out.HandshakeIdleTimeout, q.HandshakeIdleTimeout)
		out.MaxIdleTimeout = max(out.MaxIdleTimeout, q.MaxIdleTimeout)
		if q.KeepAlivePeriod > 0 && (out.KeepAlivePeriod == 0 || q.KeepAlivePeriod < out.KeepAlivePeriod) {
			out.KeepAlivePeriod = q.KeepAlivePeriod
		}
		out.MaxIncomingStreams = max(out.MaxIncomingStreams, q.MaxIncomingStreams)
		out.MaxIncomingUniStreams = max(out.MaxIncomingUniStreams, q.MaxIncomingUniStreams)
		out.InitialStreamReceiveWindow = max(out.InitialStreamReceiveWindow, q.InitialStreamReceiveWindow)
		out.MaxStreamReceiveWindow = max(out.MaxStreamReceiveWindow, q.MaxStreamReceiveWindow)
		out.InitialConnectionReceiveWindow = max(out.InitialConnectionReceiveWindow,
			q.InitialConnectionReceiveWindow)
		out.MaxConnectionReceiveWindow = max(out.MaxConnectionReceiveWindow, q.MaxConnectionReceiveWindow)
		out.EnableDatagrams = out.EnableDatagrams || q.EnableDatagrams
		out.EnableStreamResetPartialDelivery = out.EnableStreamResetPartialDelivery ||
			q.EnableStreamResetPartialDelivery
	}
	out.Allow0RTT = false
	return out
}
