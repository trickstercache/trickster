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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"net/netip"
)

func opts(t *testing.T, name string, mutate func(*options.Options)) *options.Options {
	t.Helper()
	o := &options.Options{Name: name, Limit: 1, Window: timeconv.Duration(time.Minute)}
	if mutate != nil {
		mutate(o)
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	return o
}

func buckets(name string) int {
	var n int
	ratelimit.Walk(func(got string, keys int) {
		if got == name {
			n = keys
		}
	})
	return n
}

func TestStageAndVerdict(t *testing.T) {
	if New(nil, l4.ProtocolTCP, false) != nil {
		t.Fatal("nil limiter")
	}
	peer := New(opts(t, "st-peer", nil), l4.ProtocolTCP, false)
	if _, ok := peer.(l4.Holder); ok || peer.Datagrams() {
		t.Fatal("a connection limiter holds or judges datagrams")
	}
	if peer.Peer(l4.Flow{}) != l4.Allow || peer.Flow(l4.Flow{}) != l4.Allow {
		t.Fatal("peer stage judged the flow stage")
	}
	if peer.Peer(l4.Flow{}) != l4.Reject || buckets("st-peer") != 1 {
		t.Fatal("second connection was not refused once")
	}

	sni := New(opts(t, "st-sni", func(o *options.Options) {
		o.Keys = []string{"sni"}
		o.Limit = 2
	}), l4.ProtocolTLS, false)
	if sni.Peer(l4.Flow{ServerName: "a.example"}) != l4.Allow || buckets("st-sni") != 0 {
		t.Fatal("sni was charged before the flow stage")
	}
	if sni.Flow(l4.Flow{ServerName: "a.example"}) != l4.Allow || sni.Flow(l4.Flow{ServerName: "b.example"}) != l4.Allow {
		t.Fatal("distinct names shared a bucket")
	}
	if sni.Flow(l4.Flow{}) != l4.Allow || buckets("st-sni") != 2 {
		t.Fatal("a missing server name was not exempt")
	}

	closed := New(opts(t, "st-close", func(o *options.Options) { o.Action = options.ActionClose }), l4.ProtocolTCP, false)
	closed.Peer(l4.Flow{})
	if closed.Peer(l4.Flow{}) != l4.Drop {
		t.Fatal("close did not drop")
	}
	counted := New(opts(t, "st-count", func(o *options.Options) { o.Action = options.ActionCount }), l4.ProtocolTCP, false)
	if counted.Peer(l4.Flow{}) != l4.Allow || counted.Peer(l4.Flow{}) != l4.Allow {
		t.Fatal("count mode refused")
	}
	if got := testutil.ToFloat64(metrics.RateLimitDecisions.WithLabelValues("st-count", planeStream, "counted")); got < 1 {
		t.Fatalf("counted %v", got)
	}

	grams := New(opts(t, "st-grams", func(o *options.Options) { o.Unit = options.UnitDatagrams }), l4.ProtocolUDP, false)
	if !grams.Datagrams() || grams.Peer(l4.Flow{}) != l4.Allow || buckets("st-grams") != 0 {
		t.Fatal("datagrams were charged at the peer stage")
	}
	if grams.Datagram(l4.Flow{}, 8) != l4.Allow || grams.Datagram(l4.Flow{}, 8) != l4.Reject {
		t.Fatal("datagram verdict")
	}
}

func TestSessionHoldAndFullTable(t *testing.T) {
	a := New(opts(t, "st-sess", func(o *options.Options) { o.Unit = options.UnitSessions }), l4.ProtocolUDP, false)
	h, ok := a.(l4.Holder)
	if !ok || a.Datagrams() {
		t.Fatal("sessions")
	}
	if a.Peer(l4.Flow{}) != l4.Allow {
		t.Fatal("first session")
	}
	if a.Peer(l4.Flow{}) != l4.Reject {
		t.Fatal("second session")
	}
	if got := h.Hold(l4.Flow{}); got <= 0 || got > 2*time.Minute {
		t.Fatalf("hold %v", got)
	}
	if buckets("st-sess") != 1 {
		t.Fatal("the denial was charged")
	}

	full := New(opts(t, "st-full", func(o *options.Options) {
		o.Unit = options.UnitSessions
		o.Keys = []string{"client_ip"}
		o.MaxKeys = 1
		o.MaxKeysAction = options.OnFullReject
		o.Limit = 5
	}), l4.ProtocolUDP, false)
	one := l4.Flow{Client: netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), 1)}
	two := l4.Flow{Client: netip.AddrPortFrom(netip.MustParseAddr("192.0.2.2"), 1)}
	if full.Peer(one) != l4.Allow || full.Peer(two) != l4.Reject {
		t.Fatal("full table")
	}
	if got := full.(l4.Holder).Hold(two); got != time.Minute || buckets("st-full") != 1 {
		t.Fatalf("full hold %v buckets %d", got, buckets("st-full"))
	}
}

func TestFlowACLDefersTheCharge(t *testing.T) {
	limited := New(opts(t, "st-defer", func(o *options.Options) { o.Limit = 2 }), l4.ProtocolTCP, true)
	acl := denyFlow{}
	c := l4.Compose(acl, limited)
	if c.Peer(l4.Flow{}) != l4.Allow || buckets("st-defer") != 0 {
		t.Fatal("charged at the peer stage ahead of the access list")
	}
	if c.Flow(l4.Flow{}) != l4.Reject || buckets("st-defer") != 0 {
		t.Fatal("the access list denial consumed a connection")
	}
	alone := New(opts(t, "st-once", func(o *options.Options) { o.Limit = 2 }), l4.ProtocolTCP, true)
	alone.Peer(l4.Flow{})
	if alone.Flow(l4.Flow{}) != l4.Allow || alone.Flow(l4.Flow{}) != l4.Allow {
		t.Fatal("one connection was charged at both stages")
	}
	if alone.Flow(l4.Flow{}) != l4.Reject {
		t.Fatal("third connection")
	}
}

type denyFlow struct{}

func (denyFlow) Peer(l4.Flow) l4.Verdict          { return l4.Allow }
func (denyFlow) Flow(l4.Flow) l4.Verdict          { return l4.Reject }
func (denyFlow) Datagram(l4.Flow, int) l4.Verdict { return l4.Allow }
func (denyFlow) Datagrams() bool                  { return false }
