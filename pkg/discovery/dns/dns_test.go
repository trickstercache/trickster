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
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/testutil/dnsserver"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

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
	srv := dnsserver.New(t)
	const zone = "_prom._tcp.example.com."
	srv.Set(dnsclient.TypeSRV,
		dnsserver.SRV(zone, 0, 10, 1, 9090, "prom-a.example.com."),
		dnsserver.SRV(zone, 0, 10, 3, 9090, "prom-b.example.com."),
		// lower tier (higher priority value): standby, excluded in v1
		dnsserver.SRV(zone, 0, 20, 1, 9090, "prom-standby.example.com."),
	)

	d, err := NewSRV("test-dns", testOptions(srv.Addr(), 25*time.Millisecond))
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
	srv.Set(dnsclient.TypeSRV,
		dnsserver.SRV(zone, 0, 10, 1, 9090, "prom-b.example.com."))
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "prom-b.example.com", snap[0].Name)
}

func TestADiscovery(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeA,
		dnsserver.A("prom.example.com.", 0, "10.0.0.1"),
		dnsserver.A("prom.example.com.", 0, "10.0.0.2"),
	)
	srv.Set(dnsclient.TypeAAAA,
		dnsserver.AAAA("prom.example.com.", 0, "2001:db8::1"),
	)

	d, err := NewA("test-dns", testOptions(srv.Addr(), 25*time.Millisecond))
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
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 0, "10.0.0.3"))
	srv.Set(dnsclient.TypeAAAA)
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.3:9090", snap[0].Address)
}

// TestTruncatedAnswer proves the UDP-to-TCP retry keeps discovery working
// when an answer does not fit in a datagram
func TestTruncatedAnswer(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 0, "10.0.0.1"))
	srv.SetTruncate(true)

	d, err := NewA("test-dns", testOptions(srv.Addr(), 25*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Hostname: "prom.example.com", Port: "80"}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.1:80", snap[0].Address)
}

func TestTTLFloor(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 3600, "10.0.0.1"))

	d, err := NewA("test-dns", testOptions(srv.Addr(), 25*time.Millisecond))
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
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 3600, "10.0.0.9"))
	col.expectNone(t, 400*time.Millisecond)
}

func TestResolutionFailureKeepsLastGood(t *testing.T) {
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 0, "10.0.0.1"))

	d, err := NewA("test-dns", testOptions(srv.Addr(), 25*time.Millisecond))
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
	srv.SetRCode(dnsclient.RCodeServerFailure)
	col.expectNone(t, 300*time.Millisecond)
	require.Greater(t, testutil.ToFloat64(metrics.DiscoveryRefreshErrors.
		WithLabelValues("test-dns", "dns_a")), errs0)

	// on recovery, the (changed) answer is emitted again
	srv.SetRCode(dnsclient.RCodeSuccess)
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 0, "10.0.0.2"))
	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.2:80", snap[0].Address)

	// an authoritative empty answer is different: it is a valid membership
	// and empties the pool's discovered set
	srv.Set(dnsclient.TypeA)
	srv.Set(dnsclient.TypeAAAA)
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
	srv := dnsserver.New(t)
	d, err := NewSRV("test-dns", testOptions(srv.Addr(), 25*time.Millisecond))
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
	srv := dnsserver.New(t)
	srv.Set(dnsclient.TypeSRV, dnsserver.SRV("_prom._tcp.example.com.", 30,
		10, 2, 9090, "prom-a.example.com."))
	srv.Set(dnsclient.TypeA, dnsserver.A("prom.example.com.", 30, "10.0.0.1"))
	srv.Set(dnsclient.TypeAAAA)

	r := &stdResolver{r: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Addr())
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
	srv.Set(dnsclient.TypeA)
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

// flakyResolver panics until it is healed, then answers normally. It stands
// in for the class of bug the shared poller exists to contain: a nil deref
// or index-out-of-range inside a provider's own mapping code.
type flakyResolver struct {
	calls  atomic.Int64
	healed atomic.Bool
}

func (r *flakyResolver) lookupSRV(context.Context, string) ([]*dnsclient.SRV, time.Duration, error) {
	r.calls.Add(1)
	if !r.healed.Load() {
		panic("resolver exploded")
	}
	return []*dnsclient.SRV{{Target: "prom-a.example.com.", Port: 9090, Weight: 1}},
		time.Minute, nil
}

func (r *flakyResolver) lookupIP(context.Context, string) ([]string, time.Duration, error) {
	r.calls.Add(1)
	if !r.healed.Load() {
		panic("resolver exploded")
	}
	return []string{"10.0.0.1"}, time.Minute, nil
}

// Before the shared poller, a panic anywhere in resolution killed the
// subscription goroutine outright: no crash, no log, no metric -- the ALB
// simply served its last membership forever while the health page stayed
// green. This asserts the loop now survives the panic and still converges
// once the underlying fault clears.
func TestResolutionPanicDoesNotFreezeMembership(t *testing.T) {
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-dns", "dns_srv"))

	r := &flakyResolver{}
	p := &provider{
		name: "test-dns", mode: modeSRV, res: r, interval: 5 * time.Millisecond,
	}
	d := discovery.NewLifecycle("test-dns", p.newSubscription)
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{SRVName: "x.example.com"}, col.handle)
	require.NoError(t, err)
	defer unsub()

	// the loop must keep calling despite every call panicking
	require.Eventually(t, func() bool { return r.calls.Load() >= 3 },
		5*time.Second, 5*time.Millisecond,
		"the poll loop died on the first panicking resolution")

	// and must still be alive to pick up the recovery
	r.healed.Store(true)
	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "prom-a.example.com:9090", snap[0].Address)

	require.Greater(t,
		testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-dns", "dns_srv")),
		before, "a panicking resolution should surface as a refresh error")
}
