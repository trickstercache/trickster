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
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// udpEcho answers each datagram with the prefix and the datagram
func udpEcho(t *testing.T, prefix string) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte(prefix), buf[:n]...), addr)
		}
	}()
	return pc.LocalAddr().String()
}

func startPacketServer(t *testing.T, cfg *Config) (*PacketServer, string, chan error) {
	t.Helper()
	return startPacketServerWith(t, cfg, nil)
}

func startPacketServerWith(t *testing.T, cfg *Config,
	dial func(context.Context, string) (net.Conn, error),
) (*PacketServer, string, chan error) {
	t.Helper()
	return startPacketServerResolving(t, cfg, dial, nil)
}

func startPacketServerResolving(t *testing.T, cfg *Config,
	dial func(context.Context, string) (net.Conn, error),
	lookup func(context.Context, string) ([]net.IPAddr, error),
) (*PacketServer, string, chan error) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewPacketServer("udp-test", cfg)
	if dial != nil {
		srv.dial = dial
	}
	if lookup != nil {
		srv.lookup = lookup
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(pc) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, pc.LocalAddr().String(), done
}

func udpClient(t *testing.T, addr string) *net.UDPConn {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func datagram(t *testing.T, conn *net.UDPConn, msg string) string {
	t.Helper()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func TestPacketServerRelaysSessions(t *testing.T) {
	echo := udpEcho(t, "echo:")
	srv, addr, _ := startPacketServer(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(150 * time.Millisecond)},
	})
	client := udpClient(t, addr)
	for _, msg := range []string{"one", "two"} {
		if got := datagram(t, client, msg); got != "echo:"+msg {
			t.Errorf("reply = %q", got)
		}
	}
	if srv.ActiveSessions() != 1 {
		t.Errorf("sessions = %d", srv.ActiveSessions())
	}
	// a second client is a second session; both end once idle
	other := udpClient(t, addr)
	if got := datagram(t, other, "three"); got != "echo:three" {
		t.Errorf("reply = %q", got)
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 2 })
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	// a session kept busy by the client outlives the idle timeout
	for range 5 {
		if got := datagram(t, client, "busy"); got != "echo:busy" {
			t.Fatal(got)
		}
		time.Sleep(60 * time.Millisecond)
	}
	if srv.ActiveSessions() != 1 {
		t.Errorf("a busy session was expired")
	}
}

func TestPacketServerRotatesAcrossPool(t *testing.T) {
	a, b := udpEcho(t, "a:"), udpEcho(t, "b:")
	pl := pooledOf(t, "alb",
		member(originBackend(t, "a", a), 1, healthcheck.StatusPassing),
		member(originBackend(t, "b", b), 1, healthcheck.StatusPassing))
	_, addr, _ := startPacketServer(t, &Config{Table: tableOf(t, map[string]Upstream{"": FromBackend(pl)})})
	seen := make(map[string]int)
	for range 4 {
		seen[datagram(t, udpClient(t, addr), "x")]++
	}
	if seen["a:x"] != 2 || seen["b:x"] != 2 {
		t.Errorf("replies = %v", seen)
	}
}

func TestPacketServerDropsWhatItCannotRoute(t *testing.T) {
	for name, tbl := range map[string]*Table{
		"empty":     NewTable(),
		"no_member": tableOf(t, map[string]Upstream{"": FromBackend(pooledOf(t, "none"))}),
		"bad_addr":  tableOf(t, map[string]Upstream{"": Static("not an address")}),
	} {
		t.Run(name, func(t *testing.T) {
			// the flow is held, dropping what it receives, until it expires
			shortFailedLifetime(t, 100*time.Millisecond)
			srv, addr, _ := startPacketServer(t, &Config{Table: tbl})
			client := udpClient(t, addr)
			if _, err := client.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			_ = client.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, err := client.Read(make([]byte, 16)); err == nil {
				t.Error("an unroutable datagram was answered")
			}
			if srv.ActiveSessions() != 1 {
				t.Errorf("sessions = %d, want the failed flow held", srv.ActiveSessions())
			}
			waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
		})
	}
}

func TestPacketServerShutdown(t *testing.T) {
	// a shutdown completes once the established session has gone idle
	echo := udpEcho(t, "echo:")
	srv, addr, done := startPacketServer(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(150 * time.Millisecond)},
	})
	client := udpClient(t, addr)
	if got := datagram(t, client, "hi"); got != "echo:hi" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve returned %v", err)
	}
	if srv.ActiveSessions() != 0 {
		t.Errorf("sessions after shutdown = %d", srv.ActiveSessions())
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(pc); !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve after Shutdown = %v", err)
	}
	srv.Update(nil)
	if srv.cfg.Load() == nil {
		t.Error("a nil update must leave an empty configuration")
	}
	// a shutdown whose context has already ended reports so and leaves the close to Close, as
	// the listener group does after a drain deadline
	srv2, addr2, _ := startPacketServer(t, &Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	if got := datagram(t, udpClient(t, addr2), "hi"); got != "echo:hi" {
		t.Fatal(got)
	}
	ended, cancelEnded := context.WithCancel(context.Background())
	cancelEnded()
	if err := srv2.Shutdown(ended); !errors.Is(err, context.Canceled) {
		t.Errorf("Shutdown with an ended context = %v", err)
	}
	_ = srv2.Close()
	if srv2.ActiveSessions() != 0 {
		t.Errorf("sessions after Close = %d", srv2.ActiveSessions())
	}
}

func shortFailedLifetime(t *testing.T, d time.Duration) {
	t.Helper()
	was := failedFlowLifetime
	failedFlowLifetime = d
	t.Cleanup(func() { failedFlowLifetime = was })
}

func TestPacketServerBoundsSessions(t *testing.T) {
	// beyond the session bound a new client is refused before any socket or worker is opened;
	// existing sessions go on, and an expired one frees its slot
	echo := udpEcho(t, "echo:")
	srv, addr, _ := startPacketServer(t, &Config{
		Table:          tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options:        &options.Options{IdleTimeout: timeconv.Duration(150 * time.Millisecond)},
		MaxConnections: 2,
	})
	first, second := udpClient(t, addr), udpClient(t, addr)
	if got := datagram(t, first, "a"); got != "echo:a" {
		t.Fatal(got)
	}
	if got := datagram(t, second, "b"); got != "echo:b" {
		t.Fatal(got)
	}
	third := udpClient(t, addr)
	if _, err := third.Write([]byte("c")); err != nil {
		t.Fatal(err)
	}
	_ = third.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := third.Read(make([]byte, 16)); err == nil {
		t.Fatal("a session beyond the bound was relayed")
	}
	if srv.ActiveSessions() != 2 {
		t.Errorf("sessions = %d", srv.ActiveSessions())
	}
	if got := datagram(t, first, "still"); got != "echo:still" {
		t.Errorf("an existing session was disturbed: %q", got)
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	if got := datagram(t, third, "now"); got != "echo:now" {
		t.Errorf("after expiry the refused client is admitted: %q", got)
	}
	srv.Update(&Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	if srv.maxSessions() != DefaultUDPMaxSessions {
		t.Errorf("default bound = %d", srv.maxSessions())
	}
}

func TestPacketServerOpensSessionsOffTheReceiveLoop(t *testing.T) {
	// a stalled resolution or dial for one client delays neither another client's traffic nor
	// a bounded shutdown; the stalled client's datagrams are kept and relayed once it opens
	echo := udpEcho(t, "echo:")
	release := make(chan struct{})
	var stalled atomic.Int32
	var d net.Dialer
	_, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(5 * time.Second)},
	}, func(ctx context.Context, a string) (net.Conn, error) {
		if stalled.CompareAndSwap(0, 1) {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return d.DialContext(ctx, "udp", a)
	})
	slow := udpClient(t, addr)
	if _, err := slow.Write([]byte("wait")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return stalled.Load() == 1 })
	fast := udpClient(t, addr)
	if got := datagram(t, fast, "go"); got != "echo:go" {
		t.Fatalf("another client waited on the stalled dial: %q", got)
	}
	close(release)
	buf := make([]byte, 64)
	_ = slow.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := slow.Read(buf)
	if err != nil || string(buf[:n]) != "echo:wait" {
		t.Errorf("the datagram kept while opening was not relayed: %q %v", buf[:n], err)
	}

	// a dial still pending when the drain deadline passes ends with the close that follows
	srv2, addr2, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(5 * time.Second)},
	}, func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := udpClient(t, addr2).Write([]byte("hang")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return srv2.ActiveSessions() == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv2.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown with a dial pending = %v, want the drain deadline", err)
	}
	closed := make(chan struct{})
	go func() { _ = srv2.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on the pending dial")
	}
}

func TestPacketServerMixedPoolRefusesTheInvalidShare(t *testing.T) {
	// a member under the reserved .invalid domain refuses its share without a lookup, so the
	// valid member's clients are served at once; a failed flow drops what it receives until it
	// expires rather than looking its upstream up again per datagram
	echo := udpEcho(t, "echo:")
	shortFailedLifetime(t, time.Second)
	pl := pooledOf(t, "alb",
		member(originBackend(t, "live", echo), 1, healthcheck.StatusPassing),
		member(originBackend(t, "gone", "unresolved.kgw.invalid:1"), 1, healthcheck.StatusPassing))
	srv, addr, _ := startPacketServer(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": FromBackend(pl)}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(time.Second)},
	})
	var served, refused int
	var refusedClient *net.UDPConn
	for range 4 {
		c := udpClient(t, addr)
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, 16)
		if n, err := c.Read(buf); err == nil && string(buf[:n]) == "echo:x" {
			served++
			continue
		}
		refused++
		refusedClient = c
	}
	if served != 2 || refused != 2 {
		t.Errorf("served %d refused %d", served, refused)
	}
	if srv.ActiveSessions() != 4 {
		t.Errorf("a refused flow is held until it expires: sessions = %d", srv.ActiveSessions())
	}
	if _, err := refusedClient.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
}
