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
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

// silentServer accepts connections and never sends or closes; it reports what it received
func silentServer(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var received atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
			go func() {
				buf := make([]byte, 1024)
				for {
					n, err := conn.Read(buf)
					received.Add(int64(n))
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), &received
}

func startInGroup(t *testing.T, lg *listener.Group, key string, svr *Server, limit int,
	proxy *listener.ProxyProtocolOptions,
) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	go func() {
		_ = lg.StartProtocolListener(key, ProtocolTCP, "127.0.0.1", port, limit, svr, nil, proxy)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l := lg.Get(key); l != nil && l.State() == listener.StateReady {
			return net.JoinHostPort("127.0.0.1", itoa(port))
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("listener did not become ready")
	return ""
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func TestForceCloseEndsARelayBlockedOnASilentUpstream(t *testing.T) {
	// a client that has half-closed leaves one pipe reading a silent upstream; the drain
	// deadline's force-close must reach that upstream, or the listener can never be removed
	logger.SetLogger(logging.NoopLogger())
	upstream, received := silentServer(t)
	srv := NewServer("force", ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": Static(upstream)})})
	lg := listener.NewGroup()
	addr := startInGroup(t, lg, "force", srv, 0, nil)
	client := dialTCP(t, addr)
	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return received.Load() == 5 })
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- lg.DrainAndCloseContext(ctx, "force") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("drain = %v, want the deadline to have expired", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("force-close hung on the half-closed relay")
	}
	expectClosed(t, client)
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
	srv.mu.Lock()
	upstreams := len(srv.upstreams)
	srv.mu.Unlock()
	if upstreams != 0 {
		t.Errorf("%d upstream sockets left open", upstreams)
	}
}

func TestForceCloseEndsAPendingDial(t *testing.T) {
	// a dial that has not completed when the server closes ends rather than holding the drain
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// a listener that never accepts fills its backlog, after which dials hang until they time out
	t.Cleanup(func() { _ = ln.Close() })
	srv, addr := startServer(t, ProtocolTCP, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static("10.255.255.1:9")}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(30 * time.Second)},
	})
	client := dialTCP(t, addr)
	waitFor(t, func() bool { return srv.ActiveConnections() == 1 })
	closed := make(chan struct{})
	go func() { _ = srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited on a pending dial")
	}
	expectClosed(t, client)
}

func TestUpstreamHalfCloseThroughTheListenerGroup(t *testing.T) {
	// the upstream ends its sending half first; the client must still be able to send the rest,
	// through the group's connection accounting, a connection limit and the PROXY protocol
	logger.SetLogger(logging.NoopLogger())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var got atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = conn.Write([]byte("bye\n"))
				_ = conn.(*net.TCPConn).CloseWrite()
				n, _ := io.Copy(io.Discard, conn)
				got.Store(n)
			}()
		}
	}()
	trusted, err := clientip.ParseTrusted([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer("half", ProtocolTCP, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static(ln.Addr().String())}), MaxConnections: 2,
	})
	lg := listener.NewGroup()
	t.Cleanup(func() { _ = lg.Shutdown(time.Second) })
	addr := startInGroup(t, lg, "half", srv, 0, listener.NewProxyProtocolOptions(true, trusted))
	client := dialTCP(t, addr)
	if _, err := client.Write([]byte("PROXY TCP4 192.0.2.1 127.0.0.1 40000 443\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil || line != "bye\n" {
		t.Fatalf("upstream greeting: %q %v", line, err)
	}
	// the upstream's half-close reaches the client as an EOF, and the client's side stays open
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("after the upstream half-closed: %v", err)
	}
	payload := strings.Repeat("x", 100000)
	if _, err := client.Write([]byte(payload)); err != nil {
		t.Fatalf("the client could not send after the upstream half-closed: %v", err)
	}
	_ = client.Close()
	waitFor(t, func() bool { return got.Load() == int64(len(payload)) })
}

func TestConnectionBoundWaitsForASlot(t *testing.T) {
	// the relay bounds its own connections as a limited listener would: an accept beyond the
	// bound waits for a connection to end rather than being refused
	echo := echoServer(t, "echo:", nil)
	srv, addr := startServer(t, ProtocolTCP, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static(echo)}), MaxConnections: 1,
	})
	first := dialTCP(t, addr)
	if got := exchange(t, first, "one"); got != "echo:one" {
		t.Fatal(got)
	}
	second := dialTCP(t, addr)
	_ = second.SetDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := second.Write([]byte("two\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(second).ReadString('\n'); err == nil {
		t.Fatal("a connection beyond the bound was relayed")
	}
	if srv.ActiveConnections() != 1 {
		t.Errorf("active = %d", srv.ActiveConnections())
	}
	_ = first.Close()
	_ = second.SetDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(second).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "echo:two" {
		t.Fatalf("after a slot freed: %q %v", line, err)
	}
	// raising the bound admits the waiting accept at once
	third := dialTCP(t, addr)
	srv.Update(&Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)}), MaxConnections: 4})
	if got := exchange(t, third, "three"); got != "echo:three" {
		t.Fatal(got)
	}
}

func TestIdleTimeoutIsConnectionWide(t *testing.T) {
	// a client receiving a stream while sending nothing is not idle, nor is one sending a stream
	// to an upstream that answers only at the end; a stalled connection, both sides blocked
	// writing, is
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	mode := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			m := <-mode
			go func() {
				defer conn.Close()
				switch m {
				case "download":
					for range 30 {
						if _, err := conn.Write([]byte("tick\n")); err != nil {
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
				case "upload":
					n, _ := io.Copy(io.Discard, conn)
					_, _ = conn.Write([]byte(itoa(int(n)) + "\n"))
				case "stall":
					_, _ = conn.Write(make([]byte, 64<<20))
				}
			}()
		}
	}()
	idle := 200 * time.Millisecond
	srv, addr := startServer(t, ProtocolTCP, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(ln.Addr().String())}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(idle)},
	})

	mode <- "download"
	client := dialTCP(t, addr)
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(client)
	if err != nil || strings.Count(string(data), "tick") != 30 {
		t.Errorf("one-way download for three idle periods: %d ticks, %v", strings.Count(string(data), "tick"), err)
	}

	mode <- "upload"
	client = dialTCP(t, addr)
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	var sent int
	for range 30 {
		n, err := client.Write([]byte("chunk"))
		if err != nil {
			t.Fatalf("one-way upload cut off: %v", err)
		}
		sent += n
		time.Sleep(20 * time.Millisecond)
	}
	_ = client.(*net.TCPConn).CloseWrite()
	reply, err := bufio.NewReader(client).ReadString('\n')
	if err != nil || strings.TrimSpace(reply) != itoa(sent) {
		t.Errorf("upload total = %q %v, want %d", reply, err, sent)
	}

	// neither side reads: both pipes block writing and the idle period ends the relay
	mode <- "stall"
	client = dialTCP(t, addr)
	go func() { _, _ = client.Write(make([]byte, 64<<20)) }()
	waitFor(t, func() bool { return srv.ActiveConnections() == 1 })
	started := time.Now()
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
	if elapsed := time.Since(started); elapsed > 5*idle+time.Second {
		t.Errorf("stalled relay lasted %v", elapsed)
	}
}
