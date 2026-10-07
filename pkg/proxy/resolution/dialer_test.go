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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dnsclient "github.com/trickstercache/trickster/v2/pkg/dns/client"
	"github.com/trickstercache/trickster/v2/pkg/dns/resolver"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/resolution/options"

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

func attempts(backend, tier, result string) float64 {
	return testutil.ToFloat64(metrics.OriginSRVDialAttempts.WithLabelValues(backend, tier, result))
}

func lookups(backend string, res lookupResult) float64 {
	return testutil.ToFloat64(metrics.OriginSRVLookups.WithLabelValues(backend,
		lookupResultNames[res]))
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

	// the URL's port is ignored, and the owner name matches case-insensitively
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Equal(t, port, dialPort(t, d, "_APP._tcp.example.com.:443"))
	require.Equal(t, int64(1), r.srvCalls.Load(), "the second dial hits the cache")
	require.Equal(t, int64(1), r.ipCalls.Load())
	require.Equal(t, float64(1), lookups(backend, resultMiss))
	require.Equal(t, float64(1), lookups(backend, resultHit))
	require.Equal(t, float64(2), attempts(backend, "0", attemptOK))
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

	// the weighted pick always lands on the dead target first, and the dial falls through
	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Equal(t, float64(1), attempts(backend, "0", attemptFailed))
	require.Equal(t, float64(1), attempts(backend, "0", attemptOK))
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

	require.Equal(t, port, dialPort(t, d, testDialAddr))
	require.Equal(t, float64(2), attempts(backend, "0", attemptFailed))
	require.Equal(t, float64(1), attempts(backend, "1", attemptOK))
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

	_, err := d.DialContext(t.Context(), testNetwork, testOwner)
	require.Error(t, err, "an address without a port")

	_, err = d.DialContext(t.Context(), testNetwork, testDialAddr)
	require.ErrorIs(t, err, resolver.ErrNotFound)
	require.Equal(t, float64(1), lookups(backend, resultNegative))

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

	l := ts(1, 3)
	shuffleByWeight(l, pick(0))
	require.Equal(t, "01", hosts(l))
	l = ts(1, 3)
	shuffleByWeight(l, pick(1))
	require.Equal(t, "10", hosts(l), "a draw past the first weight selects the second")
	l = ts(0, 2, 0, 1)
	shuffleByWeight(l, pick(2))
	require.Equal(t, "3120", hosts(l), "zero weights come after every weighted target")
	l = ts(0, 0)
	shuffleByWeight(l, pick(0))
	require.Equal(t, "01", hosts(l))

	lone := &srvAnswer{tiers: [][]target{ts(5)}, n: 1}
	require.Equal(t, &lone.tiers[0][0], &lone.order(pick(0))[0], "a lone target is not copied")

	two := &srvAnswer{tiers: [][]target{ts(1), ts(1)}, n: 2}
	require.Len(t, two.order(pick(0)), 2)
}

func TestAttemptTimeout(t *testing.T) {
	require.Equal(t, minAttemptTimeout, attemptTimeout(context.Background(), 3))

	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
	defer cancel()
	require.InDelta(t, float64(9*time.Second), float64(attemptTimeout(ctx, 1)),
		float64(time.Second))
	require.InDelta(t, float64(3*time.Second), float64(attemptTimeout(ctx, 3)),
		float64(time.Second))
	require.Equal(t, minAttemptTimeout, attemptTimeout(ctx, 100), "a share has a floor")

	short, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	require.LessOrEqual(t, attemptTimeout(short, 100), time.Second, "the floor never exceeds the time left")
}
