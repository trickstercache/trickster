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
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// countingUpstream refuses its first n selections and then yields addr
type countingUpstream struct {
	addr   string
	refuse int32
	calls  atomic.Int32
}

func (c *countingUpstream) Addr() (string, bool) {
	if c.calls.Add(1) <= c.refuse {
		return "", false
	}
	return c.addr, true
}

func TestFailedFlowsDoNotFillTheSessionBound(t *testing.T) {
	// flows whose upstream could not be opened are held outside the session bound for a fixed
	// time that no traffic extends, so a sender keeping them alive cannot refuse real clients
	echo := udpEcho(t, "echo:")
	shortFailedLifetime(t, 400*time.Millisecond)
	up := &countingUpstream{addr: echo, refuse: 4}
	srv, addr, _ := startPacketServer(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": up}), MaxConnections: 4,
	})
	failed := make([]*net.UDPConn, 4)
	for i := range failed {
		failed[i] = udpClient(t, addr)
		if _, err := failed[i].Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.failed == 4
	})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			for _, c := range failed {
				_, _ = c.Write([]byte("keepalive"))
			}
		}
	})
	if got := datagram(t, udpClient(t, addr), "real"); got != "echo:real" {
		t.Errorf("a real client was refused while failed flows filled the map: %q", got)
	}
	// the failed flows are forgotten on their fixed lifetime despite the keepalives; a client
	// that keeps sending afterwards is admitted afresh, as a retry should be
	started := time.Now()
	waitFor(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.failed == 0
	})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("failed flows were remembered for %v beyond the test's start", elapsed)
	}
	close(stop)
	wg.Wait()
}

func TestOpeningFlowsAreBudgeted(t *testing.T) {
	// however many clients open flows at once with the largest datagrams, the bytes kept, the
	// dials in flight and the flows opening stay under the server's bounds
	echo := udpEcho(t, "echo:")
	release := make(chan struct{})
	var inFlight, peak atomic.Int32
	var d net.Dialer
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(10 * time.Second)},
	}, func(ctx context.Context, a string) (net.Conn, error) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inFlight.Add(-1)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return d.DialContext(ctx, "udp", a)
	})
	// the datagrams are as large as a loopback socket accepts on every platform
	const clients = 200
	big := make([]byte, 8192)
	for range clients {
		c := udpClient(t, addr)
		for range maxPendingDatagrams {
			_, _ = c.Write(big)
		}
	}
	waitFor(t, func() bool { return srv.ActiveSessions() > 0 })
	time.Sleep(200 * time.Millisecond)
	if kept := srv.pending.Load(); kept > pendingByteBudget {
		t.Errorf("kept %d pending bytes, budget %d", kept, pendingByteBudget)
	}
	srv.mu.Lock()
	opening, dialing := srv.opening, srv.dialing
	srv.mu.Unlock()
	if opening > maxOpeningFlows {
		t.Errorf("%d flows opening, bound %d", opening, maxOpeningFlows)
	}
	if dialing > maxConcurrentDials || peak.Load() > maxConcurrentDials {
		t.Errorf("%d dials in flight (peak %d), bound %d", dialing, peak.Load(), maxConcurrentDials)
	}
	close(release)
	waitFor(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.opening == 0
	})
	if kept := srv.pending.Load(); kept != 0 {
		t.Errorf("%d pending bytes after every flow opened", kept)
	}
}

func TestCloseDoesNotPublishALateDial(t *testing.T) {
	// a dial that completes after the close pass has been through its flow is closed by the
	// flow itself, so Close neither waits on it nor leaks it
	echo := udpEcho(t, "echo:")
	var late atomic.Pointer[net.Conn]
	dialing := make(chan struct{}, 1)
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(10 * time.Second)},
	}, func(ctx context.Context, a string) (net.Conn, error) {
		dialing <- struct{}{}
		<-ctx.Done()
		// the dial ignores the cancellation and hands back a usable socket anyway
		conn, err := net.Dial("udp", a)
		if err != nil {
			return nil, err
		}
		late.Store(&conn)
		return conn, nil
	})
	if _, err := udpClient(t, addr).Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	<-dialing
	closed := make(chan struct{})
	go func() { _ = srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on a socket dialed after the close pass")
	}
	conn := late.Load()
	if conn == nil {
		t.Fatal("the late dial did not complete")
	}
	if _, err := (*conn).Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the late socket was left open: %v", err)
	}
	if srv.ActiveSessions() != 0 {
		t.Errorf("sessions after Close = %d", srv.ActiveSessions())
	}
}

func TestDatagramsKeepOrderWhileOpening(t *testing.T) {
	// datagrams kept while a flow opens reach the upstream ahead of those the receive loop
	// relays once the flow is open, in the order the client sent them
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	var mu sync.Mutex
	var got []int
	go func() {
		buf := make([]byte, 64)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			seq, _ := strconv.Atoi(string(buf[:n]))
			mu.Lock()
			got = append(got, seq)
			mu.Unlock()
		}
	}()
	received := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}
	var release atomic.Pointer[chan struct{}]
	var d net.Dialer
	_, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(pc.LocalAddr().String())}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(10 * time.Second)},
	}, func(ctx context.Context, a string) (net.Conn, error) {
		if r := release.Load(); r != nil {
			select {
			case <-*r:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return d.DialContext(ctx, "udp", a)
	})
	const rounds, perRound = 5, 24
	for round := range rounds {
		r := make(chan struct{})
		release.Store(&r)
		base := received()
		client := udpClient(t, addr)
		seq := 0
		send := func() {
			if _, err := client.Write([]byte(strconv.Itoa(base + seq))); err != nil {
				t.Fatal(err)
			}
			seq++
		}
		for range maxPendingDatagrams {
			send()
		}
		close(r)
		// the rest arrive from the moment the flow starts flushing what it kept, paced so no
		// more than the flow's allowance accumulates between the flush and its publication
		waitFor(t, func() bool { return received() > base })
		for seq < perRound {
			send()
			time.Sleep(2 * time.Millisecond)
		}
		deadline := time.Now().Add(3 * time.Second)
		for received() < base+perRound && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		if len(got) != base+perRound {
			t.Fatalf("round %d: upstream saw %d of %d: %v", round, len(got)-base, perRound, got[base:])
		}
		for i, v := range got[base:] {
			if v != base+i {
				t.Fatalf("round %d: upstream saw %v", round, got[base:])
			}
		}
		mu.Unlock()
	}
}

func TestShutdownDrainsSessions(t *testing.T) {
	// a shutdown refuses new flows and keeps relaying the established ones until the deadline,
	// after which the group's Close ends them
	echo := udpEcho(t, "echo:")
	srv, addr, done := startPacketServer(t, &Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	established := udpClient(t, addr)
	if got := datagram(t, established, "before"); got != "echo:before" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- srv.Shutdown(ctx) }()
	waitFor(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.draining
	})
	if got := datagram(t, established, "during"); got != "echo:during" {
		t.Errorf("an established session was cut off by the drain: %q", got)
	}
	newcomer := udpClient(t, addr)
	if _, err := newcomer.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	_ = newcomer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := newcomer.Read(make([]byte, 16)); err == nil {
		t.Error("a new flow was admitted during the drain")
	}
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want the deadline", err)
	}
	if srv.ActiveSessions() != 1 {
		t.Errorf("sessions after the deadline = %d; the established one is Close's to end", srv.ActiveSessions())
	}
	_ = srv.Close()
	if err := <-done; !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve returned %v", err)
	}
	if _, err := established.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	_ = established.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := established.Read(make([]byte, 16)); err == nil {
		t.Error("the established session outlived Close")
	}

	// a drain completes on its own once the last session ends
	srv2, addr2, done2 := startPacketServer(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(150 * time.Millisecond)},
	})
	if got := datagram(t, udpClient(t, addr2), "hi"); got != "echo:hi" {
		t.Fatal(got)
	}
	if err := srv2.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown after the session expired = %v", err)
	}
	if err := <-done2; !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve returned %v", err)
	}
}

func TestResolvedUpstreamsAreShared(t *testing.T) {
	// a named upstream is looked up once and the address reused by every flow that names it
	echo := udpEcho(t, "echo:")
	_, port, err := net.SplitHostPort(echo)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups.Add(1)
		if host != "echo.example" {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return []net.IPAddr{{IP: net.IPv4(127, 0, 0, 1)}}, nil
	}
	srv, addr, _ := startPacketServerResolving(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static(net.JoinHostPort("echo.example", port))}),
	}, nil, lookup)
	for range 3 {
		if got := datagram(t, udpClient(t, addr), "named"); got != "echo:named" {
			t.Fatalf("reply = %q", got)
		}
	}
	srv.mu.Lock()
	cached, ok := srv.resolved["echo.example"]
	srv.mu.Unlock()
	if !ok || cached.addr != "127.0.0.1" {
		t.Errorf("the upstream host was not cached: %v", cached)
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("%d lookups for three flows naming one host", n)
	}
	// a host that does not resolve fails the flow rather than the receive loop
	bad, addr2, _ := startPacketServerResolving(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static("no-such-host.example:1")}),
	}, nil, lookup)
	c := udpClient(t, addr2)
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		bad.mu.Lock()
		defer bad.mu.Unlock()
		return bad.failed == 1
	})
}

// stallingConn wraps an upstream socket with writes that block until released or their deadline
type stallingConn struct {
	net.Conn
	release  chan struct{}
	deadline atomic.Int64
	blocked  atomic.Int32
	// inFlight, when set, counts the bytes every stalled write holds across the test's conns
	inFlight *atomic.Int64
}

func (c *stallingConn) SetWriteDeadline(t time.Time) error {
	c.deadline.Store(t.UnixNano())
	return c.Conn.SetWriteDeadline(t)
}

func (c *stallingConn) Write(p []byte) (int, error) {
	select {
	case <-c.release:
		return c.Conn.Write(p)
	default:
	}
	c.blocked.Add(1)
	if c.inFlight != nil {
		c.inFlight.Add(int64(len(p)))
		defer c.inFlight.Add(-int64(len(p)))
	}
	wait := time.Until(time.Unix(0, c.deadline.Load()))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-c.release:
		return c.Conn.Write(p)
	case <-timer.C:
		return 0, &net.OpError{Op: "write", Net: "udp", Err: timeoutError{}}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestBlockedUpstreamWriteStallsOnlyItsFlow(t *testing.T) {
	// one flow's upstream blocks every write; another flow is read and relayed at once, the
	// blocked flow's queue fills to its allowance and no further, the excess is dropped and
	// counted, and a write past the write bound drops its datagram rather than the flow
	echo := udpEcho(t, "echo:")
	release := make(chan struct{})
	var first atomic.Pointer[stallingConn]
	var d net.Dialer
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static(echo)}),
	}, func(ctx context.Context, a string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, "udp", a)
		if err != nil {
			return nil, err
		}
		if first.Load() == nil {
			sc := &stallingConn{Conn: conn, release: release}
			first.Store(sc)
			return sc, nil
		}
		return conn, nil
	})
	dropsBefore := testutil.ToFloat64(metrics.ProxyStreamDroppedDatagrams.WithLabelValues("udp-test", DropQueueFull))
	timeoutsBefore := testutil.ToFloat64(metrics.ProxyStreamDroppedDatagrams.WithLabelValues("udp-test", DropWriteTimeout))
	stuck := udpClient(t, addr)
	if _, err := stuck.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { sc := first.Load(); return sc != nil && sc.blocked.Load() > 0 })
	// the receive loop is still reading: another client is relayed while the first is blocked
	started := time.Now()
	if got := datagram(t, udpClient(t, addr), "other"); got != "echo:other" {
		t.Fatalf("another flow was not relayed while a write blocked: %q", got)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("the other flow waited %v behind the blocked write", elapsed)
	}
	// the blocked flow queues up to its allowance and drops the rest
	for i := range maxQueuedDatagrams + 8 {
		if _, err := stuck.Write([]byte(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool {
		return testutil.ToFloat64(metrics.ProxyStreamDroppedDatagrams.WithLabelValues("udp-test", DropQueueFull))-dropsBefore >= 8
	})
	srv.mu.Lock()
	sess := srv.sessions[stuck.LocalAddr().String()]
	srv.mu.Unlock()
	sess.mu.Lock()
	queued := sess.num
	sess.mu.Unlock()
	if queued > maxQueuedDatagrams {
		t.Errorf("%d datagrams queued, allowance %d", queued, maxQueuedDatagrams)
	}
	if held := srv.queued.Load(); held > queuedByteBudget {
		t.Errorf("%d bytes queued across flows, budget %d", held, queuedByteBudget)
	}
	// the first write times out at the write bound and its datagram is dropped, not the flow
	waitFor(t, func() bool {
		return testutil.ToFloat64(metrics.ProxyStreamDroppedDatagrams.WithLabelValues("udp-test", DropWriteTimeout)) > timeoutsBefore
	})
	close(release)
	// once released the queue drains in order and the flow relays again
	_ = stuck.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, err := stuck.Read(buf)
	if err != nil {
		t.Fatalf("the blocked flow did not recover: %v", err)
	}
	if got := string(buf[:n]); got != "echo:0" {
		t.Errorf("first datagram relayed after the stall = %q, want the oldest queued", got)
	}
	if srv.ActiveSessions() != 2 {
		t.Errorf("sessions = %d", srv.ActiveSessions())
	}
}

func TestInFlightWritesStayWithinTheQueuedBudget(t *testing.T) {
	// a datagram being written is still the server's memory: with every flow's writer blocked
	// on one, the budget counts those too, so the bytes held never exceed it and the excess is
	// dropped and counted
	was := queuedByteBudget
	queuedByteBudget = 64 << 10
	t.Cleanup(func() { queuedByteBudget = was })
	echo := udpEcho(t, "echo:")
	release := make(chan struct{})
	var inFlight atomic.Int64
	var d net.Dialer
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static(echo)}),
	}, func(ctx context.Context, a string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, "udp", a)
		if err != nil {
			return nil, err
		}
		return &stallingConn{Conn: conn, release: release, inFlight: &inFlight}, nil
	})
	dropsBefore := testutil.ToFloat64(metrics.ProxyStreamDroppedDatagrams.WithLabelValues("udp-test", DropQueueFull))
	const flows = 24
	payload := make([]byte, 8192)
	clients := make([]*net.UDPConn, flows)
	for i := range clients {
		clients[i] = udpClient(t, addr)
		if _, err := clients[i].Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	// every flow that opened has one write in flight; more than the budget's worth were asked
	waitFor(t, func() bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.opening == 0
	})
	for _, c := range clients {
		if _, err := c.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if held := inFlight.Load(); held > queuedByteBudget {
		t.Errorf("%d bytes in flight across stalled writes, budget %d", held, queuedByteBudget)
	}
	if held := srv.queued.Load(); held > queuedByteBudget {
		t.Errorf("%d bytes charged, budget %d", held, queuedByteBudget)
	}
	if held := inFlight.Load(); held <= 0 {
		t.Error("no write was stalled")
	}
	drops := testutil.ToFloat64(metrics.ProxyStreamDroppedDatagrams.WithLabelValues("udp-test", DropQueueFull)) - dropsBefore
	if drops <= 0 {
		t.Error("nothing beyond the budget was dropped")
	}
	close(release)
	waitFor(t, func() bool { return srv.queued.Load() == 0 })
}
