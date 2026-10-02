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
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

const stickyClient = "198.51.100.1:5000"

// stickyALB builds a load balancer named for the test that keeps its flows' sessions as o says
func stickyALB(t *testing.T, mechanism string, o *so.Options, adjust func(*ao.Options),
	members ...spec,
) backends.Backend {
	t.Helper()
	name := t.Name()
	t.Cleanup(func() { sticky.ForgetTablesExcept(func(n string, _ *sticky.Table) bool { return n != name }) })
	return newALBWith(t, name, mechanism, func(ao *ao.Options) {
		ao.Sticky = o
		if adjust != nil {
			adjust(ao)
		}
	}, members...)
}

func withRetries(n int) func(*ao.Options) {
	return func(o *ao.Options) { o.Stream = &ao.StreamOptions{ConnectRetries: n} }
}

func stickyCount(t *testing.T, result string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.ALBStickyResults.WithLabelValues(t.Name(), result))
}

func pins(b backends.Backend) int {
	return b.(*alb.Client).StickyFlows().Table().Len()
}

// connect relays one flow: it is picked, dialed and closed; it returns the member's address, or
// "" when the flow was refused
func connect(t *testing.T, u l4.Upstream, f l4.Flow) string {
	t.Helper()
	r, ok := u.Pick(f)
	if !ok {
		return ""
	}
	r.Dialed(time.Millisecond, nil)
	r.Closed(nil)
	return r.Addr()
}

func fromPort(protocol string, port int) l4.Flow {
	return clientFlow(protocol, "198.51.100.1:"+strconv.Itoa(port), "")
}

// a client's connections keep to the member its first one reached, whatever the rotation, and
// stay there when the pool changes around it
func TestStickyFlowsReturnToTheirMember(t *testing.T) {
	m := origins(t, 4)
	b := stickyALB(t, "rr", &so.Options{}, nil, up(m[0], 1), up(m[1], 1), up(m[2], 1))
	u := FromBackend(b)
	first := connect(t, u, fromPort(l4.ProtocolTCP, 5000))
	for port := range 6 {
		if got := connect(t, u, fromPort(l4.ProtocolTCP, 6000+port)); got != first {
			t.Fatalf("a reconnect reached %s, not %s", got, first)
		}
	}
	others := make(map[string]bool)
	for i := range 6 {
		others[connect(t, u, clientFlow(l4.ProtocolTCP, "203.0.113."+strconv.Itoa(i)+":5000", ""))] = true
	}
	if len(others) < 3 {
		t.Errorf("six other clients reached only %v", others)
	}
	if got := stickyCount(t, sticky.ResultHit); got != 6 {
		t.Errorf("hits = %v", got)
	}
	// a reload that adds a member ahead of the rest keeps the client where it was
	moved := FromBackend(stickyALB(t, "rr", &so.Options{}, nil, up(m[3], 1), up(m[0], 1), up(m[1], 1), up(m[2], 1)))
	for port := range 4 {
		if got := connect(t, moved, fromPort(l4.ProtocolTCP, 7000+port)); got != first {
			t.Fatalf("after the pool changed, a reconnect reached %s, not %s", got, first)
		}
	}
}

// only a member that was reached is pinned: a tcp connect, or a udp reply or a clean end
func TestStickyFlowsPinOnlyAReachedMember(t *testing.T) {
	m := origins(t, 3)
	b := stickyALB(t, "rr", &so.Options{}, nil, up(m[0], 1), up(m[1], 1), up(m[2], 1))
	u := FromBackend(b)
	r, _ := u.Pick(fromPort(l4.ProtocolTCP, 5000))
	r.Dialed(time.Millisecond, syscall.ECONNREFUSED)
	if pins(b) != 0 {
		t.Fatal("a member that refused the connection was pinned")
	}
	r, _ = u.Pick(fromPort(l4.ProtocolTCP, 5001))
	r.Dialed(time.Millisecond, nil)
	if pins(b) != 1 {
		t.Fatal("a connected member was not pinned")
	}
	r.FirstByte()
	r.Closed(nil)
	if got := stickyCount(t, sticky.ResultMiss); got != 1 {
		t.Errorf("misses = %v; a flow is counted once, when it is settled", got)
	}

	// a udp socket opens whether or not a member listens: its reply is what pins it
	udp := func(client string) l4.Route {
		r, ok := u.Pick(clientFlow(l4.ProtocolUDP, client, ""))
		if !ok {
			t.Fatal("refused")
		}
		r.Dialed(time.Millisecond, nil)
		return r
	}
	answered := udp("203.0.113.1:53")
	if pins(b) != 1 {
		t.Fatal("a udp member was pinned before it answered")
	}
	answered.FirstByte()
	answered.Closed(nil)
	oneWay := udp("203.0.113.2:514")
	oneWay.Closed(nil)
	unreachable := udp("203.0.113.3:53")
	unreachable.Closed(syscall.ECONNREFUSED)
	if pins(b) != 3 {
		t.Errorf("pins = %d; want the answered and the one-way member pinned, not the unreachable one", pins(b))
	}
	// a new session from an answered client lands where its first did
	for port := range 4 {
		if got := connect(t, u, clientFlow(l4.ProtocolUDP, "203.0.113.1:"+strconv.Itoa(1000+port), "")); got != answered.Addr() {
			t.Fatalf("a new udp session reached %s, not %s", got, answered.Addr())
		}
	}
}

// a flow whose pinned member is unavailable moves and is pinned anew, or is refused
func TestStickyFlowsWhenThePinnedMemberIsUnavailable(t *testing.T) {
	for _, mode := range []string{so.OnUnavailableRepick, so.OnUnavailableReject} {
		t.Run(mode, func(t *testing.T) {
			m := origins(t, 2)
			members := []spec{up(m[0], 1), up(m[1], 1)}
			u := FromBackend(stickyALB(t, "rr", &so.Options{OnUnavailable: mode}, nil, members...))
			first := connect(t, u, fromPort(l4.ProtocolTCP, 5000))
			status := members[0].status
			if first == members[1].backend.Configuration().Host {
				status = members[1].status
			}
			status.Set(healthcheck.StatusFailing)
			got := connect(t, u, fromPort(l4.ProtocolTCP, 5001))
			if mode == so.OnUnavailableReject {
				if got != "" || stickyCount(t, sticky.ResultRejected) != 1 {
					t.Fatalf("reject mode sent the flow to %q", got)
				}
				status.Set(healthcheck.StatusPassing)
				if got = connect(t, u, fromPort(l4.ProtocolTCP, 5002)); got != first {
					t.Fatalf("a flow reached %s once its member returned, not %s", got, first)
				}
				return
			}
			if got == "" || got == first || stickyCount(t, sticky.ResultRepick) != 1 {
				t.Fatalf("repick mode sent the flow to %q, from %s", got, first)
			}
			// the session is pinned where it moved, and stays there when its old member returns
			status.Set(healthcheck.StatusPassing)
			for port := range 4 {
				if again := connect(t, u, fromPort(l4.ProtocolTCP, 6000+port)); again != got {
					t.Fatalf("a moved session reached %s, not %s", again, got)
				}
			}
		})
	}
}

// a connect that fails on the pinned member is retried elsewhere, or refused when the session
// must not move
func TestStickyFlowsOnAFailedConnect(t *testing.T) {
	for _, mode := range []string{so.OnUnavailableRepick, so.OnUnavailableReject} {
		t.Run(mode, func(t *testing.T) {
			m := origins(t, 3)
			b := stickyALB(t, "rr", &so.Options{OnUnavailable: mode}, withRetries(2), up(m[0], 1), up(m[1], 1), up(m[2], 1))
			u := FromBackend(b)
			first := connect(t, u, fromPort(l4.ProtocolTCP, 5000))
			flow := fromPort(l4.ProtocolTCP, 5001)
			r, _ := u.Pick(flow)
			if r.Addr() != first {
				t.Fatalf("the pinned flow reached %s, not %s", r.Addr(), first)
			}
			r.Dialed(time.Millisecond, syscall.ECONNREFUSED)
			next, ok := u.(l4.Retrier).Retry(flow, r)
			if mode == so.OnUnavailableReject {
				if ok || stickyCount(t, sticky.ResultRejected) != 1 {
					t.Fatal("reject mode retried the flow on another member")
				}
				return
			}
			if !ok || next.Addr() == first {
				t.Fatalf("retry = %v, %v", next, ok)
			}
			next.Dialed(time.Millisecond, nil)
			next.Closed(nil)
			if stickyCount(t, sticky.ResultRepick) != 1 {
				t.Error("the retried flow was not counted as moved")
			}
			if got := connect(t, u, fromPort(l4.ProtocolTCP, 5002)); got != next.Addr() {
				t.Errorf("the next flow reached %s, not %s where the session moved", got, next.Addr())
			}
		})
	}
}

// a failed connect to the pinned member is a refusal whenever the session must not move, even
// where no retry would be offered
func TestStickyFlowsCountAFailedConnectAsRefused(t *testing.T) {
	for _, mode := range []string{so.OnUnavailableRepick, so.OnUnavailableReject} {
		t.Run(mode, func(t *testing.T) {
			m := origins(t, 2)
			u := FromBackend(stickyALB(t, "rr", &so.Options{OnUnavailable: mode}, nil, up(m[0], 1), up(m[1], 1)))
			connect(t, u, fromPort(l4.ProtocolTCP, 5000))
			flow := fromPort(l4.ProtocolTCP, 5001)
			r, _ := u.Pick(flow)
			r.Dialed(time.Millisecond, syscall.ECONNREFUSED)
			if _, ok := u.(l4.Retrier).Retry(flow, r); ok {
				t.Fatal("a retry was offered with no connect_retries configured")
			}
			want := 0.0
			if mode == so.OnUnavailableReject {
				want = 1
			}
			if got := stickyCount(t, sticky.ResultRejected); got != want {
				t.Errorf("rejected = %v, want %v", got, want)
			}
		})
	}
}

// the load balancer the listener maps to keeps the whole path, and a retry keeps the flow in its
// pinned pool while that pool has another member to offer
func TestStickyFlowsKeepTheirPathThroughNestedPools(t *testing.T) {
	m := origins(t, 4)
	left := newALB(t, t.Name()+"-left", "rr", up(m[0], 1), up(m[1], 1))
	right := newALB(t, t.Name()+"-right", "rr", up(m[2], 1), up(m[3], 1))
	u := FromBackend(stickyALB(t, "rr", &so.Options{}, withRetries(3), up(left, 1), up(right, 1)))
	first := connect(t, u, fromPort(l4.ProtocolTCP, 5000))
	for port := range 6 {
		if got := connect(t, u, fromPort(l4.ProtocolTCP, 6000+port)); got != first {
			t.Fatalf("a reconnect reached %s, not %s", got, first)
		}
	}
	pool := map[string]string{
		"10.0.0.1:9000": "left", "10.0.0.2:9000": "left", "10.0.0.3:9000": "right",
		"10.0.0.4:9000": "right",
	}
	flow := fromPort(l4.ProtocolTCP, 7000)
	r, _ := u.Pick(flow)
	r.Dialed(time.Millisecond, syscall.ECONNREFUSED)
	next, ok := u.(l4.Retrier).Retry(flow, r)
	if !ok || next.Addr() == first || pool[next.Addr()] != pool[first] {
		t.Fatalf("a retry from %s went to %v, %v; want the other member of its pool", first, next, ok)
	}
	next.Dialed(time.Millisecond, nil)
	next.Closed(nil)
	if got := connect(t, u, fromPort(l4.ProtocolTCP, 7001)); got != next.Addr() {
		t.Errorf("after the retry, a flow reached %s, not %s", got, next.Addr())
	}
}

// a pinned pool whose members are all down moves its sessions to a pool that has one, unless
// they must not move
func TestStickyFlowsLeaveAPinnedPoolWithNothingLeft(t *testing.T) {
	for _, mode := range []string{so.OnUnavailableRepick, so.OnUnavailableReject} {
		t.Run(mode, func(t *testing.T) {
			m := origins(t, 4)
			leaves := []spec{up(m[0], 1), up(m[1], 1), up(m[2], 1), up(m[3], 1)}
			left := newALB(t, t.Name()+"-left", "rr", leaves[0], leaves[1])
			right := newALB(t, t.Name()+"-right", "rr", leaves[2], leaves[3])
			u := FromBackend(stickyALB(t, "rr", &so.Options{OnUnavailable: mode}, nil, up(left, 1), up(right, 1)))
			first := connect(t, u, fromPort(l4.ProtocolTCP, 5000))
			pinned, other := leaves[:2], map[string]bool{"10.0.0.3:9000": true, "10.0.0.4:9000": true}
			if other[first] {
				pinned, other = leaves[2:], map[string]bool{"10.0.0.1:9000": true, "10.0.0.2:9000": true}
			}
			for _, l := range pinned {
				l.status.Set(healthcheck.StatusFailing)
			}
			got := connect(t, u, fromPort(l4.ProtocolTCP, 5001))
			restore := func() {
				for _, l := range pinned {
					l.status.Set(healthcheck.StatusPassing)
				}
			}
			if mode == so.OnUnavailableReject {
				if got != "" || stickyCount(t, sticky.ResultRejected) != 1 {
					t.Fatalf("reject mode sent the flow to %q", got)
				}
				restore()
				if got = connect(t, u, fromPort(l4.ProtocolTCP, 5002)); got != first {
					t.Fatalf("a flow reached %s once its pool returned, not %s", got, first)
				}
				return
			}
			if !other[got] || stickyCount(t, sticky.ResultRepick) != 1 {
				t.Fatalf("repick mode sent the flow to %q, from %s", got, first)
			}
			restore()
			for port := range 4 {
				if again := connect(t, u, fromPort(l4.ProtocolTCP, 6000+port)); again != got {
					t.Fatalf("a moved session reached %s, not %s", again, got)
				}
			}
		})
	}
}

// a table key read from the flow keeps together the flows that share it, whichever client sends them
func TestStickyFlowsByServerNameAndTLV(t *testing.T) {
	m := origins(t, 4)
	members := []spec{up(m[0], 1), up(m[1], 1), up(m[2], 1), up(m[3], 1)}
	byName := FromBackend(stickyALB(t, "rr", &so.Options{Table: so.TableOptions{Key: "sni"}}, nil, members...))
	byTLV := FromBackend(newALBWith(t, t.Name()+"-tlv", "rr", func(o *ao.Options) {
		o.Sticky = &so.Options{Table: so.TableOptions{Key: "proxy_tlv:0xEA"}}
	}, members...))
	t.Cleanup(func() {
		sticky.ForgetTablesExcept(func(n string, _ *sticky.Table) bool { return n != t.Name()+"-tlv" })
	})
	reached := make(map[string]bool)
	for i := range 4 {
		host := "shop" + strconv.Itoa(i) + ".example.com"
		first := connect(t, byName, clientFlow(l4.ProtocolTLS, "198.51.100.1:1000", host))
		reached[first] = true
		for c := range 4 {
			f := clientFlow(l4.ProtocolTLS, "203.0.113."+strconv.Itoa(c)+":2000", host)
			if got := connect(t, byName, f); got != first {
				t.Fatalf("%s reached %s and %s", host, first, got)
			}
		}
		tlv := func(client string) l4.Flow {
			f := clientFlow(l4.ProtocolTCP, client, "")
			f.Proxy = tlvs{0xEA: "vpce-" + strconv.Itoa(i)}
			return f
		}
		first = connect(t, byTLV, tlv("198.51.100.1:1000"))
		if got := connect(t, byTLV, tlv("203.0.113.9:2000")); got != first {
			t.Fatalf("endpoint %d reached %s and %s", i, first, got)
		}
	}
	if len(reached) < 3 {
		t.Errorf("four server names reached only %v", reached)
	}
}

// a load balancer that keeps no sessions, or keeps them only in tokens, carries no table
func TestFlowsWithoutATable(t *testing.T) {
	m := origins(t, 1)
	for name, o := range map[string]*so.Options{"none": nil, "cookie": {Mode: so.ModeCookie}} {
		b := newALBWith(t, t.Name()+name, "rr", func(ao *ao.Options) { ao.Sticky = o }, up(m[0], 1))
		if b.(*alb.Client).StickyFlows() != nil {
			t.Errorf("%s: a load balancer keeps flows in a table", name)
		}
		if connect(t, FromBackend(b), fromPort(l4.ProtocolTCP, 5000)) == "" {
			t.Errorf("%s: refused", name)
		}
	}
}
