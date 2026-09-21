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
	"crypto/tls"
	"go/parser"
	"go/token"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// the relay tells a route how its dial went and, if it connected, when the connection ended
func TestServerReportsToItsRoute(t *testing.T) {
	up := rotate(echoServer(t, "echo:", nil))
	_, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	conn := dialTCP(t, addr)
	if got := exchange(t, conn, "hi"); got != "echo:hi" {
		t.Fatalf("reply = %q", got)
	}
	flows, routes := up.seen()
	if len(flows) != 1 || len(routes) != 1 {
		t.Fatalf("%d flows, %d routes", len(flows), len(routes))
	}
	local := netip.MustParseAddrPort(conn.LocalAddr().String())
	if flows[0].Protocol != ProtocolTCP || flows[0].Client != local || flows[0].ServerName != "" {
		t.Errorf("flow = %+v, want the client %v", flows[0], local)
	}
	r := routes[0]
	if r.dialed.Load() != 1 || r.failed() || r.dialTook.Load() <= 0 {
		t.Errorf("dial report: %d calls, failed %v, took %d", r.dialed.Load(), r.failed(), r.dialTook.Load())
	}
	if r.closed.Load() != 0 {
		t.Error("the route was closed while its connection is open")
	}
	_ = conn.Close()
	waitFor(t, func() bool { return r.closed.Load() == 1 })
}

func TestServerReportsAFailedDialAndNothingAfter(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	up := rotate(dead)
	srv, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	expectClosed(t, dialTCP(t, addr))
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
	_, routes := up.seen()
	if len(routes) != 1 || routes[0].dialed.Load() != 1 || !routes[0].failed() {
		t.Fatalf("a failed dial was not reported: %d routes", len(routes))
	}
	if routes[0].closed.Load() != 0 {
		t.Error("a route whose dial failed was also closed")
	}
}

func TestServerOffersTheServerNameToItsUpstream(t *testing.T) {
	cert := selfSigned(t, "shop.example.com")
	up := rotate(echoServer(t, "tls:", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}))
	_, addr := startServer(t, ProtocolTLS, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", addr,
		&tls.Config{ServerName: "shop.example.com", InsecureSkipVerify: true}) // #nosec G402 -- a test certificate
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := exchange(t, conn, "hi"); got != "tls:hi" {
		t.Fatalf("reply = %q", got)
	}
	flows, _ := up.seen()
	if len(flows) != 1 || flows[0].Protocol != ProtocolTLS || flows[0].ServerName != "shop.example.com" {
		t.Errorf("flows = %+v", flows)
	}
}

func TestPacketServerReportsToItsRoute(t *testing.T) {
	up := rotate(udpEcho(t, "echo:"))
	srv, addr, _ := startPacketServer(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(150 * time.Millisecond)},
	})
	client := udpClient(t, addr)
	if got := datagram(t, client, "one"); got != "echo:one" {
		t.Fatalf("reply = %q", got)
	}
	_ = datagram(t, client, "two")
	flows, routes := up.seen()
	if len(flows) != 1 || len(routes) != 1 {
		t.Fatalf("a session of two datagrams made %d picks", len(flows))
	}
	local := netip.MustParseAddrPort(client.LocalAddr().String())
	if flows[0].Protocol != ProtocolUDP || flows[0].Client.Port() != local.Port() {
		t.Errorf("flow = %+v, want the client port %d", flows[0], local.Port())
	}
	if routes[0].dialed.Load() != 1 || routes[0].failed() {
		t.Error("the session's dial was not reported as a success")
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	waitFor(t, func() bool { return routes[0].closed.Load() == 1 })
}

// the relay engine knows nothing of backends, load balancing or metrics: what it needs from
// them it declares as Upstream, Route and Observer, and they adapt to it
func TestImportBoundary(t *testing.T) {
	const module = "github.com/trickstercache/trickster/v2/pkg/"
	forbidden := []string{module + "backends", module + "lb"}
	// tests may log; the relay itself may not
	forbiddenOutsideTests := []string{module + "observability"}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		files++
		for _, imp := range f.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			banned := forbidden
			if !strings.HasSuffix(e.Name(), "_test.go") {
				banned = append(slices.Clone(forbidden), forbiddenOutsideTests...)
			}
			for _, bad := range banned {
				if name == bad || strings.HasPrefix(name, bad+"/") {
					t.Errorf("%s imports %s: the relay must not depend on backends, the balancer or the metrics; "+
						"they adapt to it through Upstream and Observer", e.Name(), name)
				}
			}
		}
	}
	if files == 0 {
		t.Fatal("no source files were checked")
	}
}

// the relay reports its own events to its listener's observer, and to nothing else
func TestRelayReportsToItsObserver(t *testing.T) {
	counts := &countingObserver{}
	srv, addr := startServer(t, ProtocolTCP, &Config{
		Observer: counts, Table: tableOf(t, map[string]Upstream{"": rotate("", echoServer(t, "echo:", nil))}),
	})
	conn := dialTCP(t, addr)
	if got := exchange(t, conn, "hello"); got != "echo:hello" {
		t.Fatalf("reply = %q", got)
	}
	if active, _, _ := counts.snapshot(); active != 1 {
		t.Errorf("active while a connection is open = %d", active)
	}
	_ = conn.Close()
	expectClosed(t, dialTCP(t, addr))
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
	waitFor(t, func() bool { active, _, _ := counts.snapshot(); return active == 0 })
	_, results, bytes := counts.snapshot()
	if results[ResultProxied] != 1 || results[ResultNoUpstream] != 1 {
		t.Errorf("results = %v", results)
	}
	if bytes[DirectionIn] != int64(len("hello\n")) || bytes[DirectionOut] != int64(len("echo:hello\n")) {
		t.Errorf("bytes = %v", bytes)
	}

	udpCounts := &countingObserver{}
	_, udpAddr, _ := startPacketServer(t, &Config{
		Observer: udpCounts, Table: tableOf(t, map[string]Upstream{"": rotate(udpEcho(t, "echo:"))}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(100 * time.Millisecond)},
	})
	if got := datagram(t, udpClient(t, udpAddr), "ping"); got != "echo:ping" {
		t.Fatalf("reply = %q", got)
	}
	waitFor(t, func() bool { active, _, _ := udpCounts.snapshot(); return active == 0 })
	_, results, bytes = udpCounts.snapshot()
	if results[ResultProxied] != 1 || bytes[DirectionIn] != 4 || bytes[DirectionOut] != 9 {
		t.Errorf("udp results = %v, bytes = %v", results, bytes)
	}
	// with no observer the relay still works; that is what every other test here runs with
	var none *Config
	none.observer().Opened()
	none.observer().Ended()
	none.observer().Result(ResultProxied)
	none.observer().Bytes(DirectionIn, 1)
	none.observer().Dropped(DropQueueFull)
}

func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// the first byte the upstream sends is reported once, however much follows
func TestServerReportsTheFirstUpstreamByte(t *testing.T) {
	up := rotate(echoServer(t, "echo:", nil))
	_, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	conn := dialTCP(t, addr)
	_, routes := func() ([]Flow, []*recordedRoute) {
		waitFor(t, func() bool { _, r := up.seen(); return len(r) == 1 })
		return up.seen()
	}()
	if routes[0].firstByte.Load() != 0 {
		t.Error("a first byte was reported before the upstream sent one")
	}
	for _, line := range []string{"one", "two", "three"} {
		if got := exchange(t, conn, line); got != "echo:"+line {
			t.Fatalf("reply = %q", got)
		}
	}
	if got := routes[0].firstByte.Load(); got != 1 {
		t.Errorf("first byte reported %d times", got)
	}
	flows, _ := up.seen()
	if flows[0].Listener != "test" {
		t.Errorf("flow listener = %q", flows[0].Listener)
	}
}

// a failed dial moves to the next route the upstream offers, inside one connect timeout, and
// every route tried hears how its dial went
func TestServerRetriesAFailedDial(t *testing.T) {
	dead := deadAddr(t)
	live := echoServer(t, "echo:", nil)
	up := retrying{rotate(live, dead, dead)}
	up.retries = 2
	_, addr := startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	if got := exchange(t, dialTCP(t, addr), "hi"); got != "echo:hi" {
		t.Fatalf("reply = %q", got)
	}
	_, routes := up.seen()
	if len(routes) != 3 || !routes[0].failed() || !routes[1].failed() || routes[2].failed() {
		t.Fatalf("%d routes tried; want two failed dials and then a success", len(routes))
	}
	if routes[0].closed.Load() != 0 || routes[1].closed.Load() != 0 {
		t.Error("a route whose dial failed was also closed")
	}

	// retries are bounded by what the upstream offers
	none := retrying{rotate(dead)}
	_, addr = startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": none})})
	expectClosed(t, dialTCP(t, addr))
	waitFor(t, func() bool { _, r := none.seen(); return len(r) == 1 && r[0].failed() })

	// a final route is never retried, whatever the upstream could offer
	final := retrying{rotate(live, dead)}
	final.retries, final.final = 5, true
	_, addr = startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": final})})
	expectClosed(t, dialTCP(t, addr))
	waitFor(t, func() bool { _, r := final.seen(); return len(r) == 1 && r[0].failed() })
	time.Sleep(50 * time.Millisecond)
	if _, r := final.seen(); len(r) != 1 {
		t.Errorf("a final route was retried: %d routes", len(r))
	}

	// an upstream that runs out of routes mid-retry ends the connection
	refusing := retrying{rotate("", dead)}
	refusing.retries = 3
	_, addr = startServer(t, ProtocolTCP, &Config{Table: tableOf(t, map[string]Upstream{"": refusing})})
	expectClosed(t, dialTCP(t, addr))
}

// the connect timeout covers every attempt together, not each one
func TestServerRetriesShareOneConnectTimeout(t *testing.T) {
	// an address that accepts nothing and refuses nothing: the dial hangs until it times out
	blackhole := "192.0.2.1:9"
	up := retrying{rotate(blackhole, blackhole, blackhole, blackhole)}
	up.retries = 10
	_, addr := startServer(t, ProtocolTCP, &Config{
		Table:   tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(150 * time.Millisecond)},
	})
	began := time.Now()
	expectClosed(t, dialTCP(t, addr))
	if took := time.Since(began); took > 2*time.Second {
		t.Errorf("retries ran for %v against a 150ms connect timeout", took)
	}
	if _, r := up.seen(); len(r) > 2 {
		t.Errorf("%d routes were dialed inside one connect timeout", len(r))
	}
}

// a udp upstream with nothing listening answers a datagram with a port-unreachable, which is
// the only sign it is down; the session's route hears of it when the session ends
func TestPacketServerReportsARefusingUpstream(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gone := pc.LocalAddr().String()
	_ = pc.Close()
	up := rotate(gone)
	srv, addr, _ := startPacketServer(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(300 * time.Millisecond)},
	})
	client := udpClient(t, addr)
	for range 3 {
		_, _ = client.Write([]byte("anyone?"))
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	// a session that ends on a refusal is gone, so a later datagram may have opened another
	_, routes := up.seen()
	if len(routes) == 0 || routes[0].closed.Load() != 1 {
		t.Fatalf("%d routes", len(routes))
	}
	if routes[0].closeErr.Load() == nil {
		t.Skip("this platform did not report the refused datagram on the connected socket")
	}
	if routes[0].firstByte.Load() != 0 {
		t.Error("a first reply was reported from an upstream that never answered")
	}
}

func TestPacketServerReportsTheFirstReply(t *testing.T) {
	up := rotate(udpEcho(t, "echo:"))
	_, addr, _ := startPacketServer(t, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	client := udpClient(t, addr)
	for _, msg := range []string{"one", "two", "three"} {
		if got := datagram(t, client, msg); got != "echo:"+msg {
			t.Fatalf("reply = %q", got)
		}
	}
	_, routes := up.seen()
	if len(routes) != 1 || routes[0].firstByte.Load() != 1 {
		t.Errorf("first reply reported %d times over a session of three", routes[0].firstByte.Load())
	}
}
