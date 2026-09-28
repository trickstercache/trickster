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

package l4_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl/stream"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

func aclList(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	compiled, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func aclConfig(t *testing.T, protocol string, listener *ipacl.List, entries map[string]l4.Upstream, lists map[l4.Upstream]*ipacl.List) *l4.Config {
	t.Helper()
	table := l4.NewTable()
	for host, up := range entries {
		if err := table.Add(host, up); err != nil {
			t.Fatal(err)
		}
	}
	return &l4.Config{
		Table:     table,
		Admission: stream.New(protocol, listener, table, lists),
		Options:   &options.Options{ConnectTimeout: timeconv.Duration(2 * time.Second)},
	}
}

type stages struct {
	l4.Admission
	mu    sync.Mutex
	peers []l4.Flow
	flows []l4.Flow
}

func (s *stages) Peer(f l4.Flow) l4.Verdict {
	s.mu.Lock()
	s.peers = append(s.peers, f)
	s.mu.Unlock()
	return s.Admission.Peer(f)
}

func (s *stages) Flow(f l4.Flow) l4.Verdict {
	s.mu.Lock()
	s.flows = append(s.flows, f)
	s.mu.Unlock()
	return s.Admission.Flow(f)
}

func (s *stages) seen() (peers, flows []l4.Flow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]l4.Flow(nil), s.peers...), append([]l4.Flow(nil), s.flows...)
}

type tally struct {
	mu      sync.Mutex
	results map[string]int
	drops   map[string]int
}

func (c *tally) add(m *map[string]int, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *m == nil {
		*m = make(map[string]int)
	}
	(*m)[key]++
}

func (c *tally) Opened()                   {}
func (c *tally) Ended()                    {}
func (c *tally) Result(r string)           { c.add(&c.results, r) }
func (c *tally) Dropped(reason string)     { c.add(&c.drops, reason) }
func (c *tally) Bytes(string, int64)       {}
func (c *tally) result(name string) int    { c.mu.Lock(); defer c.mu.Unlock(); return c.results[name] }
func (c *tally) dropped(reason string) int { c.mu.Lock(); defer c.mu.Unlock(); return c.drops[reason] }

func waitUntil(t *testing.T, cond func() bool) {
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

func tcpEcho(t *testing.T, prefix string) (string, *atomic.Int32) {
	t.Helper()
	return serveEcho(t, prefix, nil)
}

func tlsEcho(t *testing.T, prefix string, cert tls.Certificate) (string, *atomic.Int32) {
	t.Helper()
	return serveEcho(t, prefix, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
}

func serveEcho(t *testing.T, prefix string, tlsConfig *tls.Config) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			go func() {
				defer conn.Close()
				if tlsConfig != nil {
					conn = tls.Server(conn, tlsConfig)
				}
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if line != "" {
						_, _ = conn.Write([]byte(prefix + line))
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), &n
}

func udpEcho(t *testing.T, prefix string) (string, *atomic.Int32) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	var n atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			read, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			n.Add(1)
			_, _ = pc.WriteTo(append([]byte(prefix), buf[:read]...), addr)
		}
	}()
	return pc.LocalAddr().String(), &n
}

func startTCP(t *testing.T, protocol string, cfg *l4.Config) (*l4.Server, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := l4.NewServer("test", protocol, cfg)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, ln.Addr().String()
}

func startUDP(t *testing.T, cfg *l4.Config) (*l4.PacketServer, string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := l4.NewPacketServer("udp-test", cfg)
	go func() { _ = srv.Serve(pc) }()
	t.Cleanup(func() { _ = srv.Close() })
	return srv, pc.LocalAddr().String()
}

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
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

func denial(t *testing.T, conn net.Conn) error {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(make([]byte, 1))
	if n != 0 {
		t.Fatalf("the server wrote %d bytes", n)
	}
	return err
}

func expectReset(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if errors.Is(err, syscall.ECONNRESET) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := denial(t, conn); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the client read %v, want a reset", err)
	}
}

func expectClose(t *testing.T, addr string) {
	t.Helper()
	conn := dial(t, addr)
	err := denial(t, conn)
	if err == nil || errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("the client read %v, want a close", err)
	}
}

func expectDials(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	time.Sleep(200 * time.Millisecond)
	if got := n.Load(); got != want {
		t.Fatalf("upstream accepted %d connections, want %d", got, want)
	}
}

func watch(cfg *l4.Config) *stages {
	s := &stages{Admission: cfg.Admission}
	cfg.Admission = s
	return s
}

func TestStreamTCPClientIP(t *testing.T) {
	echo, dials := tcpEcho(t, "echo:")
	up := l4.Static(echo)
	allow := aclList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
	deny := aclList(t, ipacl.Options{Action: "reject"})
	drop := aclList(t, ipacl.Options{Action: "drop"})

	t.Run("listener allow", func(t *testing.T) {
		_, addr := startTCP(t, l4.ProtocolTCP, aclConfig(t, l4.ProtocolTCP, allow, map[string]l4.Upstream{"": up}, nil))
		if got := exchange(t, dial(t, addr), "hi"); got != "echo:hi" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("listener deny", func(t *testing.T) {
		before := dials.Load()
		cfg := aclConfig(t, l4.ProtocolTCP, deny, map[string]l4.Upstream{"": up}, nil)
		cfg.MaxConnections = 1
		asked := watch(cfg)
		srv, addr := startTCP(t, l4.ProtocolTCP, cfg)
		expectReset(t, addr)
		waitUntil(t, func() bool { return srv.ActiveConnections() == 0 })
		peers, flows := asked.seen()
		if len(peers) != 1 || len(flows) != 0 {
			t.Fatalf("peer calls = %d, flow calls = %d", len(peers), len(flows))
		}
		expectDials(t, dials, before)
		// the slot is free: a second connection is accepted and judged
		expectReset(t, addr)
		waitUntil(t, func() bool {
			peers, _ := asked.seen()
			return len(peers) == 2 && srv.ActiveConnections() == 0
		})
		expectDials(t, dials, before)
	})
	t.Run("listener drop", func(t *testing.T) {
		before := dials.Load()
		_, addr := startTCP(t, l4.ProtocolTCP, aclConfig(t, l4.ProtocolTCP, drop, map[string]l4.Upstream{"": up}, nil))
		expectClose(t, addr)
		expectDials(t, dials, before)
	})
	t.Run("backend allow", func(t *testing.T) {
		_, addr := startTCP(t, l4.ProtocolTCP, aclConfig(t, l4.ProtocolTCP, nil, map[string]l4.Upstream{"": up}, map[l4.Upstream]*ipacl.List{up: allow}))
		if got := exchange(t, dial(t, addr), "hi"); got != "echo:hi" {
			t.Fatalf("reply = %q", got)
		}
	})
	t.Run("backend deny", func(t *testing.T) {
		before := dials.Load()
		cfg := aclConfig(t, l4.ProtocolTCP, nil, map[string]l4.Upstream{"": up}, map[l4.Upstream]*ipacl.List{up: deny})
		asked := watch(cfg)
		srv, addr := startTCP(t, l4.ProtocolTCP, cfg)
		expectReset(t, addr)
		waitUntil(t, func() bool { return srv.ActiveConnections() == 0 })
		peers, flows := asked.seen()
		if len(peers) != 1 || len(flows) != 1 {
			t.Fatalf("peer calls = %d, flow calls = %d", len(peers), len(flows))
		}
		expectDials(t, dials, before)
	})
	t.Run("listener then backend", func(t *testing.T) {
		// the listener drops, so the backend's reject is not what the client sees
		before := dials.Load()
		cfg := aclConfig(t, l4.ProtocolTCP, drop, map[string]l4.Upstream{"": up}, map[l4.Upstream]*ipacl.List{up: deny})
		asked := watch(cfg)
		_, addr := startTCP(t, l4.ProtocolTCP, cfg)
		expectClose(t, addr)
		_, flows := asked.seen()
		if len(flows) != 0 {
			t.Fatal("the backend list was judged after the listener denied")
		}
		expectDials(t, dials, before)
	})
}

func TestStreamPROXYClientIP(t *testing.T) {
	echo, dials := tcpEcho(t, "echo:")
	up := l4.Static(echo)
	// 127.0.0.1 is the socket. Only the header source 192.0.2.9 is allowed.
	clientIP := aclList(t, ipacl.Options{Allow: []string{"192.0.2.9"}})
	cfg := aclConfig(t, l4.ProtocolTCP, clientIP, map[string]l4.Upstream{"": up}, nil)
	asked := watch(cfg)
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &proxyproto.Listener{
		Listener:          tcp,
		ReadHeaderTimeout: time.Second,
		ConnPolicy: func(o proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			ap, err := netip.ParseAddrPort(o.Upstream.String())
			if err != nil || ap.Addr() != netip.MustParseAddr("127.0.0.1") {
				return proxyproto.SKIP, nil
			}
			return proxyproto.USE, nil
		},
	}
	srv := l4.NewServer("test", l4.ProtocolTCP, cfg)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	allowed := dialProxy(t, ln.Addr().String(), "192.0.2.9")
	if got := exchange(t, allowed, "hi"); got != "echo:hi" {
		t.Fatalf("reply = %q", got)
	}
	peers, _ := asked.seen()
	if len(peers) != 1 || peers[0].Client.Addr() != netip.MustParseAddr("192.0.2.9") {
		t.Fatalf("peer judged %+v, want 192.0.2.9", peers)
	}

	denied := dialProxy(t, ln.Addr().String(), "198.51.100.8")
	// the relay resets a plain TCP connection. This test's PROXY wrapper is not one, so deny closes it.
	if err := denial(t, denied); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("a denied PROXY source read %v, want the connection ended", err)
	}
	_ = allowed.Close()
	waitUntil(t, func() bool { return srv.ActiveConnections() == 0 })
	peers, flows := asked.seen()
	if len(peers) != 2 || peers[1].Client.Addr() != netip.MustParseAddr("198.51.100.8") || len(flows) != 1 {
		t.Fatalf("judged peers=%+v flows=%d", peers, len(flows))
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("upstream accepted %d connections, want the allowed one", got)
	}
}

func TestStreamPROXYPeerListIsNotAppliedToTheHeader(t *testing.T) {
	echo, _ := tcpEcho(t, "echo:")
	up := l4.Static(echo)
	// the socket list denies the header source; a backend list keeps an admission installed
	peer := aclList(t, ipacl.Options{Source: "peer", Allow: []string{"10.1.1.1"}})
	backend := aclList(t, ipacl.Options{Default: "allow"})
	cfg := aclConfig(t, l4.ProtocolTCP, peer, map[string]l4.Upstream{"": up}, map[l4.Upstream]*ipacl.List{up: backend})
	asked := watch(cfg)
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &proxyproto.Listener{
		Listener: tcp, ReadHeaderTimeout: time.Second,
		ConnPolicy: func(proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
			return proxyproto.USE, nil
		},
	}
	srv := l4.NewServer("test", l4.ProtocolTCP, cfg)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	conn := dialProxy(t, ln.Addr().String(), "192.0.2.9")
	if got := exchange(t, conn, "hi"); got != "echo:hi" {
		t.Fatalf("reply = %q", got)
	}
	peers, _ := asked.seen()
	if len(peers) != 1 || peers[0].Client.Addr() != netip.MustParseAddr("192.0.2.9") {
		t.Fatalf("peer judged %+v", peers)
	}
}

func dialProxy(t *testing.T, addr, src string) net.Conn {
	t.Helper()
	conn := dial(t, addr)
	h := proxyproto.HeaderProxyFromAddrs(2,
		&net.TCPAddr{IP: net.ParseIP(src), Port: 4242},
		&net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 80})
	if _, err := h.WriteTo(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func testCert(t *testing.T, hosts ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "l4 test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: hosts, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type countedConn struct {
	net.Conn
	read atomic.Int64
}

func (c *countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func TestStreamTLSClientIPAndRoute(t *testing.T) {
	cert := testCert(t, "shop.example.com", "other.example.com")
	exactEcho, exactDials := tlsEcho(t, "exact:", cert)
	wildEcho, wildDials := tlsEcho(t, "wild:", cert)
	exact := l4.Static(exactEcho)
	wild := l4.Static(wildEcho)
	listener := aclList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
	denyWild := aclList(t, ipacl.Options{Action: "reject"})
	cfg := aclConfig(t, l4.ProtocolTLS, listener, map[string]l4.Upstream{
		"shop.example.com": exact,
		"*.example.com":    wild,
	}, map[l4.Upstream]*ipacl.List{wild: denyWild})
	asked := watch(cfg)
	_, addr := startTCP(t, l4.ProtocolTLS, cfg)

	t.Run("exact backend", func(t *testing.T) {
		raw := &countedConn{Conn: dial(t, addr)}
		conn := tls.Client(raw, &tls.Config{ServerName: "shop.example.com", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- a test certificate
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := conn.Handshake(); err != nil {
			t.Fatal(err)
		}
		if got := exchange(t, conn, "hi"); got != "exact:hi" {
			t.Fatalf("reply = %q", got)
		}
		if wildDials.Load() != 0 {
			t.Fatal("the wildcard backend was dialed for the exact name")
		}
	})
	t.Run("wildcard backend deny", func(t *testing.T) {
		before := exactDials.Load()
		raw := &countedConn{Conn: dial(t, addr)}
		conn := tls.Client(raw, &tls.Config{ServerName: "other.example.com", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- a test certificate
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := conn.Handshake(); err == nil {
			t.Fatal("a denied backend completed the handshake")
		}
		if raw.read.Load() != 0 {
			t.Fatalf("the server wrote %d bytes after the denial", raw.read.Load())
		}
		if wildDials.Load() != 0 || exactDials.Load() != before {
			t.Fatalf("dials exact=%d wild=%d", exactDials.Load(), wildDials.Load())
		}
	})
	t.Run("no route", func(t *testing.T) {
		flowsBefore := len(mustFlows(asked))
		raw := &countedConn{Conn: dial(t, addr)}
		conn := tls.Client(raw, &tls.Config{ServerName: "missing.example", InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) // #nosec G402 -- a test certificate
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := conn.Handshake(); err == nil {
			t.Fatal("an unroutable name completed the handshake")
		}
		if raw.read.Load() != 0 {
			t.Fatal("the server wrote to an unroutable name")
		}
		if len(mustFlows(asked)) != flowsBefore {
			t.Fatal("a connection with no route reached the flow stage")
		}
		if wildDials.Load() != 0 {
			t.Fatal("an unroutable name was dialed")
		}
	})

	peers, flows := asked.seen()
	if len(peers) != 3 {
		t.Fatalf("peer calls = %d", len(peers))
	}
	for _, peer := range peers {
		if peer.ServerName != "" || peer.Client.Addr() != netip.MustParseAddr("127.0.0.1") {
			t.Fatalf("peer stage saw %+v", peer)
		}
	}
	if len(flows) != 2 || flows[0].ServerName != "shop.example.com" || flows[1].ServerName != "other.example.com" {
		t.Fatalf("flow stage saw %+v", flows)
	}

	t.Run("listener deny before the client hello", func(t *testing.T) {
		exactBefore, wildBefore := exactDials.Load(), wildDials.Load()
		blocked := aclList(t, ipacl.Options{Action: "drop"})
		cfg := aclConfig(t, l4.ProtocolTLS, blocked, map[string]l4.Upstream{"shop.example.com": exact}, nil)
		asked := watch(cfg)
		_, addr := startTCP(t, l4.ProtocolTLS, cfg)
		conn := dial(t, addr)
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := conn.Read(make([]byte, 8))
		if n != 0 || err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read %d, %v; want a close before the client hello", n, err)
		}
		peers, flows := asked.seen()
		if len(flows) != 0 || len(peers) != 1 || peers[0].ServerName != "" {
			t.Fatalf("peer=%+v flow calls=%d", peers, len(flows))
		}
		if exactDials.Load() != exactBefore || wildDials.Load() != wildBefore {
			t.Fatal("the client hello was dialed")
		}
	})
}

func mustFlows(s *stages) []l4.Flow {
	_, flows := s.seen()
	return flows
}

func TestStreamUDPACL(t *testing.T) {
	echo, dials := udpEcho(t, "echo:")
	up := l4.Static(echo)
	allow := aclList(t, ipacl.Options{Allow: []string{"127.0.0.1"}})
	for _, tc := range []struct {
		name     string
		listener *ipacl.List
		backend  *ipacl.List
	}{
		{name: "listener allow", listener: allow},
		{name: "backend allow", backend: allow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lists := map[l4.Upstream]*ipacl.List{}
			if tc.backend != nil {
				lists[up] = tc.backend
			}
			_, addr := startUDP(t, aclConfig(t, l4.ProtocolUDP, tc.listener, map[string]l4.Upstream{"": up}, lists))
			client := udpClient(t, addr)
			if got := udpRead(t, client, "hi"); got != "echo:hi" {
				t.Fatalf("reply = %q", got)
			}
		})
	}
	for _, action := range []string{"reject", "drop"} {
		t.Run(action, func(t *testing.T) {
			before := dials.Load()
			deny := aclList(t, ipacl.Options{Action: action})
			cfg := aclConfig(t, l4.ProtocolUDP, deny, map[string]l4.Upstream{"": up}, nil)
			asked := watch(cfg)
			obs := &tally{}
			cfg.Observer = obs
			srv, addr := startUDP(t, cfg)
			client := udpClient(t, addr)
			if _, err := client.Write([]byte("no")); err != nil {
				t.Fatal(err)
			}
			expectUDPSilence(t, client)
			waitUntil(t, func() bool { return obs.result(l4.ResultDenied) == 1 && srv.ActiveSessions() == 0 })
			peers, flows := asked.seen()
			if len(peers) != 1 || len(flows) != 0 {
				t.Fatalf("peer calls = %d, flow calls = %d", len(peers), len(flows))
			}
			held := obs.dropped(l4.DropDenied)
			if _, err := client.Write([]byte("again")); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, func() bool { return obs.dropped(l4.DropDenied) == held+1 })
			peers, _ = asked.seen()
			if len(peers) != 1 {
				t.Fatalf("held datagrams judged the peer %d times", len(peers))
			}
			if dials.Load() != before {
				t.Fatalf("upstream datagrams = %d, want %d", dials.Load(), before)
			}

			// a new generation releases the hold and judges the client again
			allowCfg := aclConfig(t, l4.ProtocolUDP, allow, map[string]l4.Upstream{"": up}, nil)
			allowed := watch(allowCfg)
			srv.Update(allowCfg)
			if got := udpRead(t, client, "now"); got != "echo:now" {
				t.Fatalf("reply after reload = %q", got)
			}
			peers, _ = allowed.seen()
			if len(peers) != 1 {
				t.Fatalf("the reloaded admission was asked %d times", len(peers))
			}
		})
	}
	t.Run("backend deny", func(t *testing.T) {
		deny := aclList(t, ipacl.Options{Action: "drop"})
		cfg := aclConfig(t, l4.ProtocolUDP, allow, map[string]l4.Upstream{"": up}, map[l4.Upstream]*ipacl.List{up: deny})
		asked := watch(cfg)
		_, addr := startUDP(t, cfg)
		client := udpClient(t, addr)
		before := dials.Load()
		if _, err := client.Write([]byte("no")); err != nil {
			t.Fatal(err)
		}
		expectUDPSilence(t, client)
		waitUntil(t, func() bool {
			peers, _ := asked.seen()
			return len(peers) == 1
		})
		if dials.Load() != before {
			t.Fatal("a denied backend was dialed")
		}
	})
	t.Run("established flow is not rejudged", func(t *testing.T) {
		cfg := aclConfig(t, l4.ProtocolUDP, allow, map[string]l4.Upstream{"": up}, nil)
		srv, addr := startUDP(t, cfg)
		client := udpClient(t, addr)
		if got := udpRead(t, client, "stay"); got != "echo:stay" {
			t.Fatalf("reply = %q", got)
		}
		deny := aclList(t, ipacl.Options{Action: "drop"})
		next := aclConfig(t, l4.ProtocolUDP, deny, map[string]l4.Upstream{"": up}, nil)
		asked := watch(next)
		srv.Update(next)
		if got := udpRead(t, client, "still"); got != "echo:still" {
			t.Fatalf("reply after reload = %q", got)
		}
		if peers, _ := asked.seen(); len(peers) != 0 {
			t.Fatalf("an established flow was judged %d times", len(peers))
		}
		fresh := udpClient(t, addr)
		if _, err := fresh.Write([]byte("new")); err != nil {
			t.Fatal(err)
		}
		expectUDPSilence(t, fresh)
		waitUntil(t, func() bool {
			peers, _ := asked.seen()
			return len(peers) == 1
		})
	})
}

func TestStreamUDPDeniedHoldIsNotRefreshed(t *testing.T) {
	echo, _ := udpEcho(t, "echo:")
	up := l4.Static(echo)
	deny := aclList(t, ipacl.Options{Action: "drop"})
	cfg := aclConfig(t, l4.ProtocolUDP, deny, map[string]l4.Upstream{"": up}, nil)
	asked := watch(cfg)
	obs := &tally{}
	cfg.Observer = obs
	_, addr := startUDP(t, cfg)
	client := udpClient(t, addr)
	if _, err := client.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return obs.result(l4.ResultDenied) == 1 })
	deniedAt := time.Now()
	// a datagram late in the original hold must not move the deadline
	time.Sleep(time.Until(deniedAt.Add(l4.DefaultDeniedHold - time.Second)))
	held := obs.dropped(l4.DropDenied)
	if _, err := client.Write([]byte("later")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 700*time.Millisecond, func() bool { return obs.dropped(l4.DropDenied) == held+1 }) {
		t.Fatal("the datagram inside the hold was not dropped")
	}
	if peers, _ := asked.seen(); len(peers) != 1 {
		t.Fatalf("a held datagram judged the peer %d times", len(peers))
	}
	time.Sleep(time.Until(deniedAt.Add(l4.DefaultDeniedHold + 400*time.Millisecond)))
	if _, err := client.Write([]byte("again")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		peers, _ := asked.seen()
		return len(peers) == 2
	})
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
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

func udpRead(t *testing.T, conn *net.UDPConn, msg string) string {
	t.Helper()
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return string(buf[:n])
}

func expectUDPSilence(t *testing.T, conn *net.UDPConn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 16)
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("a denied client read %q", buf[:n])
	}
}
