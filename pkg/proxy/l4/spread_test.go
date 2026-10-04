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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// spreading is an upstream that commits each flow to every one of its addresses at once
type spreading struct {
	addrs []string

	mu     sync.Mutex
	routes []*recordedRoute
}

func (u *spreading) route(addr string) *recordedRoute {
	r := &recordedRoute{addr: addr, final: true}
	u.mu.Lock()
	u.routes = append(u.routes, r)
	u.mu.Unlock()
	return r
}

func (u *spreading) to() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.addrs
}

func (u *spreading) empty() {
	u.mu.Lock()
	u.addrs = nil
	u.mu.Unlock()
}

func (u *spreading) Pick(Flow) (Route, bool) {
	addrs := u.to()
	if len(addrs) == 0 {
		return nil, false
	}
	return u.route(addrs[0]), true
}

func (u *spreading) Race(Flow) []Route {
	addrs := u.to()
	routes := make([]Route, len(addrs))
	for i, addr := range addrs {
		routes[i] = u.route(addr)
	}
	return routes
}

func (u *spreading) Mirror(Flow, Route) []Route {
	addrs := u.to()
	routes := make([]Route, 0, len(addrs))
	for _, addr := range addrs[1:] {
		routes = append(routes, u.route(addr))
	}
	return routes
}

func (u *spreading) seen() []*recordedRoute {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*recordedRoute(nil), u.routes...)
}

// refusedAddr is an address nothing listens at
func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func abandoned(r *recordedRoute) bool {
	err := r.dialErr.Load()
	return err != nil && errors.Is(*err, ErrAbandoned)
}

func TestServerRacesItsRoutes(t *testing.T) {
	counts := &countingObserver{}
	up := &spreading{addrs: []string{refusedAddr(t), echoServer(t, "a:", nil), echoServer(t, "b:", nil)}}
	srv, addr := startServer(t, ProtocolTCP, &Config{
		Observer: counts, Table: tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(2 * time.Second)},
	})
	conn := dialTCP(t, addr)
	reply := exchange(t, conn, "hi")
	if reply != "a:hi" && reply != "b:hi" {
		t.Fatalf("reply = %q", reply)
	}
	_ = conn.Close()
	waitFor(t, func() bool { return srv.ActiveConnections() == 0 })
	routes := up.seen()
	if len(routes) != 3 {
		t.Fatalf("%d routes", len(routes))
	}
	waitFor(t, func() bool {
		return routes[0].dialed.Load() == 1 && routes[1].dialed.Load() == 1 && routes[2].dialed.Load() == 1
	})
	var won, lost int
	for _, r := range routes[1:] {
		switch {
		case !r.failed():
			won++
			if r.closed.Load() != 1 || !strings.HasPrefix(reply, map[string]string{up.addrs[1]: "a:", up.addrs[2]: "b:"}[r.addr]) {
				t.Errorf("the winner %s closed %d times behind reply %q", r.addr, r.closed.Load(), reply)
			}
		case abandoned(r):
			lost++
			if r.closed.Load() != 0 {
				t.Error("a route that lost the race was reported closed")
			}
		}
	}
	if won != 1 || lost != 1 {
		t.Errorf("%d winners and %d abandoned of two live routes", won, lost)
	}
	// the route nothing listens at failed on its own account, or was cut short by the winner
	if !routes[0].failed() || routes[0].closed.Load() != 0 {
		t.Error("a refused route was not reported failed")
	}
	if _, results, _ := counts.snapshot(); results[ResultProxied] != 1 {
		t.Errorf("results = %v", results)
	}
}

func TestServerRaceWithNoWinner(t *testing.T) {
	counts := &countingObserver{}
	up := &spreading{addrs: []string{refusedAddr(t), refusedAddr(t)}}
	_, addr := startServer(t, ProtocolTCP, &Config{
		Observer: counts, Table: tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(time.Second)},
	})
	expectClosed(t, dialTCP(t, addr))
	for _, r := range up.seen() {
		if r.dialed.Load() != 1 || !r.failed() || abandoned(r) {
			t.Errorf("%s: dialed %d times, failed %v", r.addr, r.dialed.Load(), r.failed())
		}
	}
	// an upstream with nothing to race refuses the flow
	up.empty()
	expectClosed(t, dialTCP(t, addr))
	waitFor(t, func() bool {
		_, results, _ := counts.snapshot()
		return results[ResultDialFailed] == 1 && results[ResultNoUpstream] == 1
	})
}

// udpSink records the datagrams it receives and answers each one
func udpSink(t *testing.T, prefix string) (string, func() []string) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	var mu sync.Mutex
	var got []string
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			got = append(got, string(buf[:n]))
			mu.Unlock()
			_, _ = pc.WriteTo(append([]byte(prefix), buf[:n]...), addr)
		}
	}()
	return pc.LocalAddr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestPacketServerMirrorsDatagrams(t *testing.T) {
	primary, primaryGot := udpSink(t, "primary:")
	second, secondGot := udpSink(t, "second:")
	third, thirdGot := udpSink(t, "third:")
	const unreachable = "192.0.2.1:9"
	up := &spreading{addrs: []string{primary, second, unreachable, third}}
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{IdleTimeout: timeconv.Duration(200 * time.Millisecond)},
	}, func(ctx context.Context, a string) (net.Conn, error) {
		if a == unreachable {
			return nil, errors.New("no route to host")
		}
		var d net.Dialer
		return d.DialContext(ctx, "udp", a)
	})
	client := udpClient(t, addr)
	for _, msg := range []string{"one", "two", "three"} {
		if got := datagram(t, client, msg); got != "primary:"+msg {
			t.Fatalf("reply = %q", got)
		}
	}
	// every mirror got every datagram, in order, and nothing a mirror answered came back
	for name, got := range map[string]func() []string{"primary": primaryGot, "second": secondGot, "third": thirdGot} {
		waitFor(t, func() bool { return len(got()) == 3 })
		if strings.Join(got(), ",") != "one,two,three" {
			t.Errorf("%s received %v", name, got())
		}
	}
	_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if n, err := client.Read(make([]byte, 64)); err == nil {
		t.Errorf("a mirror's reply reached the client: %d bytes", n)
	}
	waitFor(t, func() bool { return srv.ActiveSessions() == 0 })
	routes := up.seen()
	if len(routes) != 4 {
		t.Fatalf("%d routes", len(routes))
	}
	for i, r := range routes {
		wantFailed := r.addr == unreachable
		wantClosed := int32(1)
		if wantFailed {
			wantClosed = 0
		}
		if r.dialed.Load() != 1 || r.failed() != wantFailed || r.closed.Load() != wantClosed {
			t.Errorf("route %d (%s): dialed %d, failed %v, closed %d", i, r.addr, r.dialed.Load(), r.failed(), r.closed.Load())
		}
	}
	if routes[0].firstByte.Load() != 1 || routes[1].firstByte.Load() != 0 {
		t.Error("only the answering route has a first reply to report")
	}
}

func TestPacketServerBoundsItsMirrors(t *testing.T) {
	primary, _ := udpSink(t, "primary:")
	up := &spreading{addrs: []string{primary}}
	for range MaxUDPMirrors + 2 {
		sink, _ := udpSink(t, "mirror:")
		up.addrs = append(up.addrs, sink)
	}
	srv, addr, _ := startPacketServer(t, &Config{Table: tableOf(t, map[string]Upstream{"": up})})
	if got := datagram(t, udpClient(t, addr), "x"); got != "primary:x" {
		t.Fatalf("reply = %q", got)
	}
	var open, left int
	for _, r := range up.seen()[1:] {
		if abandoned(r) {
			left++
		} else if !r.failed() {
			open++
		}
	}
	if open != MaxUDPMirrors || left != 2 {
		t.Errorf("%d mirrors open and %d abandoned", open, left)
	}
	_ = srv.Close()
	for i, r := range up.seen() {
		if !r.failed() && r.closed.Load() != 1 {
			t.Errorf("route %d was closed %d times", i, r.closed.Load())
		}
	}
}

// a flow closed while it was still dialing closes the mirrors it dialed, too
func TestCloseDoesNotPublishLateMirrors(t *testing.T) {
	primary, _ := udpSink(t, "primary:")
	mirror, _ := udpSink(t, "mirror:")
	up := &spreading{addrs: []string{primary, mirror}}
	dialing := make(chan struct{}, 2)
	srv, addr, _ := startPacketServerWith(t, &Config{
		Table:   tableOf(t, map[string]Upstream{"": up}),
		Options: &options.Options{ConnectTimeout: timeconv.Duration(10 * time.Second)},
	}, func(ctx context.Context, a string) (net.Conn, error) {
		if a == primary {
			dialing <- struct{}{}
			<-ctx.Done()
		}
		// the dial ignores the cancellation and hands back a usable socket anyway
		return net.Dial("udp", a)
	})
	if _, err := udpClient(t, addr).Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	<-dialing
	_ = srv.Close()
	routes := up.seen()
	if len(routes) != 2 {
		t.Fatalf("%d routes", len(routes))
	}
	for i, r := range routes {
		if r.failed() || r.closed.Load() != 1 {
			t.Errorf("route %d: failed %v, closed %d", i, r.failed(), r.closed.Load())
		}
	}
}
