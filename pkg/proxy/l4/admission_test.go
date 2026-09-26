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
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

type verdicts struct {
	peer, flow, datagram Verdict
	datagrams            bool
	mu                   sync.Mutex
	asked                askedFlows
}

type askedFlows struct {
	peers, flows, grams []Flow
	sizes               []int
}

func (v *verdicts) Peer(f Flow) Verdict {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked.peers = append(v.asked.peers, f)
	return v.peer
}

func (v *verdicts) Flow(f Flow) Verdict {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked.flows = append(v.asked.flows, f)
	return v.flow
}

func (v *verdicts) Datagram(f Flow, size int) Verdict {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.asked.grams = append(v.asked.grams, f)
	v.asked.sizes = append(v.asked.sizes, size)
	return v.datagram
}

func (v *verdicts) Datagrams() bool { return v.datagrams }

func (v *verdicts) seen() askedFlows {
	v.mu.Lock()
	defer v.mu.Unlock()
	return askedFlows{
		peers: append([]Flow(nil), v.asked.peers...), flows: append([]Flow(nil), v.asked.flows...),
		grams: append([]Flow(nil), v.asked.grams...), sizes: append([]int(nil), v.asked.sizes...),
	}
}

func admitted(t *testing.T, up Upstream, adm Admission, obs Observer) *Config {
	t.Helper()
	return &Config{Table: tableOf(t, map[string]Upstream{"": up}), Admission: adm, Observer: obs}
}

func expectReset(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("the client read %v, want a reset", err)
	}
}

// the peer stage runs before any byte is read and the flow stage before the pick, both with the
// PROXY header the connection arrived behind
func TestServerAsksEachStageInTurn(t *testing.T) {
	up := rotate(echoServer(t, "echo:", nil))
	adm := &verdicts{}
	counts := &countingObserver{}
	_, addr := startServer(t, ProtocolTCP, admitted(t, up, adm, counts))
	conn := dialTCP(t, addr)
	if got := exchange(t, conn, "hi"); got != "echo:hi" {
		t.Fatalf("reply = %q", got)
	}
	asked := adm.seen()
	if len(asked.peers) != 1 || len(asked.flows) != 1 || len(asked.grams) != 0 {
		t.Fatalf("asked %d peers, %d flows, %d datagrams", len(asked.peers), len(asked.flows), len(asked.grams))
	}
	local := netip.MustParseAddrPort(conn.LocalAddr().String())
	for stage, f := range map[string]Flow{"peer": asked.peers[0], "flow": asked.flows[0]} {
		if f.Listener != "test" || f.Protocol != ProtocolTCP || f.Client != local || f.ServerName != "" || f.Proxy != nil {
			t.Errorf("the %s stage saw %+v, want the client %v", stage, f, local)
		}
	}
	_, results, _ := counts.snapshot()
	if results[ResultProxied] != 1 || results[ResultDenied] != 0 {
		t.Errorf("results = %v", results)
	}

	behind := &verdicts{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer("test", ProtocolTCP, admitted(t, up, behind, nil))
	go func() { _ = srv.Serve(headerListener{ln}) }()
	t.Cleanup(func() { _ = srv.Close() })
	exchange(t, dialTCP(t, ln.Addr().String()), "hi")
	asked = behind.seen()
	if len(asked.peers) != 1 || asked.peers[0].Proxy == nil || len(asked.flows) != 1 || asked.flows[0].Proxy == nil {
		t.Errorf("the PROXY header did not reach both stages: %+v", asked)
	}
}

func TestServerDropsAtThePeerStage(t *testing.T) {
	up := rotate(echoServer(t, "echo:", nil))
	adm := &verdicts{peer: Drop}
	counts := &countingObserver{}
	srv, addr := startServer(t, ProtocolTCP, admitted(t, up, adm, counts))
	conn := dialTCP(t, addr)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("the client read %v, want a close", err)
	}
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
	if flows, _ := up.seen(); len(flows) != 0 {
		t.Error("a dropped connection was picked an upstream")
	}
	if asked := adm.seen(); len(asked.flows) != 0 {
		t.Error("a connection dropped at the peer stage reached the flow stage")
	}
	// the observer hears of the end just after the connection leaves the count
	waitFor(t, func() bool { active, _, _ := counts.snapshot(); return active == 0 })
	if _, results, _ := counts.snapshot(); results[ResultDenied] != 1 || results[ResultProxied] != 0 {
		t.Errorf("results = %v", results)
	}
}

// at either stage, the client sees a reset rather than a close
func TestServerRejectsWithAReset(t *testing.T) {
	up := rotate(echoServer(t, "echo:", nil))
	for stage, adm := range map[string]*verdicts{"peer": {peer: Reject}, "flow": {flow: Reject}} {
		counts := &countingObserver{}
		_, addr := startServer(t, ProtocolTCP, admitted(t, up, adm, counts))
		expectReset(t, dialTCP(t, addr))
		waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultDenied] == 1 })
		if flows, _ := up.seen(); len(flows) != 0 {
			t.Errorf("a connection rejected at the %s stage was picked an upstream", stage)
		}
	}
}

// the peer stage runs before the hello is read and the flow stage after the server name has
// routed the connection; a name that routes nowhere is never judged
func TestServerJudgesTheTLSFlowAfterThePeek(t *testing.T) {
	cert := selfSigned(t, "shop.example.com")
	up := rotate(echoServer(t, "tls:", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	adm := &verdicts{flow: Drop}
	counts := &countingObserver{}
	_, addr := startServer(t, ProtocolTLS, &Config{
		Table: tableOf(t, map[string]Upstream{"shop.example.com": up}), Admission: adm, Observer: counts,
	})
	handshake := func(serverName string) error {
		conn := dialTCP(t, addr)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		return tls.Client(conn, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake() // #nosec G402 -- a test certificate
	}
	if err := handshake("shop.example.com"); err == nil {
		t.Error("a connection dropped at the flow stage completed its handshake")
	}
	asked := adm.seen()
	if len(asked.peers) != 1 || asked.peers[0].ServerName != "" || asked.peers[0].Protocol != ProtocolTLS {
		t.Errorf("the peer stage saw %+v, want no server name", asked.peers)
	}
	if len(asked.flows) != 1 || asked.flows[0].ServerName != "shop.example.com" || asked.flows[0].Protocol != ProtocolTLS {
		t.Errorf("the flow stage saw %+v, want the server name", asked.flows)
	}
	if err := handshake("other.example.com"); err == nil {
		t.Error("an unroutable name completed its handshake")
	}
	if asked := adm.seen(); len(asked.flows) != 1 || len(asked.peers) != 2 {
		t.Errorf("a connection with no route was judged: %+v", asked)
	}
	waitFor(t, func() bool {
		_, results, _ := counts.snapshot()
		return results[ResultDenied] == 1 && results[ResultNoRoute] == 1
	})
	if flows, _ := up.seen(); len(flows) != 0 {
		t.Error("a dropped connection was picked an upstream")
	}
}

func TestServerReloadSwapsTheAdmission(t *testing.T) {
	echo := echoServer(t, "echo:", nil)
	srv, addr := startServer(t, ProtocolTCP, admitted(t, Static(echo), &verdicts{peer: Drop}, nil))
	expectClosed(t, dialTCP(t, addr))
	srv.Update(&Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	if got := exchange(t, dialTCP(t, addr), "hi"); got != "echo:hi" {
		t.Fatalf("reply once the admission was removed = %q", got)
	}
	srv.Update(admitted(t, Static(echo), &verdicts{flow: Drop}, nil))
	expectClosed(t, dialTCP(t, addr))
}

// a reject is a drop on udp; a denied client is held, its datagrams dropped unasked, and a
// config swap releases the hold so the new admission judges it
func TestPacketServerDeniesSessionsAtThePeerStage(t *testing.T) {
	echo := udpEcho(t, "echo:")
	for name, verdict := range map[string]Verdict{"dropped": Drop, "rejected": Reject} {
		adm := &verdicts{peer: verdict}
		counts := &countingObserver{}
		srv, addr, _ := startPacketServer(t, admitted(t, rotate(echo), adm, counts))
		client := udpClient(t, addr)
		for range 2 {
			if _, err := client.Write([]byte("anyone?")); err != nil {
				t.Fatal(err)
			}
		}
		_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := client.Read(make([]byte, 16)); err == nil {
			t.Errorf("a %s session was answered", name)
		}
		waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultDenied] == 1 })
		waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
		waitFor(t, func() bool { active, _, _ := counts.snapshot(); return active == 0 })
		// held: a further datagram is dropped without asking, and opens nothing
		before := counts.dropped(DropDenied)
		if _, err := client.Write([]byte("still?")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return counts.dropped(DropDenied) == before+1 })
		asked := adm.seen()
		local := netip.MustParseAddrPort(client.LocalAddr().String())
		if len(asked.peers) != 1 || asked.peers[0].Protocol != ProtocolUDP || asked.peers[0].Client != local ||
			asked.peers[0].Listener != "udp-test" || asked.peers[0].ServerName != "" {
			t.Errorf("%s: the peer stage saw %+v, want the client %v once", name, asked.peers, local)
		}
		if len(asked.flows) != 0 || len(asked.grams) != 0 {
			t.Errorf("%s: udp reached the flow stage %d times and the datagram stage %d times", name,
				len(asked.flows), len(asked.grams))
		}
		if active, results, _ := counts.snapshot(); active != 0 || results[ResultRefused] != 0 || results[ResultProxied] != 0 {
			t.Errorf("%s: active = %d, results = %v", name, active, results)
		}
		allow := &verdicts{}
		srv.Update(admitted(t, rotate(echo), allow, counts))
		if got := datagram(t, client, "now?"); got != "echo:now?" {
			t.Fatalf("%s: after the swap, reply = %q", name, got)
		}
		if asked := allow.seen(); len(asked.peers) != 1 {
			t.Errorf("%s: the admission the swap brought was asked %d times", name, len(asked.peers))
		}
	}
}

// each datagram is judged on the flow's own worker before it is relayed, the first included; with
// Datagrams off the stage is never asked, whatever it would answer, until a reload reads it again
func TestPacketServerJudgesDatagrams(t *testing.T) {
	echo := udpEcho(t, "echo:")
	judged := &verdicts{datagram: Drop, datagrams: true}
	counts := &countingObserver{}
	var dials atomic.Int32
	srv, addr, _ := startPacketServerWith(t, admitted(t, rotate(echo), judged, counts), countingDial(&dials))
	client := udpClient(t, addr)
	if _, err := client.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return counts.dropped(DropDenied) == 1 })
	if srv.ActiveSessions() != 1 || dials.Load() != 0 {
		t.Errorf("sessions = %d, dials = %d; want the flow awaiting an allowed datagram, undialed",
			srv.ActiveSessions(), dials.Load())
	}
	local := netip.MustParseAddrPort(client.LocalAddr().String())
	if asked := judged.seen(); len(asked.peers) != 1 || len(asked.sizes) != 1 || asked.sizes[0] != len("first") ||
		asked.grams[0].Client != local || asked.grams[0].Protocol != ProtocolUDP {
		t.Errorf("the datagram stage saw %+v", asked)
	}
	// once allowed, the open flow's later datagrams are still judged, each by its size
	allowed := &verdicts{datagrams: true}
	srv.Update(admitted(t, rotate(echo), allowed, counts))
	for _, msg := range []string{"two", "three"} {
		if got := datagram(t, client, msg); got != "echo:"+msg {
			t.Fatalf("reply = %q", got)
		}
	}
	if asked := allowed.seen(); len(asked.sizes) != 2 || asked.sizes[0] != 3 || asked.sizes[1] != 5 || len(asked.peers) != 0 {
		t.Errorf("the datagram stage saw %+v, want two datagrams of an open flow", asked)
	}
	if dials.Load() != 1 {
		t.Errorf("%d dials, want one once a datagram was allowed", dials.Load())
	}

	off := &verdicts{datagram: Drop}
	srv2, addr2, _ := startPacketServer(t, admitted(t, rotate(echo), off, nil))
	client2 := udpClient(t, addr2)
	if got := datagram(t, client2, "ping"); got != "echo:ping" {
		t.Fatalf("reply = %q", got)
	}
	// the answer to Datagrams was read at the last swap: changing it moves nothing until the next
	off.datagrams = true
	if got := datagram(t, client2, "pong"); got != "echo:pong" {
		t.Fatalf("reply = %q", got)
	}
	if asked := off.seen(); len(asked.grams) != 0 {
		t.Errorf("the datagram stage was asked %d times while Datagrams was off", len(asked.grams))
	}
	obs := &countingObserver{}
	srv2.Update(admitted(t, rotate(echo), off, obs))
	if _, err := client2.Write([]byte("dropped")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return obs.dropped(DropDenied) == 1 })
	if asked := off.seen(); len(asked.grams) != 1 {
		t.Errorf("the datagram stage was asked %d times after the swap", len(asked.grams))
	}
}

type resettable struct {
	net.Conn
	resets *atomic.Int32
}

func (r resettable) Reset() error {
	r.resets.Add(1)
	return resetConn(r.Conn)
}

// through a plain, a replaying and a wrapped connection the client sees a reset; what cannot be
// reset is left as it was
func TestResetConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var resets atomic.Int32
	for name, wrap := range map[string]func(net.Conn) net.Conn{
		"tcp":     func(c net.Conn) net.Conn { return c },
		"replay":  func(c net.Conn) net.Conn { return &replayConn{Conn: c, r: c} },
		"wrapped": func(c net.Conn) net.Conn { return resettable{Conn: c, resets: &resets} },
	} {
		client := dialTCP(t, ln.Addr().String())
		server := <-accepted
		if err := resetConn(wrap(server)); err != nil {
			t.Errorf("%s: reset = %v", name, err)
		}
		expectReset(t, client)
	}
	if resets.Load() != 1 {
		t.Errorf("the wrapper's own reset was used %d times", resets.Load())
	}
	// a connection already closed cannot take a linger, and says so rather than pretending
	_ = dialTCP(t, ln.Addr().String())
	closed := <-accepted
	_ = closed.Close()
	if err := resetConn(closed); err == nil || errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a closed connection was reset: %v", err)
	}
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	if err := resetConn(p1); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a pipe was reset: %v", err)
	}
	go func() { _, _ = p2.Read(make([]byte, 1)) }()
	if _, err := p1.Write([]byte("x")); err != nil {
		t.Errorf("a pipe that cannot be reset was closed: %v", err)
	}
}

type gatedAdmission struct {
	arrived, release chan struct{}
}

func (g *gatedAdmission) Peer(Flow) Verdict {
	g.arrived <- struct{}{}
	<-g.release
	return Allow
}

func (*gatedAdmission) Flow(Flow) Verdict          { return Allow }
func (*gatedAdmission) Datagram(Flow, int) Verdict { return Allow }
func (*gatedAdmission) Datagrams() bool            { return false }

func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not happen: the server's lock is held while its admission answers", what)
	}
}

// two first datagrams of one peer handled at once open one flow, judged once, with both queued;
// the server answers about its sessions while the admission is still deciding
func TestPacketServerOpensOneFlowForConcurrentFirstDatagrams(t *testing.T) {
	gate := &gatedAdmission{arrived: make(chan struct{}), release: make(chan struct{})}
	counts := &countingObserver{}
	srv := NewPacketServer("udp-test", &Config{
		Table: tableOf(t, map[string]Upstream{"": Static("127.0.0.1:9")}), Admission: gate, Observer: counts,
	})
	srv.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { _ = srv.Close() })
	// released ahead of the close, so a server that held its lock through the admission cannot hang
	var released sync.Once
	t.Cleanup(func() { released.Do(func() { close(gate.release) }) })
	client := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 4000}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { srv.forward([]byte("first"), client) })
	}
	wg.Wait()
	within(t, "the admission being asked", func() { <-gate.arrived })
	within(t, "a session count", func() {
		if srv.ActiveSessions() != 1 {
			t.Errorf("sessions = %d while the admission decides, want the one flow held", srv.ActiveSessions())
		}
	})
	released.Do(func() { close(gate.release) })
	select {
	case <-gate.arrived:
		t.Error("the admission was asked twice for one flow")
	case <-time.After(50 * time.Millisecond):
	}
	srv.mu.Lock()
	sess, opening := srv.sessions[client.String()], srv.opening
	srv.mu.Unlock()
	if sess == nil || opening != 1 || srv.ActiveSessions() != 1 {
		t.Fatalf("sessions = %d, opening = %d, want one flow", srv.ActiveSessions(), opening)
	}
	sess.mu.Lock()
	queued := sess.num
	sess.mu.Unlock()
	if queued != 2 {
		t.Errorf("the flow holds %d datagrams, want both", queued)
	}
	if active, results, _ := counts.snapshot(); active != 1 || len(results) != 0 {
		t.Errorf("active = %d, results = %v", active, results)
	}
}

type sessionCounting struct {
	srv  atomic.Pointer[PacketServer]
	seen atomic.Int32
}

func (q *sessionCounting) Peer(Flow) Verdict {
	q.seen.Store(int32(q.srv.Load().ActiveSessions()))
	return Allow
}

func (*sessionCounting) Flow(Flow) Verdict          { return Allow }
func (*sessionCounting) Datagram(Flow, int) Verdict { return Allow }
func (*sessionCounting) Datagrams() bool            { return false }

func TestPacketServerAdmissionMayAskAboutSessions(t *testing.T) {
	asking := &sessionCounting{}
	srv, addr, _ := startPacketServer(t, admitted(t, rotate(udpEcho(t, "echo:")), asking, nil))
	asking.srv.Store(srv)
	if got := datagram(t, udpClient(t, addr), "one"); got != "echo:one" {
		t.Fatalf("reply = %q", got)
	}
	if asking.seen.Load() != 1 {
		t.Errorf("the admission counted %d sessions, want its own flow, held while it decides", asking.seen.Load())
	}
}

// the server publishes a copy of the config it is given, so the caller may reuse or share its
// own for a later swap, and nothing it writes there reaches the receive loop until it does
func TestPacketServerUpdateLeavesTheCallerConfigAlone(t *testing.T) {
	echo := udpEcho(t, "echo:")
	cfg := &Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})}
	srv, addr, _ := startPacketServer(t, cfg)
	other := NewPacketServer("other", cfg)
	t.Cleanup(func() { _ = other.Close() })
	if published := srv.cfg.Load(); published == cfg || published == other.cfg.Load() {
		t.Fatal("the caller's config, or another server's copy, was published")
	}
	client := udpClient(t, addr)
	if got := datagram(t, client, "one"); got != "echo:one" {
		t.Fatalf("reply = %q", got)
	}
	judging := &verdicts{datagram: Drop, datagrams: true}
	counts := &countingObserver{}
	cfg.Admission, cfg.Observer = judging, counts
	srv.Update(cfg)
	if cfg.datagrams {
		t.Error("the caller's config was written")
	}
	if published := srv.cfg.Load(); published == cfg || !published.datagrams || published.Admission != Admission(judging) {
		t.Errorf("published %+v", published)
	}
	cfg.Admission = nil
	for _, msg := range []string{"two", "three"} {
		if _, err := client.Write([]byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return counts.dropped(DropDenied) == 2 })
}

type slowFor struct {
	client         atomic.Pointer[netip.AddrPort]
	peer, datagram chan struct{}
}

func (a *slowFor) slow(f Flow) bool {
	c := a.client.Load()
	return c != nil && f.Client == *c
}

func (a *slowFor) Peer(f Flow) Verdict {
	if a.slow(f) {
		<-a.peer
	}
	return Allow
}

func (a *slowFor) Datagram(f Flow, _ int) Verdict {
	if a.slow(f) {
		<-a.datagram
	}
	return Allow
}

func (*slowFor) Flow(Flow) Verdict { return Allow }
func (*slowFor) Datagrams() bool   { return true }

// an admission that takes its time over one client delays that client alone: the receive loop
// keeps serving the others while it decides, at the peer stage and the datagram stage alike
func TestPacketServerKeepsAdmissionOffTheReceiveLoop(t *testing.T) {
	slow := &slowFor{peer: make(chan struct{}), datagram: make(chan struct{})}
	_, addr, _ := startPacketServer(t, admitted(t, rotate(udpEcho(t, "echo:")), slow, nil))
	t.Cleanup(func() {
		for _, ch := range []chan struct{}{slow.peer, slow.datagram} {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	})
	quick := udpClient(t, addr)
	held := udpClient(t, addr)
	heldAddr := netip.MustParseAddrPort(held.LocalAddr().String())
	slow.client.Store(&heldAddr)
	if got := datagram(t, quick, "one"); got != "echo:one" {
		t.Fatalf("reply = %q", got)
	}
	if _, err := held.Write([]byte("held")); err != nil {
		t.Fatal(err)
	}
	if got := datagram(t, quick, "two"); got != "echo:two" {
		t.Fatalf("while a flow waits on the peer stage, reply = %q", got)
	}
	close(slow.peer)
	if got := datagram(t, quick, "three"); got != "echo:three" {
		t.Fatalf("while a flow waits on the datagram stage, reply = %q", got)
	}
	close(slow.datagram)
	_ = held.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	n, err := held.Read(buf)
	if err != nil || string(buf[:n]) != "echo:held" {
		t.Errorf("the held client read %q, %v; want its reply once its admission answered", buf[:n], err)
	}
}

func countingDial(dials *atomic.Int32) func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "udp", addr)
	}
}

type holding struct {
	*verdicts
	hold time.Duration
}

func (h *holding) Hold(Flow) time.Duration { return h.hold }

// the admission sets the hold: a brief one ends and the client is judged again; none at all
// judges every datagram of the client afresh, each on a flow of its own
func TestPacketServerHoldsDeniedClientsAsTheAdmissionSays(t *testing.T) {
	echo := udpEcho(t, "echo:")
	brief := &holding{verdicts: &verdicts{peer: Drop}, hold: 150 * time.Millisecond}
	srv, addr, _ := startPacketServer(t, admitted(t, rotate(echo), brief, nil))
	client := udpClient(t, addr)
	if _, err := client.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(brief.seen().peers) == 1 })
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	time.Sleep(200 * time.Millisecond)
	if _, err := client.Write([]byte("two")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(brief.seen().peers) == 2 })

	none := &holding{verdicts: &verdicts{peer: Drop}, hold: 0}
	counts := &countingObserver{}
	srv2, addr2, _ := startPacketServer(t, admitted(t, rotate(echo), none, counts))
	client2 := udpClient(t, addr2)
	for i := range 3 {
		if _, err := client2.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return len(none.seen().peers) == i+1 })
		waitFor(t, func() bool { return srv2.ActiveSessions() == 0 })
	}
	if _, results, _ := counts.snapshot(); results[ResultDenied] != 3 || counts.dropped(DropDenied) != 0 {
		t.Errorf("results = %v, dropped = %v; want three flows denied and nothing dropped unasked", results, counts.dropped(DropDenied))
	}
}

// past the bound on held clients the oldest hold makes room: the newest denial is always held,
// and a client whose hold was evicted is judged again rather than dropped unasked
func TestPacketServerBoundsTheClientsItHolds(t *testing.T) {
	prev := maxHeldFlows
	maxHeldFlows = 3
	t.Cleanup(func() { maxHeldFlows = prev })
	adm := &verdicts{peer: Drop}
	counts := &countingObserver{}
	srv, addr, _ := startPacketServer(t, admitted(t, rotate(udpEcho(t, "echo:")), adm, counts))
	clients := make([]*net.UDPConn, 4)
	for i := range clients {
		clients[i] = udpClient(t, addr)
		if _, err := clients[i].Write([]byte("hi")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return len(adm.seen().peers) == i+1 })
		waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	}
	srv.mu.Lock()
	held, slots := len(srv.held), len(srv.heldKeys)
	srv.mu.Unlock()
	if held != 3 || slots != 3 {
		t.Errorf("holding %d clients in %d slots, want the bound of 3", held, slots)
	}
	// the first client's hold made room for the fourth: it is judged again, and held anew in place
	// of the second; the third and fourth are dropped unasked, and the second is judged again
	if _, err := clients[0].Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(adm.seen().peers) == 5 })
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	before := counts.dropped(DropDenied)
	for _, c := range clients[2:] {
		if _, err := c.Write([]byte("again")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return counts.dropped(DropDenied) == before+2 })
	if _, err := clients[1].Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(adm.seen().peers) == 6 })
	srv.mu.Lock()
	held, slots = len(srv.held), len(srv.heldKeys)
	srv.mu.Unlock()
	if held != 3 || slots != 3 {
		t.Errorf("holding %d clients in %d slots after evictions, want the bound of 3", held, slots)
	}
}

type selective struct {
	deny atomic.Pointer[netip.AddrPort]
}

func (a *selective) Datagram(f Flow, _ int) Verdict {
	if d := a.deny.Load(); d != nil && f.Client == *d {
		return Drop
	}
	return Allow
}

func (*selective) Peer(Flow) Verdict { return Allow }
func (*selective) Flow(Flow) Verdict { return Allow }
func (*selective) Datagrams() bool   { return true }

// a client whose datagrams are all denied dials nothing and its flow ends at the idle timeout
// however much it sends; once open, only relayed traffic keeps a flow from going idle
func TestPacketServerNeverDialsForADeniedClient(t *testing.T) {
	up := rotate(udpEcho(t, "echo:"))
	sel := &selective{}
	counts := &countingObserver{}
	var dials atomic.Int32
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": up}), Admission: sel, Observer: counts,
		Options: &options.Options{IdleTimeout: timeconv.Duration(300 * time.Millisecond)},
	}, countingDial(&dials))
	denied := udpClient(t, addr)
	deniedAddr := netip.MustParseAddrPort(denied.LocalAddr().String())
	sel.deny.Store(&deniedAddr)
	allowed := udpClient(t, addr)
	if got := datagram(t, allowed, "ok"); got != "echo:ok" {
		t.Fatalf("reply = %q", got)
	}
	sent := 0
	for stop := time.Now().Add(700 * time.Millisecond); time.Now().Before(stop); sent++ {
		if _, err := denied.Write([]byte("no")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, func() bool { return counts.dropped(DropDenied) >= float64(sent-2) })
	if dials.Load() != 1 {
		t.Errorf("%d dials, want the allowed client's alone", dials.Load())
	}
	// the denied flow expired at least once while its client kept sending, and expires once more
	waitFor(t, func() bool { return srv.ActiveSessions() == 1 })
	if _, results, _ := counts.snapshot(); results[ResultDenied] < 1 || results[ResultProxied] != 1 {
		t.Errorf("results = %v", results)
	}

	allowedAddr := netip.MustParseAddrPort(allowed.LocalAddr().String())
	sel.deny.Store(&allowedAddr)
	_, routes := up.seen()
	for stop := time.Now().Add(700 * time.Millisecond); time.Now().Before(stop); {
		if _, err := allowed.Write([]byte("no")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(routes) != 1 || routes[0].closed.Load() != 1 {
		t.Errorf("the open flow's route was closed %d times while its client sent only denied datagrams; want once, at idle",
			routes[0].closed.Load())
	}
	if dials.Load() != 1 {
		t.Errorf("%d dials, want no new one for a client whose datagrams are denied", dials.Load())
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
}

// closing the server ends a flow still awaiting an allowed datagram at once, not at its idle timeout
func TestPacketServerCloseEndsAWaitingFlow(t *testing.T) {
	sel := &selective{}
	var dials atomic.Int32
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": rotate(udpEcho(t, "echo:"))}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(time.Minute)}, Admission: sel,
	}, countingDial(&dials))
	client := udpClient(t, addr)
	clientAddr := netip.MustParseAddrPort(client.LocalAddr().String())
	sel.deny.Store(&clientAddr)
	if _, err := client.Write([]byte("no")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 1 })
	closed := make(chan struct{})
	go func() {
		_ = srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("closing waited on a flow that was awaiting an allowed datagram")
	}
	if dials.Load() != 0 || srv.ActiveSessions() != 0 {
		t.Errorf("dials = %d, sessions = %d after the close", dials.Load(), srv.ActiveSessions())
	}
}

type denyingAllBut struct {
	allow atomic.Pointer[netip.AddrPort]
}

func (a *denyingAllBut) Datagram(f Flow, _ int) Verdict {
	if p := a.allow.Load(); p != nil && f.Client == *p {
		return Allow
	}
	return Drop
}

func (*denyingAllBut) Peer(Flow) Verdict { return Allow }
func (*denyingAllBut) Flow(Flow) Verdict { return Allow }
func (*denyingAllBut) Datagrams() bool   { return true }

// flows awaiting an allowed datagram have an allowance apart from the session bound; at it the
// oldest waiter is told to expire and the newcomer refused, so denied sources never fill the sessions
func TestPacketServerWaitingFlowsHaveTheirOwnAllowance(t *testing.T) {
	prev := maxWaitingFlows
	maxWaitingFlows = 2
	t.Cleanup(func() { maxWaitingFlows = prev })
	echo := udpEcho(t, "echo:")
	adm := &denyingAllBut{}
	counts := &countingObserver{}
	var dials atomic.Int32
	config := func(a Admission) *Config {
		return &Config{
			Table: tableOf(t, map[string]Upstream{"": rotate(echo)}), Admission: a, Observer: counts,
			MaxConnections: 3,
		}
	}
	srv, addr, _ := startPacketServerWith(t, config(adm), countingDial(&dials))
	waitingFlows := func() (waiting, sessions int) {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.waiting, len(srv.sessions)
	}
	deny := func(n int) {
		if _, err := udpClient(t, addr).Write([]byte("no")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return counts.dropped(DropDenied) == float64(n) })
	}
	settled := func(waiting, sessions int) {
		t.Helper()
		waitFor(t, func() bool { w, s := waitingFlows(); return w == waiting && s == sessions })
	}
	// two denied sources wait; a third finds the allowance full, is refused, and the oldest waiter
	// is told to expire; its place freed, a fourth waits again
	deny(1)
	deny(2)
	settled(2, 2)
	deny(3)
	waitFor(t, func() bool {
		_, results, _ := counts.snapshot()
		return results[ResultRefused] == 1 && results[ResultDenied] == 1
	})
	settled(1, 1)
	deny(4)
	settled(2, 2)
	if dials.Load() != 0 {
		t.Errorf("%d dials for denied sources", dials.Load())
	}
	// the session bound of three does not count the waiters: three allowed sources open, a fourth is refused
	allowed := udpClient(t, addr)
	allowedAddr := netip.MustParseAddrPort(allowed.LocalAddr().String())
	adm.allow.Store(&allowedAddr)
	if got := datagram(t, allowed, "yes"); got != "echo:yes" {
		t.Fatalf("reply = %q", got)
	}
	srv.Update(config(&verdicts{datagrams: true}))
	for range 2 {
		if got := datagram(t, udpClient(t, addr), "yes"); got != "echo:yes" {
			t.Fatalf("reply = %q", got)
		}
	}
	settled(2, 5)
	if _, err := udpClient(t, addr).Write([]byte("yes")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultRefused] == 2 })
	if w, s := waitingFlows(); w != 2 || s != 5 || dials.Load() != 3 {
		t.Errorf("waiting = %d, sessions = %d, dials = %d; want two waiters, three open flows and no more", w, s, dials.Load())
	}

	// a waiter that is allowed leaves the allowance before its dial, whether or not the dial succeeds
	late := &denyingAllBut{}
	failures := &countingObserver{}
	srv2, addr2, _ := startPacketServerWith(t, admitted(t, Static("127.0.0.1:9"), late, failures),
		func(context.Context, string) (net.Conn, error) { return nil, syscall.ECONNREFUSED })
	client := udpClient(t, addr2)
	if _, err := client.Write([]byte("no")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { srv2.mu.Lock(); defer srv2.mu.Unlock(); return srv2.waiting == 1 })
	clientAddr := netip.MustParseAddrPort(client.LocalAddr().String())
	late.allow.Store(&clientAddr)
	if _, err := client.Write([]byte("yes")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, results, _ := failures.snapshot(); return results[ResultDialFailed] == 1 })
	srv2.mu.Lock()
	waiting := srv2.waiting
	srv2.mu.Unlock()
	if waiting != 0 {
		t.Errorf("waiting = %d after the allowed datagram, want the allowance released", waiting)
	}
}

type gatedDatagram struct {
	client atomic.Pointer[netip.AddrPort]
	gate   chan struct{}
	asked  atomic.Int32
}

func (g *gatedDatagram) Datagram(f Flow, _ int) Verdict {
	g.asked.Add(1)
	if c := g.client.Load(); c != nil && f.Client == *c {
		<-g.gate
	}
	return Allow
}

func (*gatedDatagram) Peer(Flow) Verdict { return Allow }
func (*gatedDatagram) Flow(Flow) Verdict { return Allow }
func (*gatedDatagram) Datagrams() bool   { return true }

// the first datagram was allowed under one config; a swap before it is relayed has the new
// admission judge it, whether the swap lands during the dial or during the judging itself
func TestPacketServerRejudgesTheFirstDatagramAfterASwap(t *testing.T) {
	echo := udpEcho(t, "echo:")
	// swapDuringDial sends one datagram under before, swaps to after while its dial is gated,
	// releases the dial and reports whether the datagram was relayed
	swapDuringDial := func(before, after Admission) (bool, *countingObserver) {
		release := make(chan struct{})
		var dialing atomic.Int32
		counts := &countingObserver{}
		srv, addr, _ := startPacketServerWith(t, admitted(t, rotate(echo), before, counts),
			func(ctx context.Context, addr string) (net.Conn, error) {
				dialing.Add(1)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				var d net.Dialer
				return d.DialContext(ctx, "udp", addr)
			})
		client := udpClient(t, addr)
		if _, err := client.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return dialing.Load() == 1 })
		srv.Update(admitted(t, rotate(echo), after, counts))
		close(release)
		_ = client.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err := client.Read(make([]byte, 16))
		return err == nil, counts
	}
	first, second := &verdicts{datagrams: true}, &verdicts{datagram: Drop, datagrams: true}
	if relayed, counts := swapDuringDial(first, second); relayed || counts.dropped(DropDenied) != 1 {
		t.Errorf("relayed = %v, dropped = %v; want the new admission's denial to hold", relayed, counts.dropped(DropDenied))
	}
	if a, b := first.seen(), second.seen(); len(a.sizes) != 1 || len(b.sizes) != 1 {
		t.Errorf("judged %d times under the old config and %d under the new; want once each", len(a.sizes), len(b.sizes))
	}
	unjudged := &verdicts{datagram: Drop}
	if relayed, _ := swapDuringDial(&verdicts{datagrams: true}, unjudged); !relayed || len(unjudged.seen().sizes) != 0 {
		t.Errorf("relayed = %v, asked %d times; want the datagram relayed unasked under a config that judges nothing",
			relayed, len(unjudged.seen().sizes))
	}

	gated := &gatedDatagram{gate: make(chan struct{})}
	var dials atomic.Int32
	counts := &countingObserver{}
	srv, addr, _ := startPacketServerWith(t, admitted(t, rotate(echo), gated, counts), countingDial(&dials))
	client := udpClient(t, addr)
	clientAddr := netip.MustParseAddrPort(client.LocalAddr().String())
	gated.client.Store(&clientAddr)
	if _, err := client.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return gated.asked.Load() == 1 })
	denying := &verdicts{datagram: Drop, datagrams: true}
	srv.Update(admitted(t, rotate(echo), denying, counts))
	close(gated.gate)
	waitFor(t, func() bool { return counts.dropped(DropDenied) == 1 })
	if dials.Load() != 0 || len(denying.seen().sizes) != 1 {
		t.Errorf("dials = %d, judged %d times by the new admission; want none and once", dials.Load(), len(denying.seen().sizes))
	}
}

// a slot in the hold ring evicts only the hold it recorded: a hold forgotten and remade sits in
// a new slot, and the old one evicts nothing, however many times that happens
func TestPacketServerHoldRingForgetsStaleSlots(t *testing.T) {
	prev := maxHeldFlows
	maxHeldFlows = 3
	t.Cleanup(func() { maxHeldFlows = prev })
	srv := NewPacketServer("held", &Config{})
	t.Cleanup(func() { _ = srv.Close() })
	gen := srv.cfg.Load().gen
	live := func(key string) bool {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.heldLive(key, gen)
	}
	sizes := func() (held, slots int) {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return len(srv.held), len(srv.heldKeys)
	}
	srv.hold("A", time.Minute, gen)
	srv.hold("B", time.Nanosecond, gen)
	srv.hold("C", time.Minute, gen)
	if live("B") {
		t.Fatal("an expired hold was live")
	}
	srv.hold("B", time.Minute, gen)
	if live("A") || !live("B") || !live("C") {
		t.Error("remaking B did not evict A, the oldest live hold")
	}
	srv.hold("D", time.Minute, gen)
	if !live("B") || !live("C") || !live("D") {
		t.Error("evicting the stale B slot took the fresh B hold with it")
	}
	srv.hold("E", time.Minute, gen)
	if live("C") || !live("B") || !live("D") || !live("E") {
		t.Error("E did not evict C")
	}
	srv.hold("F", time.Minute, gen)
	if live("B") || !live("D") || !live("E") || !live("F") {
		t.Error("F did not evict the fresh B, the oldest live hold by then")
	}
	for i := range 1000 {
		key := string(rune('a' + i%5))
		srv.hold(key, time.Nanosecond, gen)
		_ = live(key)
		srv.hold(key, time.Minute, gen)
		if held, slots := sizes(); held > 3 || slots > 3 {
			t.Fatalf("holding %d clients in %d slots after %d cycles, bound 3", held, slots, i+1)
		}
	}
	srv.mu.Lock()
	stale := srv.heldLive("e", gen+1)
	srv.mu.Unlock()
	if stale {
		t.Error("a hold made under an earlier config was live")
	}
}

// a waiter whose datagram is allowed takes a relaying session only if one is free, else it is
// refused; once a session ends, one refused client's fresh datagram takes its place, not more
func TestPacketServerPromotesWaitersWithinTheSessionBound(t *testing.T) {
	echo := udpEcho(t, "echo:")
	adm := &denyingAllBut{}
	counts := &countingObserver{}
	var dials atomic.Int32
	config := func(a Admission) *Config {
		return &Config{
			Table: tableOf(t, map[string]Upstream{"": rotate(echo)}), Admission: a, Observer: counts,
			MaxConnections: 1, Options: &options.Options{IdleTimeout: timeconv.Duration(time.Second)},
		}
	}
	srv, addr, _ := startPacketServerWith(t, config(adm), countingDial(&dials))
	// three waiters first, since a new flow needs a free session at admit; then the one session opens
	waiters := make([]*net.UDPConn, 3)
	for i := range waiters {
		waiters[i] = udpClient(t, addr)
		if _, err := waiters[i].Write([]byte("no")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return counts.dropped(DropDenied) == float64(i+1) })
	}
	waitFor(t, func() bool { srv.mu.Lock(); defer srv.mu.Unlock(); return srv.waiting == 3 })
	open := udpClient(t, addr)
	openAddr := netip.MustParseAddrPort(open.LocalAddr().String())
	adm.allow.Store(&openAddr)
	if got := datagram(t, open, "one"); got != "echo:one" {
		t.Fatalf("reply = %q", got)
	}
	// a swap allows everyone: each waiter's next datagram is allowed, but the one session is taken
	srv.Update(config(&verdicts{datagrams: true}))
	for _, w := range waiters {
		if _, err := w.Write([]byte("yes")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultRefused] == 3 })
	waitFor(t, func() bool { return srv.ActiveSessions() == 1 })
	if dials.Load() != 1 {
		t.Errorf("%d dials, want the open flow's alone", dials.Load())
	}
	for i, w := range waiters {
		_ = w.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if _, err := w.Read(make([]byte, 16)); err == nil {
			t.Errorf("waiter %d was relayed past the session bound", i)
		}
	}
	// the open flow goes idle; one refused client's datagram then takes its place, and the next is refused
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	if got := datagram(t, waiters[0], "again"); got != "echo:again" {
		t.Fatalf("reply = %q", got)
	}
	if _, err := waiters[1].Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultRefused] == 4 })
	if dials.Load() != 2 || srv.ActiveSessions() != 1 {
		t.Errorf("dials = %d, sessions = %d; want one new flow in the freed session", dials.Load(), srv.ActiveSessions())
	}
}

type gatedAllow struct {
	client atomic.Pointer[netip.AddrPort]
	gate   chan struct{}
	asked  atomic.Int32
}

// the gated client's first datagram is denied and its second waits on the gate, then is
// allowed; everyone else is denied
func (g *gatedAllow) Datagram(f Flow, _ int) Verdict {
	if c := g.client.Load(); c != nil && f.Client == *c {
		if g.asked.Add(1) == 1 {
			return Drop
		}
		<-g.gate
		return Allow
	}
	return Drop
}

func (*gatedAllow) Peer(Flow) Verdict { return Allow }
func (*gatedAllow) Flow(Flow) Verdict { return Allow }
func (*gatedAllow) Datagrams() bool   { return true }

// a waiter evicted while its admission was still deciding never dials, whatever the decision:
// it keeps its place until it has exited, and the newcomer is refused meanwhile
func TestPacketServerEvictedWaiterNeverDials(t *testing.T) {
	prev := maxWaitingFlows
	maxWaitingFlows = 1
	t.Cleanup(func() { maxWaitingFlows = prev })
	adm := &gatedAllow{gate: make(chan struct{})}
	counts := &countingObserver{}
	var dials atomic.Int32
	srv, addr, _ := startPacketServerWith(t, admitted(t, rotate(udpEcho(t, "echo:")), adm, counts), countingDial(&dials))
	t.Cleanup(func() {
		select {
		case <-adm.gate:
		default:
			close(adm.gate)
		}
	})
	waiting := func() int {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.waiting
	}
	waiter := udpClient(t, addr)
	waiterAddr := netip.MustParseAddrPort(waiter.LocalAddr().String())
	adm.client.Store(&waiterAddr)
	if _, err := waiter.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return counts.dropped(DropDenied) == 1 && waiting() == 1 })
	if _, err := waiter.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return adm.asked.Load() == 2 })
	// the allowance is full while the waiter's admission decides: a newcomer is refused, and the
	// waiter, told to expire, keeps its place
	if _, err := udpClient(t, addr).Write([]byte("no")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultRefused] == 1 })
	if waiting() != 1 {
		t.Errorf("waiting = %d while the evicted waiter's admission still decides, want its place kept", waiting())
	}
	close(adm.gate)
	waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultDenied] == 1 })
	waitFor(t, func() bool { return waiting() == 0 && srv.ActiveSessions() == 0 })
	waitFor(t, func() bool { active, _, _ := counts.snapshot(); return active == 0 })
	if dials.Load() != 0 {
		t.Errorf("%d dials for a waiter evicted before its admission allowed it", dials.Load())
	}
	_ = waiter.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := waiter.Read(make([]byte, 16)); err == nil {
		t.Error("an evicted waiter's datagram was relayed")
	}
}

// promotion turns a flow back when it was closed, told to expire or evicted meanwhile, or when no
// session is free, and moves a waiter into the sessions otherwise; a flow that never waited passes
func TestPromoteWaitingOutcomes(t *testing.T) {
	srv := NewPacketServer("promote", &Config{MaxConnections: 1})
	t.Cleanup(func() { _ = srv.Close() })
	waiter := func(name string) *udpSession {
		sess := newUDPSession(&net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 4000})
		srv.mu.Lock()
		srv.sessions[name] = sess
		srv.opening++
		srv.mu.Unlock()
		if !srv.startWaiting(sess) {
			t.Fatalf("%s could not wait", name)
		}
		return sess
	}
	waiting := func() int {
		srv.mu.Lock()
		defer srv.mu.Unlock()
		return srv.waiting
	}
	expired := waiter("expired")
	expired.mu.Lock()
	expired.expired = true
	expired.mu.Unlock()
	if got := srv.promoteWaiting(expired); got != promoteExpired || waiting() != 1 {
		t.Errorf("an expired waiter promoted to %d with %d waiting; want it turned back, still waiting", got, waiting())
	}
	closed := waiter("closed")
	closed.mu.Lock()
	closed.closed = true
	closed.mu.Unlock()
	if got := srv.promoteWaiting(closed); got != promoteClosed || waiting() != 2 {
		t.Errorf("a closed waiter promoted to %d with %d waiting", got, waiting())
	}
	// an ordinary session takes the one slot: a waiter cannot be promoted past it
	srv.mu.Lock()
	srv.sessions["open"] = newUDPSession(&net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 4000})
	srv.mu.Unlock()
	full := waiter("full")
	if got := srv.promoteWaiting(full); got != promoteRefused || waiting() != 3 {
		t.Errorf("a waiter promoted to %d past the session bound, with %d waiting", got, waiting())
	}
	srv.mu.Lock()
	delete(srv.sessions, "open")
	srv.mu.Unlock()
	if got := srv.promoteWaiting(full); got != promoted || waiting() != 2 || full.waiter != nil {
		t.Errorf("a waiter with a free session promoted to %d, with %d waiting", got, waiting())
	}
	never := newUDPSession(&net.UDPAddr{IP: net.IPv4(192, 0, 2, 3), Port: 4000})
	if got := srv.promoteWaiting(never); got != promoted {
		t.Errorf("a flow that never waited promoted to %d", got)
	}
}

// a flow told to expire, by eviction or its idle timer, in the moment between its datagram being
// allowed and its promotion never dials or relays: it ends with one result and one Ended
func TestPacketServerNeverDialsAFlowGoneBeforePromotion(t *testing.T) {
	prev := maxWaitingFlows
	maxWaitingFlows = 1
	t.Cleanup(func() { maxWaitingFlows = prev })
	for name, byTimer := range map[string]bool{"evicted": false, "timed out": true} {
		open := make(chan struct{})
		close(open)
		adm := &gatedAllow{gate: open}
		counts := &countingObserver{}
		var dials atomic.Int32
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := NewPacketServer("udp-test", admitted(t, rotate(udpEcho(t, "echo:")), adm, counts))
		srv.dial = countingDial(&dials)
		waiter := udpClient(t, pc.LocalAddr().String())
		waiterAddr := netip.MustParseAddrPort(waiter.LocalAddr().String())
		adm.client.Store(&waiterAddr)
		// the hook runs on the flow's worker once its datagram was allowed, before promotion: the
		// timed-out case sets what the idle timer's callback would; the evicted case waits to be evicted
		reached, proceed := make(chan struct{}), make(chan struct{})
		var released sync.Once
		srv.beforePromote = func(sess *udpSession) {
			if sess.client.String() != waiterAddr.String() {
				return
			}
			if byTimer {
				sess.mu.Lock()
				sess.expired = true
				sess.mu.Unlock()
				return
			}
			close(reached)
			<-proceed
		}
		go func() { _ = srv.Serve(pc) }()
		t.Cleanup(func() { _ = srv.Close() })
		t.Cleanup(func() { released.Do(func() { close(proceed) }) })
		if _, err := waiter.Write([]byte("first")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return counts.dropped(DropDenied) == 1 })
		if _, err := waiter.Write([]byte("second")); err != nil {
			t.Fatal(err)
		}
		if !byTimer {
			within(t, "the allowed datagram reaching promotion", func() { <-reached })
			if _, err := udpClient(t, pc.LocalAddr().String()).Write([]byte("no")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultRefused] == 1 })
			released.Do(func() { close(proceed) })
		}
		waitFor(t, func() bool { _, results, _ := counts.snapshot(); return results[ResultDenied] == 1 })
		waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
		waitFor(t, func() bool { active, _, _ := counts.snapshot(); return active == 0 })
		if dials.Load() != 0 {
			t.Errorf("%s: %d dials for a flow gone before its promotion", name, dials.Load())
		}
		_ = waiter.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := waiter.Read(make([]byte, 16)); err == nil {
			t.Errorf("%s: a flow gone before its promotion was relayed", name)
		}
		if _, results, _ := counts.snapshot(); results[ResultProxied] != 0 || results[ResultDialFailed] != 0 {
			t.Errorf("%s: results = %v", name, results)
		}
	}
}
