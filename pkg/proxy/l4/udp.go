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
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// maxDatagram is the largest UDP payload a socket can carry.
const maxDatagram = 65535

// DefaultUDPMaxSessions bounds the client flows a udp listener relays at once when the listener
// sets no connection limit; each open flow holds an upstream socket, two workers and a reply buffer.
const DefaultUDPMaxSessions = 1024

// The bounds on flows that are not yet relaying, which anyone able to send a datagram can create:
// how many may be opening at once, how many upstreams may be resolved and dialed at once, how
// many bytes all opening flows may keep between them, how many datagrams one may keep, how many
// failed flows are remembered, and for how long.
const (
	maxOpeningFlows     = 128
	maxConcurrentDials  = 32
	pendingByteBudget   = 1 << 20
	maxPendingDatagrams = 4
	maxFailedFlows      = 256
)

// The bounds on what flows queue for their writers: how many datagrams one flow may hold, how
// many bytes every flow may hold between them, and how long one write may block before its
// datagram is dropped, since a writer's stall must not become the flow's memory.
const (
	maxQueuedDatagrams = 16
	udpWriteTimeout    = time.Second
)

// queuedByteBudget bounds the bytes every flow holds for its writer between them, the datagram
// each writer is writing included; tests lower it
var queuedByteBudget int64 = 8 << 20

// Reasons a datagram is dropped, as the dropped datagrams metric labels them.
const (
	DropQueueFull    = "queue_full"
	DropWriteTimeout = "write_timeout"
)

// failedFlowLifetime is how long a failed flow is remembered; tests shorten it
var failedFlowLifetime = 5 * time.Second

// resolvedTTL is how long a resolved upstream host is reused before it is looked up again.
const resolvedTTL = 30 * time.Second

// flow states
const (
	flowOpening = iota
	flowOpen
	flowFailed
)

// PacketServer relays datagrams to an upstream member, keeping one upstream socket per client
// address so replies find their way back, and ending a session once it goes idle.
type PacketServer struct {
	name string
	cfg  atomic.Pointer[Config]
	// dial opens a flow's upstream socket to a resolved address and lookup resolves a host;
	// tests substitute ones that stall, fail or answer without the network
	dial   func(context.Context, string) (net.Conn, error)
	lookup func(context.Context, string) ([]net.IPAddr, error)
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	// dialSlot wakes a flow waiting to dial when a dial ends or the server closes
	dialSlot sync.Cond
	conn     net.PacketConn
	sessions map[string]*udpSession
	// opening, dialing and failed count the flows in each state; established flows alone are
	// held to the session bound, so flows that never relay cannot fill it
	opening  int
	dialing  int
	failed   int
	draining bool
	closed   bool
	// pending is the bytes every opening flow keeps between them, and queued the bytes every
	// flow, opening or open, holds for its writer
	pending atomic.Int64
	queued  atomic.Int64
	// resolved caches upstream host lookups, since many flows select one static upstream
	resolved map[string]resolvedAddr
	workers  sync.WaitGroup
}

// resolvedAddr is one cached lookup
type resolvedAddr struct {
	addr    string
	expires time.Time
}

// udpSession is one client's flow: its upstream socket once open, the datagrams queued for it,
// and when a datagram last moved. The receive loop only queues; the flow's writer alone writes,
// in order, so a blocked upstream stalls no other flow and nothing overtakes a queued datagram.
type udpSession struct {
	client net.Addr
	mu     sync.Mutex
	// wake rouses the writer when a datagram is queued or the flow closes
	wake   sync.Cond
	state  int
	closed bool
	// upstream is set once the flow is open, when its writer starts
	upstream net.Conn
	// set once the upstream is dialed; told when the session ends
	route Route
	// set when the upstream refused a datagram, which is how an unreachable udp upstream shows
	fault error
	// queue is a ring of the datagrams waiting for the writer, in arrival order
	queue     [maxQueuedDatagrams][]byte
	head, num int
	last      atomic.Int64
}

func newUDPSession(client net.Addr) *udpSession {
	sess := &udpSession{client: client}
	sess.wake.L = &sess.mu
	sess.touch()
	return sess
}

func (s *udpSession) touch() {
	s.last.Store(time.Now().UnixNano())
}

func (s *udpSession) idleFor() time.Duration {
	return time.Since(time.Unix(0, s.last.Load()))
}

// push queues a datagram, or refuses it when the ring is full; the caller holds the lock
func (s *udpSession) push(payload []byte) bool {
	if s.num == len(s.queue) {
		return false
	}
	s.queue[(s.head+s.num)%len(s.queue)] = payload
	s.num++
	return true
}

// pop takes the oldest queued datagram; the caller holds the lock and has checked num
func (s *udpSession) pop() []byte {
	p := s.queue[s.head]
	s.queue[s.head] = nil
	s.head = (s.head + 1) % len(s.queue)
	s.num--
	return p
}

// NewPacketServer returns a datagram relay for a udp listener.
func NewPacketServer(name string, cfg *Config) *PacketServer {
	s := &PacketServer{
		name: name, sessions: make(map[string]*udpSession),
		resolved: make(map[string]resolvedAddr),
	}
	s.dialSlot.L = &s.mu
	s.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}
	s.lookup = net.DefaultResolver.LookupIPAddr
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.Update(cfg)
	return s
}

// Update swaps the routing table and timeouts for sessions opened from now on.
func (s *PacketServer) Update(cfg *Config) {
	if cfg == nil {
		cfg = &Config{}
	}
	s.cfg.Store(cfg)
}

func (s *PacketServer) maxSessions() int {
	if bound := s.cfg.Load().maxConnections(); bound > 0 {
		return bound
	}
	return DefaultUDPMaxSessions
}

// Serve relays datagrams until the socket is closed or the server is shut down.
func (s *PacketServer) Serve(pc net.PacketConn) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = pc.Close()
		return ErrServerClosed
	}
	s.conn = pc
	s.mu.Unlock()
	buf := make([]byte, maxDatagram)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if s.isClosed() || errors.Is(err, net.ErrClosed) {
				return ErrServerClosed
			}
			return err
		}
		s.forward(buf[:n], addr)
	}
}

func (s *PacketServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *PacketServer) result(r string) {
	s.cfg.Load().observer().Result(r)
}

func (s *PacketServer) dropped(reason string) {
	s.cfg.Load().observer().Dropped(reason)
}

// forward hands a datagram to its client's flow, opening the flow on a worker of its own, so the
// receive loop never waits on a lookup, a dial or a write
func (s *PacketServer) forward(payload []byte, client net.Addr) {
	key := client.String()
	s.mu.Lock()
	sess, ok := s.sessions[key]
	if !ok {
		sess = s.admit(key, client)
		if sess == nil {
			s.mu.Unlock()
			s.result(ResultRefused)
			return
		}
		s.mu.Unlock()
		s.enqueue(sess, payload)
		go s.run(key, sess)
		return
	}
	s.mu.Unlock()
	s.enqueue(sess, payload)
}

// admit registers a new flow, or refuses it while draining, beyond the session bound, or beyond
// the bound on flows still opening; the caller holds the server lock
func (s *PacketServer) admit(key string, client net.Addr) *udpSession {
	if s.closed || s.draining {
		return nil
	}
	if len(s.sessions)-s.failed >= s.maxSessions() || s.opening >= maxOpeningFlows {
		return nil
	}
	sess := newUDPSession(client)
	s.sessions[key] = sess
	s.opening++
	s.workers.Add(1)
	s.cfg.Load().observer().Opened()
	return sess
}

// enqueue queues a datagram for the flow's writer in constant time and without waiting on
// anything; a failed flow drops it, and one beyond the flow's or the server's allowance is
// dropped and counted, since the client resends what matters to it
func (s *PacketServer) enqueue(sess *udpSession, payload []byte) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.state == flowFailed || sess.closed {
		return
	}
	n := int64(len(payload))
	if s.queued.Add(n) > queuedByteBudget {
		s.queued.Add(-n)
		s.dropped(DropQueueFull)
		return
	}
	if sess.state == flowOpening {
		// an opening flow holds less, since anyone able to send a datagram can open one
		if sess.num >= maxPendingDatagrams {
			s.queued.Add(-n)
			s.dropped(DropQueueFull)
			return
		}
		if s.pending.Add(n) > pendingByteBudget {
			s.pending.Add(-n)
			s.queued.Add(-n)
			s.dropped(DropQueueFull)
			return
		}
	}
	if !sess.push(append([]byte(nil), payload...)) {
		s.queued.Add(-n)
		s.dropped(DropQueueFull)
		return
	}
	if sess.state == flowOpen {
		sess.touch()
		sess.wake.Signal()
	}
}

// writer relays the flow's queued datagrams to its upstream in order until the flow closes; a
// write that blocks past the write bound drops its datagram, so a stalled upstream costs the
// flow that datagram rather than the server its receive loop
func (s *PacketServer) writer(sess *udpSession, up net.Conn) {
	defer s.workers.Done()
	sess.mu.Lock()
	for {
		for sess.num == 0 && !sess.closed {
			sess.wake.Wait()
		}
		if sess.closed {
			for sess.num > 0 {
				s.queued.Add(-int64(len(sess.pop())))
			}
			sess.mu.Unlock()
			return
		}
		payload := sess.pop()
		sess.mu.Unlock()
		// the datagram stays charged to the budget until the write returns, since it is held
		// for as long as the write blocks, however soon its ring slot is reused
		_ = up.SetWriteDeadline(time.Now().Add(udpWriteTimeout))
		n, err := up.Write(payload)
		s.queued.Add(-int64(len(payload)))
		switch {
		case err == nil:
			s.cfg.Load().observer().Bytes(DirectionIn, int64(n))
		case isTimeout(err):
			s.dropped(DropWriteTimeout)
		default:
			// the socket is unusable; closing it ends the reply loop and so the flow
			_ = up.Close()
		}
		sess.mu.Lock()
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (s *PacketServer) run(key string, sess *udpSession) {
	defer s.end(key, sess)
	up, ok := s.open(sess)
	if !ok {
		s.fail(sess)
		return
	}
	s.result(ResultProxied)
	s.reply(sess, up)
}

// open resolves and dials the flow's upstream within the dial bound and the connect timeout,
// then starts the flow's writer, which relays what the flow kept while it waited ahead of
// everything queued since
func (s *PacketServer) open(sess *udpSession) (net.Conn, bool) {
	cfg := s.cfg.Load()
	up := cfg.table().Lookup("")
	if up == nil {
		s.result(ResultNoRoute)
		return nil, false
	}
	route, ok := up.Pick(flowOf(s.name, ProtocolUDP, sess.client, ""))
	if !ok {
		s.result(ResultNoUpstream)
		return nil, false
	}
	if !s.acquireDial() {
		route.Dialed(0, ErrAbandoned)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(s.ctx, cfg.options().Connect())
	began := time.Now()
	conn, err := s.resolveAndDial(ctx, route.Addr())
	cancel()
	s.releaseDial()
	route.Dialed(time.Since(began), err)
	if err != nil {
		s.result(ResultDialFailed)
		return nil, false
	}
	sess.mu.Lock()
	if sess.closed {
		// the close pass has been through this flow; what was dialed after it is closed here
		sess.mu.Unlock()
		_ = conn.Close()
		route.Closed(nil)
		return nil, false
	}
	sess.route = route
	// what the flow kept while opening leaves the opening budget and stays queued for the writer
	var kept int64
	for i := range sess.num {
		kept += int64(len(sess.queue[(sess.head+i)%len(sess.queue)]))
	}
	s.pending.Add(-kept)
	sess.state = flowOpen
	sess.upstream = conn
	s.workers.Add(1)
	go s.writer(sess, conn)
	sess.mu.Unlock()
	s.settle(sess)
	return conn, true
}

// settle moves a flow out of the opening count once it is open
func (s *PacketServer) settle(sess *udpSession) {
	s.mu.Lock()
	s.opening--
	s.mu.Unlock()
	sess.touch()
}

func (s *PacketServer) acquireDial() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.dialing >= maxConcurrentDials {
		if s.closed {
			return false
		}
		s.dialSlot.Wait()
	}
	if s.closed {
		return false
	}
	s.dialing++
	return true
}

func (s *PacketServer) releaseDial() {
	s.mu.Lock()
	s.dialing--
	s.dialSlot.Signal()
	s.mu.Unlock()
}

// resolveAndDial dials the upstream at its address, looking a host up once per resolvedTTL for
// every flow that names it
func (s *PacketServer) resolveAndDial(ctx context.Context, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return s.dial(ctx, addr)
	}
	now := time.Now()
	s.mu.Lock()
	cached, ok := s.resolved[host]
	s.mu.Unlock()
	if !ok || now.After(cached.expires) {
		ips, err := s.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
		}
		cached = resolvedAddr{addr: ips[0].String(), expires: now.Add(resolvedTTL)}
		s.mu.Lock()
		s.resolved[host] = cached
		s.mu.Unlock()
	}
	return s.dial(ctx, net.JoinHostPort(cached.addr, port))
}

// fail marks a flow whose upstream could not be opened; it is remembered for a short, fixed
// time so its client does not cause a lookup per datagram, outside the session bound and
// however much the client sends, and is forgotten at once when too many are remembered
func (s *PacketServer) fail(sess *udpSession) {
	sess.mu.Lock()
	sess.state = flowFailed
	for sess.num > 0 {
		n := int64(len(sess.pop()))
		s.pending.Add(-n)
		s.queued.Add(-n)
	}
	sess.mu.Unlock()
	s.mu.Lock()
	s.opening--
	if s.failed >= maxFailedFlows || s.closed {
		s.mu.Unlock()
		return
	}
	s.failed++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.failed--
		s.mu.Unlock()
	}()
	timer := time.NewTimer(failedFlowLifetime)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
	case <-timer.C:
	}
}

var datagramPool = sync.Pool{New: func() any {
	b := make([]byte, maxDatagram)
	return &b
}}

func (s *PacketServer) reply(sess *udpSession, up net.Conn) {
	bp := datagramPool.Get().(*[]byte)
	defer datagramPool.Put(bp)
	buf := *bp
	sess.mu.Lock()
	route := sess.route
	sess.mu.Unlock()
	replied := false
	for {
		idle := s.cfg.Load().options().UDPIdle()
		_ = up.SetReadDeadline(time.Now().Add(idle))
		n, err := up.Read(buf)
		if n > 0 {
			if !replied && route != nil {
				replied = true
				route.FirstByte()
			}
			sess.touch()
			s.mu.Lock()
			pc := s.conn
			s.mu.Unlock()
			if _, werr := pc.WriteTo(buf[:n], sess.client); werr == nil {
				s.cfg.Load().observer().Bytes(DirectionOut, int64(n))
			}
		}
		if err == nil {
			continue
		}
		if isTimeout(err) && sess.idleFor() < idle {
			// the upstream was silent but the client was not; the session lives on
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			// nothing is listening at the upstream: the only sign a udp member is down
			sess.mu.Lock()
			sess.fault = err
			sess.mu.Unlock()
		}
		return
	}
}

func (s *PacketServer) end(key string, sess *udpSession) {
	sess.mu.Lock()
	sess.closed = true
	if sess.upstream != nil {
		_ = sess.upstream.Close()
	}
	if sess.route != nil {
		sess.route.Closed(sess.fault)
		sess.route = nil
	}
	if sess.state == flowOpening {
		for sess.num > 0 {
			n := int64(len(sess.pop()))
			s.pending.Add(-n)
			s.queued.Add(-n)
		}
	}
	sess.wake.Broadcast()
	sess.mu.Unlock()
	s.mu.Lock()
	if s.sessions[key] == sess {
		delete(s.sessions, key)
	}
	s.mu.Unlock()
	s.cfg.Load().observer().Ended()
	s.workers.Done()
}

// Shutdown refuses new flows and keeps relaying the established ones until they end or ctx is
// done, then closes the socket and every flow.
func (s *PacketServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.close()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops relaying and ends every flow.
func (s *PacketServer) Close() error {
	s.close()
	s.workers.Wait()
	return nil
}

func (s *PacketServer) close() {
	// the context ends pending dials and failed flows' waits; each flow is closed under its own
	// lock, and a dial that completes after that finds the flow closed and closes what it dialed
	s.cancel()
	s.mu.Lock()
	s.closed = true
	if s.conn != nil {
		_ = s.conn.Close()
	}
	sessions := make([]*udpSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.dialSlot.Broadcast()
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.mu.Lock()
		sess.closed = true
		if sess.upstream != nil {
			_ = sess.upstream.Close()
		}
		sess.wake.Broadcast()
		sess.mu.Unlock()
	}
}

// ActiveSessions reports how many client flows are registered, failed ones included.
func (s *PacketServer) ActiveSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}
