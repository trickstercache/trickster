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

package l4

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// Listener protocols served by this package.
const (
	ProtocolTCP = "tcp"
	ProtocolTLS = "tls"
	ProtocolUDP = "udp"
)

// Connection results, as the connections metric labels them.
const (
	ResultProxied    = "proxied"
	ResultNotTLS     = "not_tls"
	ResultNoRoute    = "no_route"
	ResultNoUpstream = "no_upstream"
	ResultDialFailed = "dial_failed"
	ResultRefused    = "refused"
)

// Byte directions, as the bytes metric labels them.
const (
	DirectionIn  = "in"
	DirectionOut = "out"
)

// copyBufferSize is the relay's read size in each direction.
const copyBufferSize = 32 * 1024

// ErrServerClosed is returned by Serve after Shutdown or Close.
var ErrServerClosed = errors.New("l4: server closed")

// Config is what a server routes with; a reload swaps the whole value at once.
type Config struct {
	// Table selects the upstream for each connection
	Table *Table
	// Options tunes the connect and idle timeouts
	Options *options.Options
	// MaxConnections bounds the connections relayed at once, or the UDP sessions open at once;
	// zero applies no bound to connections and the default bound to sessions
	MaxConnections int
}

func (c *Config) table() *Table {
	if c == nil {
		return nil
	}
	return c.Table
}

func (c *Config) options() *options.Options {
	if c == nil {
		return nil
	}
	return c.Options
}

func (c *Config) maxConnections() int {
	if c == nil {
		return 0
	}
	return c.MaxConnections
}

// Server relays accepted connections to upstream members, choosing the member by the TLS server
// name on a tls listener and by the table's catch-all on a tcp listener.
type Server struct {
	name     string
	protocol string
	cfg      atomic.Pointer[Config]
	// ctx ends every pending upstream dial when the server is closed
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	// slot wakes an accept waiting on the connection bound when a connection ends
	slot     sync.Cond
	listener net.Listener
	// conns are the client connections being relayed and upstreams the connections they are
	// relayed to; both are closed by Close, since a relay blocked on either side must end
	conns     map[net.Conn]struct{}
	upstreams map[net.Conn]struct{}
	closed    bool
	workers   sync.WaitGroup
}

// NewServer returns a server for a listener of the tcp or tls protocol.
func NewServer(name, protocol string, cfg *Config) *Server {
	s := &Server{
		name: name, protocol: protocol,
		conns: make(map[net.Conn]struct{}), upstreams: make(map[net.Conn]struct{}),
	}
	s.slot.L = &s.mu
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.Update(cfg)
	return s
}

// Update swaps the routing table and timeouts for connections accepted from now on; established
// connections keep the upstream they were relayed to.
func (s *Server) Update(cfg *Config) {
	if cfg == nil {
		cfg = &Config{}
	}
	s.cfg.Store(cfg)
	s.mu.Lock()
	s.slot.Broadcast()
	s.mu.Unlock()
}

// Serve accepts connections until the listener is closed or the server is shut down.
func (s *Server) Serve(l net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = l.Close()
		return ErrServerClosed
	}
	s.listener = l
	s.mu.Unlock()
	for {
		if !s.acquire() {
			return ErrServerClosed
		}
		conn, err := l.Accept()
		if err != nil {
			if s.isClosed() || errors.Is(err, net.ErrClosed) {
				return ErrServerClosed
			}
			return err
		}
		if !s.track(conn) {
			_ = conn.Close()
			return ErrServerClosed
		}
		go s.handle(conn)
	}
}

// acquire waits until the connection bound admits another connection, as a limited listener
// waits ahead of accepting; it returns false once the server is closed.
func (s *Server) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.closed {
			return false
		}
		if bound := s.cfg.Load().maxConnections(); bound <= 0 || len(s.conns) < bound {
			return true
		}
		s.slot.Wait()
	}
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	s.workers.Add(1)
	metrics.ProxyStreamActiveConnections.WithLabelValues(s.name, s.protocol).Inc()
	return true
}

func (s *Server) untrack(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.slot.Broadcast()
	s.mu.Unlock()
	metrics.ProxyStreamActiveConnections.WithLabelValues(s.name, s.protocol).Dec()
	s.workers.Done()
}

// trackUpstream registers an upstream connection so a force-close reaches it; the dial and the
// registration are decided under the lock the close takes, so no upstream is left untracked.
func (s *Server) trackUpstream(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.upstreams[conn] = struct{}{}
	return true
}

func (s *Server) untrackUpstream(conn net.Conn) {
	_ = conn.Close()
	s.mu.Lock()
	delete(s.upstreams, conn)
	s.mu.Unlock()
}

func (s *Server) result(r string) {
	metrics.ProxyStreamConnections.WithLabelValues(s.name, s.protocol, r).Inc()
}

func (s *Server) handle(client net.Conn) {
	defer s.untrack(client)
	cfg := s.cfg.Load()
	opts := cfg.options()
	var host string
	if s.protocol == ProtocolTLS {
		var err error
		host, client, err = peekClientHello(client, opts.Connect())
		if err != nil {
			s.result(ResultNotTLS)
			return
		}
	}
	up := cfg.table().Lookup(host)
	if up == nil {
		s.result(ResultNoRoute)
		return
	}
	addr, ok := up.Addr()
	if !ok {
		s.result(ResultNoUpstream)
		return
	}
	dialer := net.Dialer{Timeout: opts.Connect()}
	upstream, err := dialer.DialContext(s.ctx, "tcp", addr)
	if err != nil {
		s.result(ResultDialFailed)
		return
	}
	if !s.trackUpstream(upstream) {
		_ = upstream.Close()
		s.result(ResultRefused)
		return
	}
	defer s.untrackUpstream(upstream)
	s.result(ResultProxied)
	in, out := relay(client, upstream, opts.Idle())
	metrics.ProxyStreamBytes.WithLabelValues(s.name, s.protocol, DirectionIn).Add(float64(in))
	metrics.ProxyStreamBytes.WithLabelValues(s.name, s.protocol, DirectionOut).Add(float64(out))
}

// Shutdown stops accepting and waits for relayed connections to end until ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.slot.Broadcast()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting, ends every pending dial and closes both sides of every relay.
func (s *Server) Close() error {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	if s.listener != nil {
		_ = s.listener.Close()
	}
	for conn := range s.conns {
		_ = conn.Close()
	}
	for conn := range s.upstreams {
		_ = conn.Close()
	}
	s.slot.Broadcast()
	s.mu.Unlock()
	s.workers.Wait()
	return nil
}

// ActiveConnections reports how many relayed connections are open.
func (s *Server) ActiveConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// relay copies both directions until each has ended, returning the bytes moved from the client
// and to it; a side that ends cleanly half-closes its peer, and a side that fails closes both.
// The idle timeout is the connection's: a direction that has read nothing for that long ends
// the relay only when the other has moved nothing either.
func relay(client, upstream net.Conn, idle time.Duration) (int64, int64) {
	var in, out int64
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Go(func() { in = pipe(upstream, client, idle, &last) })
	wg.Go(func() { out = pipe(client, upstream, idle, &last) })
	wg.Wait()
	return in, out
}

var bufPool = sync.Pool{New: func() any {
	b := make([]byte, copyBufferSize)
	return &b
}}

func pipe(dst, src net.Conn, idle time.Duration, last *atomic.Int64) int64 {
	bp := bufPool.Get().(*[]byte)
	defer bufPool.Put(bp)
	buf := *bp
	var n int64
	for {
		if idle > 0 {
			_ = src.SetReadDeadline(time.Now().Add(idle))
		}
		nr, rerr := src.Read(buf)
		if nr > 0 {
			now := time.Now()
			last.Store(now.UnixNano())
			if idle > 0 {
				// a write blocked for the whole idle period is a stalled receiver
				_ = dst.SetWriteDeadline(now.Add(idle))
			}
			nw, werr := dst.Write(buf[:nr])
			n += int64(nw)
			if werr != nil {
				_ = src.Close()
				_ = dst.Close()
				return n
			}
		}
		if rerr == nil {
			continue
		}
		var ne net.Error
		if errors.As(rerr, &ne) && ne.Timeout() && idle > 0 &&
			time.Since(time.Unix(0, last.Load())) < idle {
			// the other direction moved within the idle period, so the connection is not idle
			continue
		}
		if errors.Is(rerr, io.EOF) {
			// the source finished sending; the destination may still answer
			if closeWrite(dst) != nil {
				_ = dst.Close()
			}
			return n
		}
		_ = src.Close()
		_ = dst.Close()
		return n
	}
}

// halfCloser is a connection that can end its write side alone.
type halfCloser interface {
	CloseWrite() error
}

func closeWrite(c net.Conn) error {
	if hc, ok := c.(halfCloser); ok {
		return hc.CloseWrite()
	}
	return errors.ErrUnsupported
}
