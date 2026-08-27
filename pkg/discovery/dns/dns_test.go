/*
 * Copyright 2018 The Trickster Authors
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

package dns

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// recordStore is a mutable in-process DNS zone for tests
type recordStore struct {
	mtx  sync.Mutex
	rrs  map[uint16][]dns.RR // by qtype
	fail bool                // respond SERVFAIL when set
}

func (r *recordStore) setFail(fail bool) {
	r.mtx.Lock()
	r.fail = fail
	r.mtx.Unlock()
}

func (r *recordStore) set(qtype uint16, records ...string) {
	rrs := make([]dns.RR, len(records))
	for i, s := range records {
		rr, err := dns.NewRR(s)
		if err != nil {
			panic(err)
		}
		rrs[i] = rr
	}
	r.mtx.Lock()
	if r.rrs == nil {
		r.rrs = make(map[uint16][]dns.RR)
	}
	r.rrs[qtype] = rrs
	r.mtx.Unlock()
}

func (r *recordStore) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	r.mtx.Lock()
	if r.fail {
		m.Rcode = dns.RcodeServerFailure
	} else if len(req.Question) > 0 {
		m.Answer = append(m.Answer, r.rrs[req.Question[0].Qtype]...)
	}
	r.mtx.Unlock()
	_ = w.WriteMsg(m)
}

// startTestDNS runs an in-process DNS server and returns its address
func startTestDNS(t *testing.T, store *recordStore) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &dns.Server{PacketConn: pc, Handler: store}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

type snapCollector struct {
	ch chan discovery.Snapshot
}

func newSnapCollector() *snapCollector {
	return &snapCollector{ch: make(chan discovery.Snapshot, 16)}
}

func (c *snapCollector) handle(s discovery.Snapshot) { c.ch <- s }

func (c *snapCollector) next(t *testing.T) discovery.Snapshot {
	t.Helper()
	select {
	case s := <-c.ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for snapshot")
		return nil
	}
}

func (c *snapCollector) expectNone(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case s := <-c.ch:
		t.Fatalf("expected no snapshot, got %d members", len(s))
	case <-time.After(within):
	}
}

func testOptions(server string, interval time.Duration) *do.Options {
	return &do.Options{
		Name:     "test-dns",
		Provider: "dns_srv",
		DNS: &do.DNSOptions{
			Resolver: server,
			Interval: timeconv.Duration(interval),
		},
	}
}

func TestSRVDiscovery(t *testing.T) {
	store := &recordStore{}
	store.set(dns.TypeSRV,
		"_prom._tcp.example.com. 0 IN SRV 10 1 9090 prom-a.example.com.",
		"_prom._tcp.example.com. 0 IN SRV 10 3 9090 prom-b.example.com.",
		// lower tier (higher priority value): standby, excluded in v1
		"_prom._tcp.example.com. 0 IN SRV 20 1 9090 prom-standby.example.com.",
	)
	addr := startTestDNS(t, store)

	d, err := NewSRV("test-dns", testOptions(addr, 25*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{SRVName: "_prom._tcp.example.com"},
		col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 2, "only the highest-priority tier becomes members")
	require.Equal(t, "prom-a.example.com:9090", snap[0].Address)
	require.Equal(t, 1, snap[0].Weight)
	require.Equal(t, "prom-b.example.com:9090", snap[1].Address)
	require.Equal(t, 3, snap[1].Weight, "SRV weight maps to member weight")
	require.Equal(t, "http", snap[0].Scheme)
	require.Equal(t, "10", snap[0].Labels["priority"])

	// record mutation is picked up on the next poll
	store.set(dns.TypeSRV,
		"_prom._tcp.example.com. 0 IN SRV 10 1 9090 prom-b.example.com.")
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "prom-b.example.com", snap[0].Name)
}

func TestADiscovery(t *testing.T) {
	store := &recordStore{}
	store.set(dns.TypeA,
		"prom.example.com. 0 IN A 10.0.0.1",
		"prom.example.com. 0 IN A 10.0.0.2",
	)
	store.set(dns.TypeAAAA,
		"prom.example.com. 0 IN AAAA 2001:db8::1",
	)
	addr := startTestDNS(t, store)

	d, err := NewA("test-dns", testOptions(addr, 25*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Hostname: "prom.example.com", Port: "9090", Scheme: "https"},
		col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 3, "A and AAAA answers both become members")
	addrs := make([]string, len(snap))
	for i, m := range snap {
		addrs[i] = m.Address
		require.Equal(t, "https", m.Scheme)
	}
	require.Contains(t, addrs, "10.0.0.1:9090")
	require.Contains(t, addrs, "10.0.0.2:9090")
	require.Contains(t, addrs, "[2001:db8::1]:9090")

	// rotation: one address replaced
	store.set(dns.TypeA, "prom.example.com. 0 IN A 10.0.0.3")
	store.set(dns.TypeAAAA)
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.3:9090", snap[0].Address)
}

func TestTTLFloor(t *testing.T) {
	store := &recordStore{}
	store.set(dns.TypeA, "prom.example.com. 3600 IN A 10.0.0.1")
	addr := startTestDNS(t, store)

	d, err := NewA("test-dns", testOptions(addr, 25*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Hostname: "prom.example.com", Port: "80"}, col.handle)
	require.NoError(t, err)
	defer unsub()

	require.Len(t, col.next(t), 1)
	// with a 1h TTL, the 25ms interval must NOT re-resolve; a record
	// mutation therefore goes unseen within the test window
	store.set(dns.TypeA, "prom.example.com. 3600 IN A 10.0.0.9")
	col.expectNone(t, 400*time.Millisecond)
}

func TestResolutionFailureKeepsLastGood(t *testing.T) {
	store := &recordStore{}
	store.set(dns.TypeA, "prom.example.com. 0 IN A 10.0.0.1")
	addr := startTestDNS(t, store)

	d, err := NewA("test-dns", testOptions(addr, 25*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Hostname: "prom.example.com", Port: "80"}, col.handle)
	require.NoError(t, err)
	defer unsub()

	require.Len(t, col.next(t), 1)

	// SERVFAIL responses must not emit anything: the last-good membership
	// keeps serving through the outage, and each failure counts in the
	// refresh-errors metric
	errs0 := testutil.ToFloat64(metrics.DiscoveryRefreshErrors.
		WithLabelValues("test-dns", "dns_a"))
	store.setFail(true)
	col.expectNone(t, 300*time.Millisecond)
	require.Greater(t, testutil.ToFloat64(metrics.DiscoveryRefreshErrors.
		WithLabelValues("test-dns", "dns_a")), errs0)

	// on recovery, the (changed) answer is emitted again
	store.setFail(false)
	store.set(dns.TypeA, "prom.example.com. 0 IN A 10.0.0.2")
	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.2:80", snap[0].Address)

	// an authoritative empty answer is different: it is a valid membership
	// and empties the pool's discovered set
	store.set(dns.TypeA)
	store.set(dns.TypeAAAA)
	require.Empty(t, col.next(t))
}

func TestMinTTL(t *testing.T) {
	require.Equal(t, 30*time.Second, minTTL(0, 30))
	require.Equal(t, 10*time.Second, minTTL(30*time.Second, 10))
	require.Equal(t, 10*time.Second, minTTL(10*time.Second, 30))
}

func TestNewDiscovererErrors(t *testing.T) {
	_, err := NewSRV("d", nil)
	require.Error(t, err)
	_, err = NewA("d", &do.Options{Name: "d", Provider: "dns_a"})
	require.Error(t, err, "missing dns block")
}

func TestSubscribeLifecycle(t *testing.T) {
	store := &recordStore{}
	addr := startTestDNS(t, store)
	d, err := NewSRV("test-dns", testOptions(addr, 25*time.Millisecond))
	require.NoError(t, err)

	// subscribing before Start launches on Start
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{SRVName: "x.example.com"}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.NoError(t, d.Start(t.Context()))
	require.Empty(t, col.next(t))

	_, err = d.Subscribe(nil, nil)
	require.Error(t, err)
	require.NoError(t, d.Stop())
	_, err = d.Subscribe(&do.Query{SRVName: "x"}, col.handle)
	require.ErrorIs(t, err, ErrStopped)
	require.NoError(t, d.Stop(), "Stop is idempotent")
}

// TestStdResolver exercises the stdlib-resolver fallback against the
// in-process DNS server via a custom Dial
func TestStdResolver(t *testing.T) {
	store := &recordStore{}
	store.set(dns.TypeSRV,
		"_prom._tcp.example.com. 30 IN SRV 10 2 9090 prom-a.example.com.")
	store.set(dns.TypeA, "prom.example.com. 30 IN A 10.0.0.1")
	store.set(dns.TypeAAAA)
	addr := startTestDNS(t, store)

	r := &stdResolver{r: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}}
	srvs, ttl, err := r.lookupSRV(t.Context(), "_prom._tcp.example.com.")
	require.NoError(t, err)
	require.Len(t, srvs, 1)
	require.Equal(t, "prom-a.example.com.", srvs[0].Target)
	require.Equal(t, uint16(2), srvs[0].Weight)
	require.Zero(t, ttl, "the stdlib resolver conveys no TTLs")

	ips, ttl, err := r.lookupIP(t.Context(), "prom.example.com.")
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.1"}, ips)
	require.Zero(t, ttl)

	// a NOERROR answer with no records: the stdlib surfaces empty IP
	// answers as a lookup error, which the poll loop treats as a
	// resolution failure (keep last-good)
	store.set(dns.TypeA)
	_, _, err = r.lookupIP(t.Context(), "prom.example.com.")
	require.Error(t, err)
}

func TestNewResolverSelection(t *testing.T) {
	r, err := newResolver("10.0.0.53:53")
	require.NoError(t, err)
	require.IsType(t, &directResolver{}, r)

	// with no server configured, either the resolv.conf-backed direct
	// resolver or the stdlib fallback is acceptable; it must not error
	r, err = newResolver("")
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestModeAccessors(t *testing.T) {
	pSRV := &provider{mode: modeSRV}
	pA := &provider{mode: modeA}
	require.Equal(t, "dns_srv", pSRV.providerName())
	require.Equal(t, "dns_a", pA.providerName())
	sSRV := &subscription{p: pSRV, q: &do.Query{SRVName: "s"}}
	sA := &subscription{p: pA, q: &do.Query{Hostname: "h"}}
	require.Equal(t, "s", sSRV.queryName())
	require.Equal(t, "h", sA.queryName())
}

func TestNewProviderIntervalDefault(t *testing.T) {
	p, err := newProvider("d", &do.Options{Provider: "dns_srv",
		DNS: &do.DNSOptions{Resolver: "10.0.0.53:53"}}, modeSRV)
	require.NoError(t, err)
	require.Equal(t, do.DefaultDNSInterval, p.interval)
}
