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

package nomad

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	consulopts "github.com/trickstercache/trickster/v2/pkg/discovery/consul/options"
	nomadopts "github.com/trickstercache/trickster/v2/pkg/discovery/nomad/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	bqtest "github.com/trickstercache/trickster/v2/pkg/testutil/blockingquery"

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

// reg builds a service registration for tests.
func reg(id, addr string, port int, tags ...string) serviceRegistration {
	return serviceRegistration{
		ID: id, ServiceName: "web", Address: addr, Port: port, Tags: tags,
		Namespace: "default", Datacenter: "dc1",
		JobID: "web-job", AllocID: "alloc-1", NodeID: "node-1",
	}
}

func newFakeNomad(t *testing.T, regs ...serviceRegistration) *bqtest.Server {
	t.Helper()
	if regs == nil {
		regs = []serviceRegistration{}
	}
	return bqtest.New(t, indexHeader, regs)
}

func testOptions(endpoint string) *do.Options {
	o := &do.Options{
		Name:     "test-nomad",
		Provider: "nomad",
		HTTP: &do.HTTPOptions{
			Endpoint: endpoint,
			Interval: timeconv.Duration(50 * time.Millisecond),
		},
		Nomad: &nomadopts.Options{Wait: timeconv.Duration(2 * time.Second)},
	}
	o.HTTP.Timeout = timeconv.Duration(consulopts.PollTimeout(o.Nomad.GetWait()))
	return o
}

func startDiscoverer(t *testing.T, o *do.Options) discovery.Discoverer {
	t.Helper()
	d, err := New("test-nomad", o)
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
	_, err = New("test", &do.Options{Provider: "nomad"})
	require.ErrorIs(t, err, ErrNoHTTPOptions)
}

func TestSubscribeRequiresService(t *testing.T) {
	f := newFakeNomad(t)
	d := startDiscoverer(t, testOptions(f.URL))
	_, err := d.Subscribe(&do.Query{}, func(discovery.Snapshot) {})
	require.Error(t, err)
	require.Contains(t, err.Error(), "service")
}

func TestServiceDiscovery(t *testing.T) {
	f := newFakeNomad(t,
		reg("web-1", "10.0.0.1", 8080),
		reg("web-2", "10.0.0.2", 8080),
	)
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})

	snap := col.next(t)
	require.Len(t, snap, 2)
	require.Equal(t, "10.0.0.1:8080", snap[0].Address)
	require.Equal(t, "web-1", snap[0].Name)
	require.Equal(t, "web-job", snap[0].Labels["job_id"])
	require.Equal(t, "alloc-1", snap[0].Labels["alloc_id"])

	req := f.NextRequest(t)
	require.Equal(t, "/v1/service/web", req.Path)
	require.Empty(t, req.Query["index"], "the first read must return immediately")
}

// Nomad's native registry carries no per-instance check state, so readiness
// is genuinely unknown rather than assumed good. Claiming Ready here would
// make health_mode: provider silently trust an unverified member.
func TestReadinessIsUnknown(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Equal(t, discovery.ReadyUnknown, col.next(t)[0].Ready)
}

func TestBlockingQueryObservesChange(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	f.NextRequest(t) // the initial read
	req := f.NextRequest(t)
	require.NotEmpty(t, req.Query["index"], "a subsequent read must block on the cursor")
	require.Equal(t, "2s", req.Query["wait"][0])

	start := time.Now()
	f.Update(t, []serviceRegistration{
		reg("web-1", "10.0.0.1", 8080),
		reg("web-3", "10.0.0.3", 8080),
	})
	require.Len(t, col.next(t), 2)
	require.Less(t, time.Since(start), 2*time.Second,
		"the change should arrive on the parked request, not on the next interval")
}

func TestUnchangedBlockingQueryIsANoOp(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	o := testOptions(f.URL)
	o.Nomad.Wait = timeconv.Duration(time.Second)
	o.HTTP.Timeout = timeconv.Duration(consulopts.PollTimeout(time.Second))
	d := startDiscoverer(t, o)
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	select {
	case s := <-col.ch:
		t.Fatalf("unexpected re-emission of unchanged membership: %v", s)
	case <-time.After(1500 * time.Millisecond):
	}
}

func TestIndexGoingBackwardsRecovers(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	f.SetBody(t, []serviceRegistration{reg("web-9", "10.0.0.9", 8080)})
	f.RewindIndex(5)

	snap := col.next(t)
	require.Equal(t, "10.0.0.9:8080", snap[0].Address,
		"the provider did not recover from a rewound index")
}

func TestFailureKeepsLastGoodAndRecovers(t *testing.T) {
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-nomad", "nomad"))
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)

	f.SetStatus(http.StatusForbidden)
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-nomad", "nomad")) > before
	}, 10*time.Second, 10*time.Millisecond)
	require.Empty(t, col.ch, "a failing registry must not replace the membership")

	f.SetBody(t, []serviceRegistration{reg("web-5", "10.0.0.5", 8080)})
	f.SetStatus(http.StatusOK)
	require.Equal(t, "10.0.0.5:8080", col.next(t)[0].Address)
}

func TestAuthoritativeEmptyIsEmitted(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	d := startDiscoverer(t, testOptions(f.URL))
	col := subscribe(t, d, &do.Query{Service: "web"})
	require.Len(t, col.next(t), 1)
	f.Update(t, []serviceRegistration{})
	require.Empty(t, col.next(t))
}

func TestQueryParametersArePassedThrough(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	o := testOptions(f.URL)
	o.Nomad.Namespace = "team-a"
	o.Nomad.Region = "eu-1"
	o.Nomad.AllowStale = true
	d := startDiscoverer(t, o)
	subscribe(t, d, &do.Query{Service: "web", Filter: `JobID == "web-job"`})

	req := f.NextRequest(t)
	require.Equal(t, "team-a", req.Query["namespace"][0])
	require.Equal(t, "eu-1", req.Query["region"][0])
	require.Contains(t, req.Query, "stale")
	require.Equal(t, `JobID == "web-job"`, req.Query["filter"][0])
	require.NotContains(t, req.Query, "tag",
		"nomad's service endpoint has no tag parameter; tags filter client-side")
}

func TestACLTokenIsSent(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	o := testOptions(f.URL)
	o.HTTP.Headers = map[string]string{"X-Nomad-Token": "static-token"}
	d := startDiscoverer(t, o)
	subscribe(t, d, &do.Query{Service: "web"})
	require.Equal(t, "static-token", f.NextRequest(t).Header.Get("X-Nomad-Token"))
}

// Nomad accepts the Authorization Bearer scheme as an equivalent to its own
// header, which is what makes bearer_token_file usable for a rotated ACL
// token without a nomad-specific option.
func TestRotatedTokenFileIsReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0o600))
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	o := testOptions(f.URL)
	o.HTTP.BearerTokenFile = path
	d := startDiscoverer(t, o)
	subscribe(t, d, &do.Query{Service: "web"})
	require.Equal(t, "Bearer first", f.NextRequest(t).Header.Get("Authorization"))

	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	f.BumpIndex()
	require.Eventually(t, func() bool {
		r, ok := f.TryNextRequest()
		return ok && r.Header.Get("Authorization") == "Bearer second"
	}, 10*time.Second, 10*time.Millisecond, "a rotated token was not picked up")
}

func TestSubscribeLifecycle(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	d, err := New("test-nomad", testOptions(f.URL))
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

// Stop must not wait out a parked blocking query.
func TestStopIsPromptDuringABlockingQuery(t *testing.T) {
	f := newFakeNomad(t, reg("web-1", "10.0.0.1", 8080))
	o := testOptions(f.URL)
	o.Nomad.Wait = timeconv.Duration(9 * time.Second)
	o.HTTP.Timeout = timeconv.Duration(consulopts.PollTimeout(9 * time.Second))
	d, err := New("test-nomad", o)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	col := newSnapCollector()
	_, err = d.Subscribe(&do.Query{Service: "web"}, col.handle)
	require.NoError(t, err)
	col.next(t)
	f.NextRequest(t)
	f.NextRequest(t) // the blocking one is now parked on the server

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
