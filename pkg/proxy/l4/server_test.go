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
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// echoServer answers each line with the prefix and the line, until the client half-closes
func echoServer(t *testing.T, prefix string, tlsConfig *tls.Config) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if tlsConfig != nil {
					conn = tls.Server(conn, tlsConfig)
				}
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if line != "" {
						_, _ = conn.Write([]byte(prefix + strings.TrimSpace(line) + "\n"))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func startServer(t *testing.T, protocol string, cfg *Config) (*Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer("test", protocol, cfg)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ln.Addr().String()
}

func tableOf(t *testing.T, entries map[string]Upstream) *Table {
	t.Helper()
	tbl := NewTable()
	for host, up := range entries {
		if err := tbl.Add(host, up); err != nil {
			t.Fatal(err)
		}
	}
	return tbl
}

func exchange(t *testing.T, conn net.Conn, line string) string {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(reply)
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("the connection was not closed")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestServerRelaysTCPWithHalfClose(t *testing.T) {
	echo := echoServer(t, "echo:", nil)
	srv, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	conn := dialTCP(t, addr)
	if got := exchange(t, conn, "hello"); got != "echo:hello" {
		t.Errorf("reply = %q", got)
	}
	if srv.ActiveConnections() != 1 {
		t.Errorf("active = %d", srv.ActiveConnections())
	}
	// a client that half-closes still receives what the upstream had left to say
	_, _ = conn.Write([]byte("last\n"))
	_ = conn.(*net.TCPConn).CloseWrite()
	rest, err := io.ReadAll(conn)
	if err != nil || strings.TrimSpace(string(rest)) != "echo:last" {
		t.Errorf("after half-close: %q %v", rest, err)
	}
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
}

func TestServerRotatesAcrossPoolAndRefusesADeadMembersShare(t *testing.T) {
	// each connection commits to one member: the live ones answer in turn, and the share of a
	// member that cannot be dialed is refused rather than handed to a sibling
	a, b := echoServer(t, "a:", nil), echoServer(t, "b:", nil)
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := deadLn.Addr().String()
	_ = deadLn.Close()
	pl := pooledOf(t, "alb",
		member(originBackend(t, "dead", dead), 1, healthcheck.StatusPassing),
		member(originBackend(t, "a", a), 1, healthcheck.StatusPassing),
		member(originBackend(t, "b", b), 1, healthcheck.StatusPassing))
	_, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": FromBackend(pl)})})
	seen := make(map[string]int)
	var refused int
	for range 6 {
		conn := dialTCP(t, addr)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte("x\n")); err != nil {
			refused++
			continue
		}
		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			refused++
			continue
		}
		seen[strings.TrimSpace(reply)]++
	}
	if seen["a:x"] != 2 || seen["b:x"] != 2 || refused != 2 {
		t.Errorf("replies = %v, refused %d; want each member its share", seen, refused)
	}
}

func TestServerRefusesWhatItCannotRoute(t *testing.T) {
	// nothing routed, no member, and a member that is dead
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := deadLn.Addr().String()
	_ = deadLn.Close()
	for name, tbl := range map[string]*Table{
		"empty":     NewTable(),
		"no_member": tableOf(t, map[string]Upstream{"": FromBackend(pooledOf(t, "none"))}),
		"dead":      tableOf(t, map[string]Upstream{"": Static(dead)}),
	} {
		t.Run(name, func(t *testing.T) {
			srv, addr := startServer(t, ProtocolTCP, &Config{Table: tbl})
			expectClosed(t, dialTCP(t, addr))
			waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
		})
	}
}

func TestServerSNIMatrix(t *testing.T) {
	cert := selfSigned(t, "shop.example.com", "api.example.com", "deep.wild.example.com", "other.org")
	tlsConf := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	exact := echoServer(t, "exact:", tlsConf)
	wild := echoServer(t, "wild:", tlsConf)
	catchAll := echoServer(t, "all:", tlsConf)
	tbl := tableOf(t, map[string]Upstream{
		"shop.example.com": Static(exact), "*.example.com": Static(wild), "": Static(catchAll),
	})
	srv, addr := startServer(t, ProtocolTLS, &Config{
		Table:   tbl,
		Options: &options.Options{ConnectTimeout: timeconv.Duration(time.Second)},
	})
	dialTLS := func(serverName string) *tls.Conn {
		t.Helper()
		conn := tls.Client(dialTCP(t, addr), &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: true,
		}) // #nosec G402 -- the test trusts its own certificate
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	for serverName, want := range map[string]string{
		"shop.example.com": "exact:hi", "api.example.com": "wild:hi",
		"deep.wild.example.com": "all:hi", "other.org": "all:hi",
	} {
		conn := dialTLS(serverName)
		if got := exchange(t, conn, "hi"); got != want {
			t.Errorf("%s: reply = %q, want %q", serverName, got, want)
		}
		_ = conn.Close()
	}
	// the client hello reached the upstream intact: the upstream terminated the session
	plain := dialTCP(t, addr)
	_, _ = plain.Write([]byte("not tls at all\n"))
	expectClosed(t, plain)
	silent := dialTCP(t, addr)
	expectClosed(t, silent)
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })

	// without a catch-all an unrouted name is refused before any upstream is dialed
	srv.Update(&Config{Table: tableOf(t, map[string]Upstream{"shop.example.com": Static(exact)})})
	updated := dialTLS("shop.example.com")
	if got := exchange(t, updated, "hi"); got != "exact:hi" {
		t.Errorf("after update: %q", got)
	}
	_ = updated.Close()
	unrouted := dialTLS("other.org")
	if err := unrouted.Handshake(); err == nil {
		t.Error("an unrouted server name completed a handshake")
	}
	tlsConn := dialTLS("shop.example.com")
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := tlsConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
}

func TestServerIdleTimeoutClosesQuietConnections(t *testing.T) {
	echo := echoServer(t, "echo:", nil)
	_, addr := startServer(t, ProtocolTCP, &Config{
		Table:   tableOf(t, map[string]Upstream{"": Static(echo)}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(100 * time.Millisecond)},
	})
	conn := dialTCP(t, addr)
	if got := exchange(t, conn, "hi"); got != "echo:hi" {
		t.Fatal(got)
	}
	started := time.Now()
	expectClosed(t, conn)
	if time.Since(started) > 2*time.Second {
		t.Error("the idle connection was not closed promptly")
	}
}

func TestServerDrainsUnderLoad(t *testing.T) {
	echo := echoServer(t, "echo:", nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer("drain", ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	const clients = 24
	conns := make([]net.Conn, clients)
	for i := range conns {
		conns[i] = dialTCP(t, ln.Addr().String())
		if got := exchange(t, conns[i], "open"); got != "echo:open" {
			t.Fatal(got)
		}
	}
	waitFor(t, func() bool { return srv.ActiveConnections() == clients })

	// a bounded drain returns when its context ends, leaving every connection in service
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with connections open = %v", err)
	}
	if err := <-serveErr; !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve returned %v", err)
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		t.Error("a draining server accepted a new connection")
	}
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() {
			if got := exchange(t, c, "during"); got != "echo:during" {
				t.Errorf("during drain: %q", got)
			}
		})
	}
	wg.Wait()
	if srv.ActiveConnections() != clients {
		t.Errorf("active during drain = %d", srv.ActiveConnections())
	}

	// connections that finish let an unbounded drain complete on its own
	half := conns[:clients/2]
	for _, c := range half {
		_ = c.Close()
	}
	waitFor(t, func() bool { return srv.ActiveConnections() == clients-len(half) })
	drained := make(chan error, 1)
	go func() { drained <- srv.Shutdown(context.Background()) }()
	select {
	case err := <-drained:
		t.Fatalf("Shutdown returned %v with connections still open", err)
	case <-time.After(50 * time.Millisecond):
	}
	for _, c := range conns[clients/2:] {
		_ = c.Close()
	}
	if err := <-drained; err != nil {
		t.Errorf("Shutdown after the last connection closed = %v", err)
	}
	if err := srv.Serve(ln); !errors.Is(err, ErrServerClosed) {
		t.Errorf("Serve after Shutdown = %v", err)
	}
}

func TestServerCloseEndsConnections(t *testing.T) {
	echo := echoServer(t, "echo:", nil)
	srv, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": Static(echo)})})
	conn := dialTCP(t, addr)
	if got := exchange(t, conn, "hi"); got != "echo:hi" {
		t.Fatal(got)
	}
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, conn)
	if srv.ActiveConnections() != 0 {
		t.Errorf("active after Close = %d", srv.ActiveConnections())
	}
	srv.Update(nil)
	if srv.cfg.Load() == nil {
		t.Error("a nil update must leave an empty configuration")
	}
}

func TestPipeClosesBothOnWriteFailure(t *testing.T) {
	// a destination that has gone away ends the relay in both directions at once
	src, srcPeer := net.Pipe()
	dst, dstPeer := net.Pipe()
	_ = dstPeer.Close()
	go func() { _, _ = srcPeer.Write([]byte("data")) }()
	var last atomic.Int64
	if n := pipe(dst, src, 0, &last); n != 0 {
		t.Errorf("bytes written to a closed destination = %d", n)
	}
	if _, err := srcPeer.Write([]byte("more")); err == nil {
		t.Error("the source was left open after the destination failed")
	}
	_ = dst.Close()
}
