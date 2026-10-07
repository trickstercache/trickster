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

package resolution

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
	"github.com/trickstercache/trickster/v2/pkg/dns/resolver"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/resolution/options"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	testNetwork   = "tcp"
	testOwner     = "_app._tcp.example.com"
	testOwnerFQDN = testOwner + "."
	testDialAddr  = testOwner + ":80"
	testTargetA   = "node-a.example.com."
	testTargetB   = "node-b.example.com."
	testLoopback  = "127.0.0.1"
	testTimeout   = 3 * time.Second
)

// fakeResolver answers from fixed maps and counts its lookups
type fakeResolver struct {
	mtx      sync.Mutex
	srv      map[string]*resolver.SRVAnswer
	ips      map[string]resolver.IPAnswer
	srvCalls atomic.Int64
	ipCalls  atomic.Int64
}

func newFakeResolver() *fakeResolver {
	return &fakeResolver{
		srv: make(map[string]*resolver.SRVAnswer),
		ips: make(map[string]resolver.IPAnswer),
	}
}

func (r *fakeResolver) setSRV(owner string, answer *resolver.SRVAnswer) {
	r.mtx.Lock()
	r.srv[owner] = answer
	r.mtx.Unlock()
}

func (r *fakeResolver) setIP(name string, addrs ...string) {
	r.mtx.Lock()
	r.ips[name] = resolver.IPAnswer{Addrs: addrs, TTL: testRecTTL}
	r.mtx.Unlock()
}

func (r *fakeResolver) LookupSRV(_ context.Context, fqdn string) (*resolver.SRVAnswer, error) {
	r.srvCalls.Add(1)
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if answer, ok := r.srv[fqdn]; ok {
		return answer, nil
	}
	return nil, resolver.ErrNotFound
}

func (r *fakeResolver) LookupIP(_ context.Context, fqdn string) (resolver.IPAnswer, error) {
	r.ipCalls.Add(1)
	r.mtx.Lock()
	defer r.mtx.Unlock()
	if ia, ok := r.ips[fqdn]; ok {
		return ia, nil
	}
	return resolver.IPAnswer{}, resolver.ErrNotFound
}

func srvRecord(priority, weight uint16, port int, target string) *dnsclient.SRV {
	return &dnsclient.SRV{Priority: priority, Weight: weight, Port: uint16(port), Target: target}
}

// listen returns a loopback listener that accepts and holds connections, and its port
func listen(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen(testNetwork, testLoopback+":0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()
	return ln, ln.Addr().(*net.TCPAddr).Port
}

// deadPort returns a loopback port with nothing listening on it
func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen(testNetwork, testLoopback+":0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func newTestDialer(t *testing.T, name string, r resolver.Resolver) *Dialer {
	t.Helper()
	d := newDialer(name, &options.Options{Mode: options.ModeSRV}, r, 0, testTimeout)
	d.intn = func(int) int { return 0 }
	return d
}

func dialPort(t *testing.T, d *Dialer, addr string) int {
	t.Helper()
	conn, err := d.DialContext(t.Context(), testNetwork, addr)
	require.NoError(t, err)
	defer conn.Close()
	return conn.RemoteAddr().(*net.TCPAddr).Port
}

// since snapshots a process-global counter and returns its growth, so assertions hold under -count
func since(c prometheus.Counter) func() float64 {
	base := testutil.ToFloat64(c)
	return func() float64 { return testutil.ToFloat64(c) - base }
}

func attempts(backend, tier, result string) func() float64 {
	return since(metrics.OriginSRVDialAttempts.WithLabelValues(backend, tier, result))
}

func lookups(backend string, res lookupResult) func() float64 {
	return since(metrics.OriginSRVLookups.WithLabelValues(backend, lookupResultNames[res]))
}

func TestNew(t *testing.T) {
	require.Nil(t, New("b", nil, 0, testTimeout))
	require.Nil(t, New("b", &options.Options{Mode: options.ModeA}, 0, testTimeout))
	var nilDialer *Dialer
	require.False(t, nilDialer.VerifiesTarget())

	d := New("b", &options.Options{
		Mode: options.ModeSRV, Resolver: "10.0.0.2:53",
		TLSServerName: options.ServerNameTarget,
	}, 0, testTimeout)
	require.NotNil(t, d)
	require.True(t, d.VerifiesTarget())
	require.NotNil(t, New("b", &options.Options{Mode: options.ModeSRV}, 0, testTimeout))
}

func TestDialUsesSRVPort(t *testing.T) {
	const backend = "srv-port"
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(10, 1, port, testTargetA)}, TTL: testRecTTL,
	})
	r.setIP(testTargetA, testLoopback)
	d := newTestDialer(t, backend, r)
	miss, hit := lookups(backend, resultMiss), lookups(backend, resultHit)
	ok := attempts(backend, "0", attemptOK)

	// the URL's port is ignored, and the owner name matches case-insensitively
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Equal(t, port, dialPort(t, d, "_APP._tcp.example.com.:443"))
	require.Equal(t, int64(1), r.srvCalls.Load(), "the second dial hits the cache")
	require.Equal(t, int64(1), r.ipCalls.Load())
	require.Equal(t, float64(1), miss())
	require.Equal(t, float64(1), hit())
	require.Equal(t, float64(2), ok())
}

func TestDialFailoverWithinTier(t *testing.T) {
	const backend = "srv-failover"
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{Records: []*dnsclient.SRV{
		srvRecord(10, 1, deadPort(t), testTargetA),
		srvRecord(10, 1, port, testTargetB),
	}})
	r.setIP(testTargetA, testLoopback)
	r.setIP(testTargetB, testLoopback)
	d := newTestDialer(t, backend, r)
	failed, ok := attempts(backend, "0", attemptFailed), attempts(backend, "0", attemptOK)

	// the weighted pick always lands on the dead target first, and the dial falls through
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Equal(t, float64(1), failed())
	require.Equal(t, float64(1), ok())
}

func TestDialFallsToLowerTier(t *testing.T) {
	const backend = "srv-tiers"
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{Records: []*dnsclient.SRV{
		srvRecord(20, 1, port, testTargetB),
		srvRecord(10, 1, deadPort(t), testTargetA),
		// an unresolvable target in the preferred tier counts as a failed attempt
		srvRecord(10, 1, port, "missing.example.com."),
		srvRecord(10, 1, port, "."),
	}})
	r.setIP(testTargetA, testLoopback)
	r.setIP(testTargetB, testLoopback)
	d := newTestDialer(t, backend, r)
	failed, ok := attempts(backend, "0", attemptFailed), attempts(backend, "1", attemptOK)

	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Equal(t, float64(2), failed())
	require.Equal(t, float64(1), ok())
}

func TestDialAdditionalSection(t *testing.T) {
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, port, "Node-A.example.com.")},
		TTL:     testRecTTL,
		Additional: map[string]resolver.IPAnswer{
			testTargetA: {Addrs: []string{testLoopback}, TTL: testRecTTL},
		},
	})
	d := newTestDialer(t, "srv-additional", r)
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Zero(t, r.ipCalls.Load(), "the additional section answers the target's address")
}

func TestDialIPLiteral(t *testing.T) {
	_, port := listen(t)
	r := newFakeResolver()
	d := newTestDialer(t, "srv-ip", r)
	require.Equal(t, port, dialPort(t, d, net.JoinHostPort(testLoopback, strconv.Itoa(port))))
	require.Zero(t, r.srvCalls.Load())
}

func TestDialIPLiteralTarget(t *testing.T) {
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, port, testLoopback+".")},
	})
	d := newTestDialer(t, "srv-ip-target", r)
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Zero(t, r.ipCalls.Load(), "an IP literal target needs no address lookup")
}

func TestDialErrors(t *testing.T) {
	const backend = "srv-errors"
	r := newFakeResolver()
	d := newTestDialer(t, backend, r)
	negative := lookups(backend, resultNegative)

	_, err := d.DialContext(t.Context(), testNetwork, testOwner)
	require.Error(t, err, "an address without a port")

	_, err = d.DialContext(t.Context(), testNetwork, testDialAddr)
	require.ErrorIs(t, err, resolver.ErrNotFound)
	require.Equal(t, float64(1), negative())

	r.setSRV("empty.example.com.", &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, 80, ".")},
	})
	_, err = d.DialContext(t.Context(), testNetwork, "empty.example.com:80")
	require.ErrorIs(t, err, errNoTargets, "a lone '.' target means no service")

	r.setSRV("dead.example.com.", &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, deadPort(t), testTargetA)},
	})
	r.setIP(testTargetA, testLoopback)
	_, err = d.DialContext(t.Context(), testNetwork, "dead.example.com:80")
	require.Error(t, err)

	r.setSRV("noaddr.example.com.", &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, 80, testTargetB)},
	})
	r.mtx.Lock()
	r.ips[testTargetB] = resolver.IPAnswer{}
	r.mtx.Unlock()
	_, err = d.DialContext(t.Context(), testNetwork, "noaddr.example.com:80")
	require.ErrorIs(t, err, errNoTargets)
}

func TestDialTLSTarget(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(ts.Close)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	// the httptest certificate names example.com, never the SRV owner
	const certName = "example.com."
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, port, certName)},
	})
	r.setIP(certName, testLoopback)
	d := newTestDialer(t, "srv-tls", r)
	cfg := ts.Client().Transport.(*http.Transport).TLSClientConfig

	conn, err := d.DialTLSContext(t.Context(), testNetwork, testOwner+":443", cfg)
	require.NoError(t, err)
	tc, ok := conn.(*tls.Conn)
	require.True(t, ok)
	require.Equal(t, "example.com", tc.ConnectionState().ServerName)
	require.NoError(t, conn.Close())

	// without the test CA the handshake fails verification and the connection is closed
	_, err = d.DialTLSContext(t.Context(), testNetwork, testOwner+":443", nil)
	require.Error(t, err)

	_, err = d.DialTLSContext(t.Context(), testNetwork, "missing.example.com:443", cfg)
	require.ErrorIs(t, err, resolver.ErrNotFound)
}

func TestOrder(t *testing.T) {
	ts := func(weights ...int) []target {
		out := make([]target, len(weights))
		for i, w := range weights {
			out[i] = target{host: strconv.Itoa(i), weight: w}
		}
		return out
	}
	hosts := func(in []target) string {
		var s string
		for _, t := range in {
			s += t.host
		}
		return s
	}
	pick := func(k int) func(int) int { return func(n int) int { return min(k, n-1) } }

	// RFC 2782: a draw in [0, sum] picks the first zero-weight target on 0, else the first
	// target whose running sum reaches the draw
	tests := []struct {
		weights []int
		draw    int
		want    string
	}{
		{[]int{1, 3}, 0, "01"},
		{[]int{1, 3}, 1, "01"},
		{[]int{1, 3}, 2, "10"},
		{[]int{1, 3}, 4, "10"},
		{[]int{0, 2, 0, 1}, 0, "0213"},
		{[]int{0, 2, 0, 1}, 1, "1302"},
		{[]int{0, 2, 0, 1}, 3, "3102"},
		{[]int{2, 0}, 0, "10"},
		{[]int{0, 0}, 0, "01"},
		{[]int{0, 0}, 1, "10"},
	}
	for _, test := range tests {
		l := ts(test.weights...)
		shuffleByWeight(l, pick(test.draw))
		require.Equal(t, test.want, hosts(l), "weights %v, draw %d", test.weights, test.draw)
	}

	// every target, zero weights included, leads the order for some draw
	seen := make(map[string]bool)
	for draw := range 4 {
		l := ts(0, 2, 0, 1)
		shuffleByWeight(l, pick(draw))
		seen[l[0].host] = true
	}
	require.Len(t, seen, 3, "the first zero-weight, and each weighted, target can lead")

	lone := &srvAnswer{tiers: [][]target{ts(5)}, n: 1}
	require.Equal(t, &lone.tiers[0][0], &lone.order(pick(0))[0], "a lone target is not copied")

	two := &srvAnswer{tiers: [][]target{ts(1), ts(1)}, n: 2}
	require.Len(t, two.order(pick(0)), 2)
}

func TestAttemptBudget(t *testing.T) {
	b := attemptBudget{floor: minAttemptTimeout, reserve: attemptReserve}
	require.Equal(t, minAttemptTimeout, b.share(context.Background(), 3))

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	require.InDelta(t, float64(9*time.Second), float64(b.share(ctx, 1)), float64(time.Second))
	require.InDelta(t, float64(3*time.Second), float64(b.share(ctx, 3)), float64(time.Second))
	// 6 attempts in 9s: the floor still fits, leaving each of the other 5 its reserve
	require.Equal(t, minAttemptTimeout, b.share(ctx, 6))
	// 30 attempts in 9s: the share is raised only as far as the other 29 keep their reserve
	require.InDelta(t, float64(9*time.Second-29*attemptReserve), float64(b.share(ctx, 30)),
		float64(50*time.Millisecond))
	// 40 attempts in 9s: reserves alone exceed the time left, so the share stays fair
	require.InDelta(t, float64(9*time.Second/40), float64(b.share(ctx, 40)),
		float64(50*time.Millisecond))

	short, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	require.LessOrEqual(t, b.share(short, 2), time.Second-attemptReserve,
		"an attempt never takes the next attempt's reserve")
}

const (
	// hangingAddr is a TEST-NET-1 address whose dials hang until their deadline
	hangingAddr       = "192.0.2.1"
	budgetDialTimeout = time.Second
	budgetFloor       = 200 * time.Millisecond
	budgetReserve     = 50 * time.Millisecond
)

// newBudgetDialer dials hangingAddr* until the attempt's deadline, and everything else for real
func newBudgetDialer(t *testing.T, name string, r resolver.Resolver) *Dialer {
	t.Helper()
	d := newTestDialer(t, name, r)
	d.timeout = budgetDialTimeout
	d.budget = attemptBudget{floor: budgetFloor, reserve: budgetReserve}
	d.dialAddr = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if host, _, _ := net.SplitHostPort(addr); strings.HasPrefix(host, "192.0.2.") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return d.base.DialContext(ctx, network, addr)
	}
	return d
}

func TestDialReachesSecondAddressOfLoneTarget(t *testing.T) {
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, port, testTargetA)},
	})
	r.setIP(testTargetA, hangingAddr, testLoopback)
	d := newBudgetDialer(t, "srv-budget-addrs", r)
	// the hanging address gets only its share of the target's budget, leaving time for the next
	require.Equal(t, port, dialPort(t, d, testDialAddr))
}

func TestDialFailsOverPastHangingAddresses(t *testing.T) {
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{Records: []*dnsclient.SRV{
		srvRecord(10, 1, port, testTargetA),
		srvRecord(10, 1, port, testTargetB),
	}})
	// under per-address shares of the whole dial, five hanging addresses outlast the dial
	r.setIP(testTargetA, "192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5")
	r.setIP(testTargetB, testLoopback)
	d := newBudgetDialer(t, "srv-budget-targets", r)
	// target A's hanging addresses share A's half of the timeout, so B is still tried
	require.Equal(t, port, dialPort(t, d, testDialAddr))
}

func TestDialTriesEveryAddressPastTheFloor(t *testing.T) {
	_, port := listen(t)
	r := newFakeResolver()
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, port, testTargetA)},
	})
	// more hanging addresses than the timeout holds floors; only the last is reachable
	r.setIP(testTargetA, "192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5",
		"192.0.2.6", "192.0.2.7", testLoopback)
	d := newBudgetDialer(t, "srv-budget-floor", r)
	require.Equal(t, port, dialPort(t, d, testDialAddr))
}

// hangingResolver answers from a fakeResolver until it is told to hang, after which every
// lookup blocks until its context ends or the test finishes
type hangingResolver struct {
	*fakeResolver
	hang    atomic.Bool
	release chan struct{}
}

func (r *hangingResolver) wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return context.Canceled
	}
}

func (r *hangingResolver) LookupSRV(ctx context.Context, fqdn string) (*resolver.SRVAnswer, error) {
	if r.hang.Load() {
		return nil, r.wait(ctx)
	}
	return r.fakeResolver.LookupSRV(ctx, fqdn)
}

func (r *hangingResolver) LookupIP(ctx context.Context, fqdn string) (resolver.IPAnswer, error) {
	if r.hang.Load() {
		return resolver.IPAnswer{}, r.wait(ctx)
	}
	return r.fakeResolver.LookupIP(ctx, fqdn)
}

func TestDialServesStaleWhileDNSHangs(t *testing.T) {
	const backend = "srv-stale-hang"
	_, port := listen(t)
	r := &hangingResolver{fakeResolver: newFakeResolver(), release: make(chan struct{})}
	t.Cleanup(func() { close(r.release) })
	r.setSRV(testOwnerFQDN, &resolver.SRVAnswer{
		Records: []*dnsclient.SRV{srvRecord(0, 0, port, testTargetA)}, TTL: testRecTTL,
	})
	r.setIP(testTargetA, testLoopback)
	d := newTestDialer(t, backend, r)
	stale := lookups(backend, resultStale)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	d.srv.now, d.addrs.now = clk.now, clk.now
	require.Equal(t, port, dialPort(t, d, testDialAddr))

	// both the SRV and the address answers expire, and DNS stops answering
	clk.advance(testRecTTL)
	r.hang.Store(true)
	start := time.Now()
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Less(t, time.Since(start), 2*staleRefreshWait+time.Second,
		"each cache waits only staleRefreshWait before serving its last good answer")
	require.Equal(t, float64(1), stale())
}
