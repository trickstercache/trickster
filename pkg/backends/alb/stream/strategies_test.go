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
package stream

import (
	"errors"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

var errRefused = errors.New("connection refused")

func origins(t testing.TB, n int) []backends.Backend {
	t.Helper()
	out := make([]backends.Backend, n)
	for i := range out {
		out[i] = origin(t, "m"+strconv.Itoa(i), "10.0.0."+strconv.Itoa(i+1)+":9000")
	}
	return out
}

func clientFlow(protocol, client, serverName string) l4.Flow {
	return l4.Flow{Listener: "test", Protocol: protocol, Client: netip.MustParseAddrPort(client), ServerName: serverName}
}

func statsOf(b backends.Backend) map[string]*lb.Stats {
	out := make(map[string]*lb.Stats)
	for _, t := range b.(*alb.Client).Pool().ConfiguredTargets() {
		out[t.Addr()] = t.Member().Stats()
	}
	return out
}

// every strategy balances connections as it balances requests: only live members, all of
// them reachable, nothing left in flight, and a refusal once none is live
func TestEveryStrategyServesAStreamListener(t *testing.T) {
	for _, mech := range []string{"rr", "p2c", "lc", "lt", "hrw"} {
		t.Run(mech, func(t *testing.T) {
			m := origins(t, 4)
			members := []spec{up(m[0], 1), up(m[1], 2), up(m[2], 1), down(m[3], 5)}
			pool := newALB(t, "alb", mech, members...)
			u := FromBackend(pool)
			seen := make(map[string]int)
			var held []l4.Route
			for i := range 400 {
				r, ok := u.Pick(clientFlow(l4.ProtocolTCP, "198.51.100."+strconv.Itoa(i%250)+":"+strconv.Itoa(1024+i), ""))
				if !ok {
					t.Fatal("refused although members are live")
				}
				seen[r.Addr()]++
				r.Dialed(time.Millisecond, nil)
				held = append(held, r)
				if len(held) > 16 {
					held[0].Closed(nil)
					held = held[1:]
				}
			}
			for _, r := range held {
				r.Closed(nil)
			}
			if seen["10.0.0.4:9000"] != 0 {
				t.Errorf("%d connections went to a member that is down", seen["10.0.0.4:9000"])
			}
			for _, addr := range []string{"10.0.0.1:9000", "10.0.0.2:9000", "10.0.0.3:9000"} {
				if seen[addr] == 0 {
					t.Errorf("%s took none of 400 connections: %v", addr, seen)
				}
			}
			for addr, st := range statsOf(pool) {
				if st.Inflight() != 0 {
					t.Errorf("%s holds %d in flight after every connection closed", addr, st.Inflight())
				}
			}
			for _, member := range members[:3] {
				member.status.Set(-1)
			}
			if _, ok := u.Pick(clientFlow(l4.ProtocolTCP, "198.51.100.1:5000", "")); ok {
				t.Error("dialed although no member is live")
			}
		})
	}
}

// a client keeps its member across connections and across its ephemeral ports, and an IPv6
// client across the privacy addresses of its /64
func TestHRWKeysOnTheClientAddress(t *testing.T) {
	m := origins(t, 5)
	u := FromBackend(newALB(t, "alb", "hrw", up(m[0], 1), up(m[1], 1), up(m[2], 1), up(m[3], 1), up(m[4], 1)))
	owner := func(f l4.Flow) string {
		r, ok := u.Pick(f)
		if !ok {
			t.Fatal("refused")
		}
		r.Dialed(0, l4.ErrAbandoned)
		return r.Addr()
	}
	reached := make(map[string]bool)
	for i := range 40 {
		ip := "203.0.113." + strconv.Itoa(i+1)
		first := owner(clientFlow(l4.ProtocolTCP, ip+":40000", ""))
		reached[first] = true
		for _, port := range []string{"40001", "51234"} {
			if got := owner(clientFlow(l4.ProtocolUDP, ip+":"+port, "")); got != first {
				t.Fatalf("%s moved from %s to %s with its port", ip, first, got)
			}
		}
	}
	if len(reached) < 4 {
		t.Errorf("40 clients reached only %d of 5 members", len(reached))
	}
	a := owner(clientFlow(l4.ProtocolTCP, "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443", ""))
	if b := owner(clientFlow(l4.ProtocolTCP, "[2001:db8:1:2:1111:2222:3333:4444]:443", "")); a != b {
		t.Errorf("two addresses of one /64 reached %s and %s", a, b)
	}
	// a flow with no usable client address has no affinity, and is still served
	if _, ok := u.Pick(l4.Flow{Protocol: l4.ProtocolTCP}); !ok {
		t.Error("a flow with no client address was refused")
	}
}

// keyed on the server name, every client of one name shares a member, whoever they are
func TestHRWKeysOnTheServerName(t *testing.T) {
	m := origins(t, 5)
	sni := func(o *ao.Options) { o.HRW.Key = "sni" }
	u := FromBackend(newALBWith(t, "alb", "hrw", sni, up(m[0], 1), up(m[1], 1), up(m[2], 1), up(m[3], 1), up(m[4], 1)))
	owner := func(client, name string) string {
		r, ok := u.Pick(clientFlow(l4.ProtocolTLS, client, name))
		if !ok {
			t.Fatal("refused")
		}
		r.Dialed(0, l4.ErrAbandoned)
		return r.Addr()
	}
	reached := make(map[string]bool)
	for i := range 30 {
		name := "tenant" + strconv.Itoa(i) + ".example.com"
		first := owner("198.51.100.1:1000", name)
		reached[first] = true
		if got := owner("203.0.113.77:2000", "Tenant"+strconv.Itoa(i)+".Example.COM"); got != first {
			t.Fatalf("%s reached %s from one client and %s from another", name, first, got)
		}
	}
	if len(reached) < 4 {
		t.Errorf("30 server names reached only %d of 5 members", len(reached))
	}
	// a client that offers no name has nothing to be kept by
	spread := make(map[string]bool)
	for range 60 {
		spread[owner("198.51.100.1:1000", "")] = true
	}
	if len(spread) < 3 {
		t.Errorf("60 nameless flows reached only %d members", len(spread))
	}
}

// each load balancer a flow passes through reads the key its own way: round robin across the
// inner pools, as a weighted rule compiles to, and affinity within the one chosen
func TestNestedLoadBalancersKeyForThemselves(t *testing.T) {
	m := origins(t, 6)
	inner := func(name string, members ...spec) backends.Backend { return newALB(t, name, "hrw", members...) }
	outer := newALB(t, "outer", "rr",
		up(inner("inner-a", up(m[0], 1), up(m[1], 1), up(m[2], 1)), 1),
		up(inner("inner-b", up(m[3], 1), up(m[4], 1), up(m[5], 1)), 1))
	u := FromBackend(outer)
	first := make(map[bool]string)
	for i := range 12 {
		r, ok := u.Pick(clientFlow(l4.ProtocolTCP, "203.0.113.9:"+strconv.Itoa(30000+i), ""))
		if !ok {
			t.Fatal("refused")
		}
		r.Dialed(0, l4.ErrAbandoned)
		inA := r.Addr() == "10.0.0.1:9000" || r.Addr() == "10.0.0.2:9000" || r.Addr() == "10.0.0.3:9000"
		if prev, seen := first[inA]; seen && prev != r.Addr() {
			t.Fatalf("one client reached %s and %s within one inner pool", prev, r.Addr())
		}
		first[inA] = r.Addr()
	}
	if len(first) != 2 {
		t.Errorf("round robin across the inner pools reached %d of them", len(first))
	}
}

// what is timed depends on the protocol and on lt.signal: the connect, or the member's
// first byte, and never both
func TestLatencySignals(t *testing.T) {
	for _, test := range []struct {
		name, protocol, signal string
		wantConnect            bool
	}{
		{"tcp default is the connect", l4.ProtocolTCP, "", true},
		{"tls default is the connect", l4.ProtocolTLS, "", true},
		{"tcp first_byte", l4.ProtocolTCP, ao.LTSignalFirstByte, false},
		{"udp default is the first reply", l4.ProtocolUDP, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			// a name of its own: an ALB that keeps stats inherits them from its predecessor
			pool := newALBWith(t, "signal-"+test.protocol+test.signal, "lt", func(o *ao.Options) { o.LT.Signal = test.signal },
				up(origin(t, "a", "10.0.0.1:9000"), 1))
			st := statsOf(pool)["10.0.0.1:9000"]
			r, _ := FromBackend(pool).Pick(clientFlow(test.protocol, "198.51.100.1:5000", ""))
			r.Dialed(40*time.Millisecond, nil)
			if got := st.Latency() == 40*time.Millisecond; got != test.wantConnect {
				t.Fatalf("after the connect the average is %v", st.Latency())
			}
			time.Sleep(15 * time.Millisecond)
			r.FirstByte()
			if test.wantConnect {
				if st.Latency() != 40*time.Millisecond {
					t.Errorf("the first byte was sampled as well as the connect: %v", st.Latency())
				}
			} else if st.Latency() < 15*time.Millisecond || st.Latency() >= 40*time.Millisecond {
				t.Errorf("first-byte sample = %v, want the time since the pick", st.Latency())
			}
			r.Closed(nil)
		})
	}
}

// a failed connect is retried on another member, as many times as configured, and never onto
// the member that just failed
func TestConnectRetries(t *testing.T) {
	m := origins(t, 3)
	retries := func(n int) func(*ao.Options) {
		return func(o *ao.Options) { o.Stream = &ao.StreamOptions{ConnectRetries: n} }
	}
	u := FromBackend(newALBWith(t, "alb", "rr", retries(2), up(m[0], 1), up(m[1], 1), up(m[2], 1)))
	retrier, ok := u.(l4.Retrier)
	if !ok {
		t.Fatal("a pooled upstream cannot retry")
	}
	flow := clientFlow(l4.ProtocolTCP, "198.51.100.1:5000", "")
	first, _ := u.Pick(flow)
	first.Dialed(time.Millisecond, errRefused)
	second, ok := retrier.Retry(flow, first)
	if !ok || second.Addr() == first.Addr() {
		t.Fatalf("first retry = %v, %v", second, ok)
	}
	second.Dialed(time.Millisecond, errRefused)
	third, ok := retrier.Retry(flow, second)
	if !ok || third.Addr() == second.Addr() {
		t.Fatalf("second retry = %v, %v", third, ok)
	}
	third.Dialed(time.Millisecond, errRefused)
	if _, ok := retrier.Retry(flow, third); ok {
		t.Error("a third retry was offered with connect_retries: 2")
	}
	// the default offers none, and a route that is not the adapter's is not retried
	none := FromBackend(newALB(t, "none", "rr", up(m[0], 1), up(m[1], 1))).(l4.Retrier)
	r, _ := none.(l4.Upstream).Pick(flow)
	r.Dialed(time.Millisecond, errRefused)
	if _, ok := none.Retry(flow, r); ok {
		t.Error("a retry was offered with no connect_retries configured")
	}
	static, _ := l4.Static("10.9.9.9:1").Pick(flow)
	if _, ok := retrier.Retry(flow, static); ok {
		t.Error("retried a route the adapter never issued")
	}
	// a retry that lands on a member that must refuse its share ends there
	gone := FromBackend(newALBWith(t, "gone", "rr", retries(3), up(m[0], 1),
		up(origin(t, "gone", "unresolved.kgw.invalid:1"), 1)))
	for range 4 {
		r, ok := gone.Pick(flow)
		if !ok {
			continue
		}
		r.Dialed(time.Millisecond, errRefused)
		if next, ok := gone.(l4.Retrier).Retry(flow, r); ok {
			t.Fatalf("retried onto %s, past a member that refuses its share", next.Addr())
		}
	}
}

// connects that keep failing eject the member, and what reaches the others afterwards is
// all of the traffic
func TestPassiveEjectionThroughTheAdapter(t *testing.T) {
	m := origins(t, 3)
	passive := func(o *ao.Options) {
		o.Stream = &ao.StreamOptions{PassiveHealth: &ao.PassiveHealthOptions{Failures: 2, Eject: timeconv.Duration(time.Hour)}}
	}
	pool := newALBWith(t, "ejecting-alb", "rr", passive, up(m[0], 1), up(m[1], 1), up(m[2], 1))
	u := FromBackend(pool)
	before := testutil.ToFloat64(metrics.ALBMemberEjections.WithLabelValues("ejecting-alb", "m1"))
	flow := clientFlow(l4.ProtocolTCP, "198.51.100.1:5000", "")
	for range 12 {
		r, ok := u.Pick(flow)
		if !ok {
			t.Fatal("refused")
		}
		if r.Addr() == "10.0.0.2:9000" {
			r.Dialed(time.Millisecond, errRefused)
			continue
		}
		r.Dialed(time.Millisecond, nil)
		r.Closed(nil)
	}
	if got := testutil.ToFloat64(metrics.ALBMemberEjections.WithLabelValues("ejecting-alb", "m1")) - before; got != 1 {
		t.Fatalf("ejections metered = %v", got)
	}
	for range 20 {
		r, _ := u.Pick(flow)
		if r.Addr() == "10.0.0.2:9000" {
			t.Fatal("an ejected member was dialed")
		}
		r.Dialed(time.Millisecond, nil)
		r.Closed(nil)
	}
	// a udp member that answers with a port-unreachable counts the same way
	udp := newALBWith(t, "udp-alb", "rr", passive, up(m[0], 1), up(m[1], 1))
	uu := FromBackend(udp)
	for range 8 {
		r, _ := uu.Pick(clientFlow(l4.ProtocolUDP, "198.51.100.1:5000", ""))
		r.Dialed(0, nil)
		if r.Addr() == "10.0.0.1:9000" {
			r.Closed(errRefused)
			continue
		}
		r.Closed(nil)
	}
	if !statsOf(udp)["10.0.0.1:9000"].Ejected(time.Now()) {
		t.Error("a udp member that kept refusing datagrams was not ejected")
	}
}

func TestMemberMetrics(t *testing.T) {
	pool := newALB(t, "metered", "rr", up(origin(t, "metered-member", "10.0.0.1:9000"), 1))
	u := FromBackend(pool)
	flow := l4.Flow{Listener: "metered-listener", Protocol: l4.ProtocolTCP}
	labels := []string{"metered-listener", l4.ProtocolTCP, "metered-member"}
	active := metrics.ProxyStreamMemberActiveConnections.WithLabelValues(labels...)
	count := func(result string) float64 {
		return testutil.ToFloat64(metrics.ProxyStreamMemberConnections.WithLabelValues(append(labels, result)...))
	}
	r, _ := u.Pick(flow)
	r.Dialed(5*time.Millisecond, nil)
	if testutil.ToFloat64(active) != 1 {
		t.Errorf("active = %v", testutil.ToFloat64(active))
	}
	r.Closed(nil)
	r, _ = u.Pick(flow)
	r.Dialed(time.Millisecond, errRefused)
	r, _ = u.Pick(flow)
	r.Dialed(0, nil)
	r.Closed(errRefused)
	r, _ = u.Pick(flow)
	r.Dialed(0, l4.ErrAbandoned)
	if testutil.ToFloat64(active) != 0 {
		t.Errorf("active after every route ended = %v", testutil.ToFloat64(active))
	}
	for result, want := range map[string]float64{ResultProxied: 1, ResultDialFailed: 1, ResultUnreachable: 1} {
		if got := count(result); got != want {
			t.Errorf("%s = %v, want %v", result, got, want)
		}
	}
	if n := testutil.CollectAndCount(metrics.ProxyStreamMemberConnectDuration); n == 0 {
		t.Error("no connect duration was observed")
	}
	// a discovered member that leaves takes its series with it
	metrics.DeleteBackendSeries("metered-member")
	if got := count(ResultProxied); got != 0 {
		t.Errorf("series survived the member: %v", got)
	}
}

// a level with no load balancer options of its own takes each protocol's defaults
func TestDefaultsWithoutOptions(t *testing.T) {
	if optionsOf(lb.NewMember(lb.MemberOptions{Name: "stray", Value: "not a target"})) != nil {
		t.Error("options were found for a member that is not a pool target")
	}
	if !timesConnect(nil, l4.ProtocolTCP) || !timesConnect(nil, l4.ProtocolTLS) || timesConnect(nil, l4.ProtocolUDP) {
		t.Error("the default signal is the connect on tcp and tls, and the first reply on udp")
	}
	a := key(nil, clientFlow(l4.ProtocolTCP, "[2001:db8:1:2::1]:1", ""))
	b := key(nil, clientFlow(l4.ProtocolTCP, "[2001:db8:1:2::2]:2", ""))
	if !a.HasKey || a != b {
		t.Error("without options a client is not keyed on its /64")
	}
}

// series are resolved once per member, and the cache cannot grow without bound as discovered
// members come and go
func TestMemberSeriesAreCached(t *testing.T) {
	u := &upstream{}
	f := l4.Flow{Listener: "cached-listener", Protocol: l4.ProtocolTCP}
	m := lb.NewMember(lb.MemberOptions{Name: "cached-member"})
	first := u.seriesFor(m, f, "cached-member")
	if u.seriesFor(m, f, "cached-member") != first {
		t.Error("a member's series were resolved twice")
	}
	if allocs := testing.AllocsPerRun(100, func() { _ = u.seriesFor(m, f, "cached-member") }); allocs != 0 {
		t.Errorf("a cached lookup allocates %v", allocs)
	}
	for range maxCachedSeries + 10 {
		u.seriesFor(lb.NewMember(lb.MemberOptions{Name: "cached-member"}), f, "cached-member")
	}
	if got := u.seriesOf.Load(); got > maxCachedSeries {
		t.Errorf("the cache holds %d members' series", got)
	}
	if u.seriesFor(m, f, "cached-member") == nil {
		t.Error("a member dropped from the cache could not be resolved again")
	}
}
