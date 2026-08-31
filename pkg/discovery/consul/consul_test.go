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

package consul

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

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
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for snapshot")
		return nil
	}
}

func testOptions(endpoint string) *do.Options {
	o := &do.Options{
		Name:     "test-consul",
		Provider: "consul",
		HTTP: &do.HTTPOptions{
			Endpoint: endpoint,
			Interval: timeconv.Duration(50 * time.Millisecond),
		},
		Consul: &do.ConsulOptions{
			Wait: timeconv.Duration(2 * time.Second),
		},
	}
	o.HTTP.Timeout = timeconv.Duration(do.ConsulPollTimeout(o.Consul.GetWait()))
	return o
}

func startDiscoverer(t *testing.T, o *do.Options) discovery.Discoverer {
	t.Helper()
	d, err := New("test-consul", o)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	t.Cleanup(func() { d.Stop() })
	return d
}

func subscribe(t *testing.T, d discovery.Discoverer, q *do.Query) *snapCollector {
	t.Helper()
	col := newSnapCollector()
	unsub, err := d.Subscribe(q, col.handle)
	require.NoError(t, err)
	t.Cleanup(unsub)
	return col
}

func TestNewRequiresHTTPOptions(t *testing.T) {
	_, err := New("test", nil)
	require.ErrorIs(t, err, ErrNoHTTPOptions)
	_, err = New("test", &do.Options{Provider: "consul"})
	require.ErrorIs(t, err, ErrNoHTTPOptions)
}

func TestSubscribeRequiresService(t *testing.T) {
	f := newFakeConsul(t)
	d := startDiscoverer(t, testOptions(f.URL))
	_, err := d.Subscribe(&do.Query{}, func(discovery.Snapshot) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "service")
}

func TestServiceDiscovery(t *testing.T) {
	f := newFakeConsul(t,
		entry("web-1", "10.0.0.1", 8080, statusPassing),
		entry("web-2", "10.0.0.2", 8080, statusPassing),
	)
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})

	snap := col.next(t)
	require.Len(t, snap, 2)
	require.Equal(t, "10.0.0.1:8080", snap[0].Address)
	require.Equal(t, "web-1", snap[0].Name)
	require.Equal(t, discovery.Ready, snap[0].Ready)
	require.Equal(t, "web", snap[0].Labels["service"])
	require.Equal(t, "dc1", snap[0].Labels["datacenter"])
	require.Equal(t, "node-1", snap[0].Labels["node"])

	// the first request must not block, or the pool never fills
	req := f.nextRequest(t)
	require.Equal(t, "/v1/health/service/web", req.Path)
	require.Empty(t, req.Query["index"], "the first read must return immediately")
}

// The point of the provider: a change is observed within a round trip,
// because the server was already holding the request when it happened.
func TestBlockingQueryObservesChange(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	// the second request carries the cursor and blocks
	f.nextRequest(t) // the initial read
	req := f.nextRequest(t)
	require.NotEmpty(t, req.Query["index"], "a subsequent read must block on the cursor")
	require.Equal(t, "2s", req.Query["wait"][0])

	start := time.Now()
	f.update(
		entry("web-1", "10.0.0.1", 8080, statusPassing),
		entry("web-3", "10.0.0.3", 8080, statusPassing),
	)
	snap := col.next(t)
	require.Len(t, snap, 2)
	require.Less(t, time.Since(start), 2*time.Second,
		"the change should arrive on the parked request, not on the next interval")
}

// A blocking query that times out unchanged must be a no-op, and must not
// re-emit or re-parse.
func TestUnchangedBlockingQueryIsANoOp(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	o := testOptions(f.URL)
	o.Consul.Wait = timeconv.Duration(time.Second)
	o.HTTP.Timeout = timeconv.Duration(do.ConsulPollTimeout(time.Second))
	d := startDiscoverer(t, o)
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	select {
	case s := <-col.ch:
		t.Fatalf("unexpected re-emission of unchanged membership: %v", s)
	case <-time.After(1500 * time.Millisecond):
	}
}

// An index change that does not change the catalog still re-reads, and the
// Emitter suppresses the no-op rather than churning the pool.
func TestIndexBumpWithoutCatalogChangeDoesNotReEmit(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	f.bumpIndexOnly()
	select {
	case s := <-col.ch:
		t.Fatalf("membership was re-emitted unchanged: %v", s)
	case <-time.After(500 * time.Millisecond):
	}
}

// Consul's index can go backwards when a server is replaced or a service is
// recreated. A client that keeps blocking on the old cursor would park
// forever against an index the server will never reach again.
func TestIndexGoingBackwardsRecovers(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	// the server resets to a much lower index and serves a new catalog
	f.mtx.Lock()
	f.entries = []serviceEntry{entry("web-9", "10.0.0.9", 8080, statusPassing)}
	f.mtx.Unlock()
	f.rewindIndex(5)

	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.9:8080", snap[0].Address,
		"the provider did not recover from a rewound index")
}

// Without a usable cursor there is nothing to block on, so the provider must
// fall back to plain reads rather than stalling.
func TestMissingIndexHeaderFallsBackToPlainReads(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	f.setOmitIndexHeader(true)
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	f.mtx.Lock()
	f.entries = []serviceEntry{entry("web-4", "10.0.0.4", 8080, statusPassing)}
	f.mtx.Unlock()

	snap := col.next(t)
	require.Equal(t, "10.0.0.4:8080", snap[0].Address)
	req := f.nextRequest(t)
	require.Empty(t, req.Query["index"],
		"without a cursor from the server, requests must not claim one")
}

func TestFailureKeepsLastGoodAndRecovers(t *testing.T) {
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-consul", "consul"))
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	f.setStatus(http.StatusInternalServerError)
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-consul", "consul")) > before
	}, 10*time.Second, 10*time.Millisecond)
	require.Empty(t, col.ch, "a failing catalog must not replace the membership")

	f.mtx.Lock()
	f.entries = []serviceEntry{entry("web-5", "10.0.0.5", 8080, statusPassing)}
	f.mtx.Unlock()
	f.setStatus(http.StatusOK)
	require.Equal(t, "10.0.0.5:8080", col.next(t)[0].Address)
}

// A service that genuinely has no instances is a valid membership; a pool
// scaled to zero must be reportable.
func TestAuthoritativeEmptyIsEmitted(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)
	f.update()
	require.Empty(t, col.next(t))
}

func TestQueryParametersArePassedThrough(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	o := testOptions(f.URL)
	o.Consul.Datacenter = "dc2"
	o.Consul.Namespace = "team-a"
	o.Consul.Partition = "p1"
	o.Consul.AllowStale = true
	o.Consul.OnlyPassing = true
	d := startDiscoverer(t, o)
	subscribe(t, d, &do.Query{
		Service: "web",
		Tags:    []string{"prod", "v2"},
		Filter:  `Service.Meta.version == "2"`,
	})

	req := f.nextRequest(t)
	require.Equal(t, "dc2", req.Query["dc"][0])
	require.Equal(t, "team-a", req.Query["ns"][0])
	require.Equal(t, "p1", req.Query["partition"][0])
	require.Equal(t, "true", req.Query["passing"][0])
	require.Contains(t, req.Query, "stale")
	require.Equal(t, []string{"prod", "v2"}, req.Query["tag"])
	require.Equal(t, `Service.Meta.version == "2"`, req.Query["filter"][0])
}

func TestACLTokenIsSent(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	o := testOptions(f.URL)
	o.HTTP.Headers = map[string]string{"X-Consul-Token": "static-token"}
	d := startDiscoverer(t, o)
	subscribe(t, d, &do.Query{Service: "web"})
	require.Equal(t, "static-token", f.nextRequest(t).Header.Get("X-Consul-Token"))
}

// Consul accepts the Authorization Bearer scheme as an equivalent to its own
// header, which is what makes bearer_token_file usable for a rotated ACL
// token without a consul-specific option.
func TestRotatedTokenFileIsReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0o600))
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	o := testOptions(f.URL)
	o.HTTP.BearerTokenFile = path
	d := startDiscoverer(t, o)
	subscribe(t, d, &do.Query{Service: "web"})
	require.Equal(t, "Bearer first", f.nextRequest(t).Header.Get("Authorization"))

	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	f.bumpIndexOnly()
	require.Eventually(t, func() bool {
		select {
		case r := <-f.reqs:
			return r.Header.Get("Authorization") == "Bearer second"
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond, "a rotated token was not picked up")
}

func TestSubscribeLifecycle(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	d, err := New("test-consul", testOptions(f.URL))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{Service: "web"}, col.handle)
	require.NoError(t, err)
	col.next(t)
	unsub()
	require.NoError(t, d.Stop())
	_, err = d.Subscribe(&do.Query{Service: "web"}, col.handle)
	require.ErrorIs(t, err, ErrStopped)
}

// Stop must not wait out a parked blocking query. With DetachIterations
// false, cancelling the iteration context tears down the in-flight request.
func TestStopIsPromptDuringABlockingQuery(t *testing.T) {
	f := newFakeConsul(t, entry("web-1", "10.0.0.1", 8080, statusPassing))
	o := testOptions(f.URL)
	o.Consul.Wait = timeconv.Duration(9 * time.Second)
	o.HTTP.Timeout = timeconv.Duration(do.ConsulPollTimeout(9 * time.Second))
	d, err := New("test-consul", o)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	col := newSnapCollector()
	_, err = d.Subscribe(&do.Query{Service: "web"}, col.handle)
	require.NoError(t, err)
	col.next(t)
	f.nextRequest(t)
	f.nextRequest(t) // the blocking one is now parked on the server

	done := make(chan struct{})
	go func() {
		d.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited for a parked blocking query instead of cancelling it")
	}
}

func TestConsulDuration(t *testing.T) {
	require.Equal(t, "5m", consulDuration(5*time.Minute))
	require.Equal(t, "90s", consulDuration(90*time.Second))
	require.Equal(t, "1s", consulDuration(time.Second))
}
