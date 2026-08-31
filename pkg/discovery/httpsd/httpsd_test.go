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

package httpsd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const nativeBody = "- name: prom-1\n  address: 10.0.0.1:9090\n  weight: 2\n"

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

// expectNoPollResult waits until the server has served at least n requests,
// then asserts that none of them produced a snapshot. Waiting on real polls
// matters: the poller jitters its start by up to a second, so a bare
// "nothing arrived in 300ms" assertion would pass before the first request
// was ever sent, and would keep passing if the provider broke entirely.
func (c *snapCollector) expectNoPollResult(t *testing.T, srv *mutableServer, n int64) {
	t.Helper()
	require.Eventually(t, func() bool { return srv.hits.Load() >= n },
		10*time.Second, 10*time.Millisecond,
		"the endpoint was never polled, so nothing was actually tested")
	require.Empty(t, c.ch, "expected no snapshot from a rejected response")
}

func (c *snapCollector) expectNone(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case s := <-c.ch:
		t.Fatalf("unexpected snapshot: %v", s)
	case <-time.After(d):
	}
}

// mutableServer serves a body the test can change between polls.
type mutableServer struct {
	*httptest.Server
	mtx     sync.Mutex
	body    string
	status  int
	etag    string
	hits    atomic.Int64
	lastReq atomic.Pointer[http.Header]
}

func newMutableServer(t *testing.T, body string) *mutableServer {
	t.Helper()
	m := &mutableServer{body: body, status: http.StatusOK}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.hits.Add(1)
		h := r.Header.Clone()
		m.lastReq.Store(&h)
		m.mtx.Lock()
		body, status, etag := m.body, m.status, m.etag
		m.mtx.Unlock()
		if etag != "" {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *mutableServer) set(body string, status int) {
	m.mtx.Lock()
	m.body, m.status = body, status
	m.mtx.Unlock()
}

func (m *mutableServer) setETag(etag string) {
	m.mtx.Lock()
	m.etag = etag
	m.mtx.Unlock()
}

func testOptions(endpoint, format string, interval time.Duration) *do.Options {
	return &do.Options{
		Name:     "test-httpsd",
		Provider: "http_sd",
		HTTP: &do.HTTPOptions{
			Endpoint: endpoint,
			Interval: timeconv.Duration(interval),
			Timeout:  timeconv.Duration(2 * time.Second),
		},
		HTTPSD: &do.HTTPSDOptions{Format: format},
	}
}

func startDiscoverer(t *testing.T, o *do.Options) discovery.Discoverer {
	t.Helper()
	d, err := New("test-httpsd", o)
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	t.Cleanup(func() { d.Stop() })
	return d
}

func TestNewRequiresHTTPOptions(t *testing.T) {
	_, err := New("test", nil)
	require.ErrorIs(t, err, ErrNoHTTPOptions)
	_, err = New("test", &do.Options{Provider: "http_sd"})
	require.ErrorIs(t, err, ErrNoHTTPOptions)
}

func TestNativeFormatDiscovery(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "prom-1", snap[0].Name)
	require.Equal(t, "10.0.0.1:9090", snap[0].Address)
	require.Equal(t, 2, snap[0].Weight, "the native format carries weight")

	// a changed member list is picked up on the next poll
	srv.set("- name: prom-2\n  address: 10.0.0.2:9090\n", http.StatusOK)
	snap = col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "10.0.0.2:9090", snap[0].Address)
}

func TestPrometheusFormatDiscovery(t *testing.T) {
	srv := newMutableServer(t,
		`[{"targets": ["10.0.0.1:9090", "10.0.0.2:9090"], "labels": {"env": "prod"}}]`)
	o := testOptions(srv.URL, do.FormatPrometheus, 25*time.Millisecond)
	d := startDiscoverer(t, o)
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{Scheme: "https"}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 2)
	require.Equal(t, "10.0.0.1:9090", snap[0].Address)
	require.Equal(t, "https", snap[0].Scheme,
		"prometheus targets are bare host:port, so the query supplies the scheme")
	require.Equal(t, "prod", snap[0].Labels["env"])
}

// The format is configuration, not a guess: a prometheus document served to
// a trickster-format discoverer must fail rather than be coerced.
func TestFormatIsNotSniffed(t *testing.T) {
	srv := newMutableServer(t, `[{"targets": ["10.0.0.1:9090"]}]`)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	col.expectNoPollResult(t, srv, 2)
}

// The query's path selects one member list from an endpoint serving several,
// so ALBs with different pools can share a discoverer.
func TestQueryPathSelectsMemberList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pools/a":
			w.Write([]byte("- address: 10.0.0.1:9090\n"))
		case "/pools/b":
			w.Write([]byte("- address: 10.0.0.2:9090\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))

	colA, colB := newSnapCollector(), newSnapCollector()
	unsubA, err := d.Subscribe(&do.Query{Path: "/pools/a"}, colA.handle)
	require.NoError(t, err)
	defer unsubA()
	unsubB, err := d.Subscribe(&do.Query{Path: "/pools/b"}, colB.handle)
	require.NoError(t, err)
	defer unsubB()

	require.Equal(t, "10.0.0.1:9090", colA.next(t)[0].Address)
	require.Equal(t, "10.0.0.2:9090", colB.next(t)[0].Address)
}

// Every failure mode keeps the last-good membership. An endpoint that goes
// down must not drain the pool it was feeding.
func TestFailuresKeepLastGood(t *testing.T) {
	for name, breakIt := range map[string]func(*mutableServer){
		"server error":   func(m *mutableServer) { m.set("", http.StatusInternalServerError) },
		"unparsable":     func(m *mutableServer) { m.set("{{{ not a member list", http.StatusOK) },
		"invalid member": func(m *mutableServer) { m.set("- address: not-host-port\n", http.StatusOK) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := newMutableServer(t, nativeBody)
			d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
			col := newSnapCollector()
			unsub, err := d.Subscribe(&do.Query{}, col.handle)
			require.NoError(t, err)
			defer unsub()
			require.Len(t, col.next(t), 1)

			breakIt(srv)
			// no further snapshot: not an empty one, not a partial one
			col.expectNone(t, 400*time.Millisecond)

			// and it recovers without a restart
			srv.set("- address: 10.0.0.7:9090\n", http.StatusOK)
			require.Equal(t, "10.0.0.7:9090", col.next(t)[0].Address)
		})
	}
}

// An endpoint that genuinely has no members is not a failure, and must be
// able to say so -- otherwise a scaled-to-zero pool can never be reported.
func TestAuthoritativeEmptyIsEmitted(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.Len(t, col.next(t), 1)

	srv.set("[]", http.StatusOK)
	require.Empty(t, col.next(t))
}

func TestRefreshFailureIsCounted(t *testing.T) {
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-httpsd", "http_sd"))
	srv := newMutableServer(t, "")
	srv.set("", http.StatusInternalServerError)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-httpsd", "http_sd")) > before
	}, 5*time.Second, 10*time.Millisecond)
}

// ETag turns an unchanged membership into a 304, which must be a no-op
// rather than an empty snapshot or an error.
func TestConditionalRequestNotModifiedIsANoOp(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	srv.setETag(`"v1"`)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.Len(t, col.next(t), 1)

	// subsequent polls are 304s; no snapshot, no error, and the request
	// carries the validator we were given
	col.expectNone(t, 300*time.Millisecond)
	require.Greater(t, srv.hits.Load(), int64(1))
	require.Equal(t, `"v1"`, srv.lastReq.Load().Get("If-None-Match"))

	// a new version invalidates it and membership updates
	srv.setETag(`"v2"`)
	srv.set("- address: 10.0.0.8:9090\n", http.StatusOK)
	require.Equal(t, "10.0.0.8:9090", col.next(t)[0].Address)
}

// The validator must only be stored after a successful parse; otherwise the
// next 304 would confirm a document we rejected, and the provider would sit
// on a stale membership believing it current.
func TestETagNotStoredForRejectedDocument(t *testing.T) {
	srv := newMutableServer(t, "{{{ not a member list")
	srv.setETag(`"bad"`)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	col.expectNoPollResult(t, srv, 2)
	require.Empty(t, srv.lastReq.Load().Get("If-None-Match"),
		"a rejected document's ETag must not be sent as a validator")

	// once the document is valid, membership arrives
	srv.set(nativeBody, http.StatusOK)
	require.Len(t, col.next(t), 1)
}

func TestRefreshIntervalHeaderIsSent(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 30*time.Second))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	col.next(t)
	require.Equal(t, "30", srv.lastReq.Load().Get(refreshIntervalHeader))
}

func TestStaticHeadersAndBearerToken(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	o := testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond)
	o.HTTP.Headers = map[string]string{"X-Consul-Token": "abc"}
	o.HTTP.BearerToken = "tok"
	d := startDiscoverer(t, o)
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	col.next(t)
	h := srv.lastReq.Load()
	require.Equal(t, "abc", h.Get("X-Consul-Token"))
	require.Equal(t, "Bearer tok", h.Get("Authorization"))
}

func TestBasicAuth(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	o := testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond)
	o.HTTP.Username, o.HTTP.Password = "u", "p"
	d := startDiscoverer(t, o)
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	col.next(t)
	require.True(t, strings.HasPrefix(srv.lastReq.Load().Get("Authorization"), "Basic "))
}

// The token file is the rotation mechanism, so it is re-read per poll
// rather than cached at construction.
func TestBearerTokenFileIsRereadPerPoll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0o600))

	srv := newMutableServer(t, nativeBody)
	o := testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond)
	o.HTTP.BearerTokenFile = path
	d := startDiscoverer(t, o)
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	col.next(t)
	require.Equal(t, "Bearer first", srv.lastReq.Load().Get("Authorization"),
		"trailing whitespace should be trimmed")

	require.NoError(t, os.WriteFile(path, []byte("second"), 0o600))
	require.Eventually(t, func() bool {
		return srv.lastReq.Load().Get("Authorization") == "Bearer second"
	}, 5*time.Second, 10*time.Millisecond, "a rotated token was not picked up")
}

// An unreadable token file fails the poll without sending an unauthenticated
// request, and keeps last-good.
func TestUnreadableTokenFileFailsThePoll(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	o := testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond)
	o.HTTP.BearerTokenFile = filepath.Join(t.TempDir(), "absent")
	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-httpsd", "http_sd"))
	d := startDiscoverer(t, o)
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	// the request is never sent, so the failure metric is the only evidence
	// that a poll was attempted at all
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-httpsd", "http_sd")) > before
	}, 10*time.Second, 10*time.Millisecond)
	require.Zero(t, srv.hits.Load(), "no request should be sent without the credential")
	require.Empty(t, col.ch)
}

func TestReadBodyRejectsOversizedDocument(t *testing.T) {
	_, err := readBody(strings.NewReader(strings.Repeat("x", maxResponseBytes+1)))
	require.Error(t, err)
	require.Contains(t, err.Error(), "limit")

	b, err := readBody(strings.NewReader("ok"))
	require.NoError(t, err)
	require.Equal(t, "ok", string(b))
}

func TestReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, []byte("  \n"), 0o600))
	_, err := readTokenFile(empty)
	require.Error(t, err, "an empty token file is a misconfiguration, not a token")
	_, err = readTokenFile(filepath.Join(dir, "absent"))
	require.Error(t, err)
}

func TestSubscribeLifecycle(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	d, err := New("test-httpsd", testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	require.NoError(t, err)
	require.NoError(t, d.Start(t.Context()))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	col.next(t)
	unsub()
	require.NoError(t, d.Stop())
	_, err = d.Subscribe(&do.Query{}, col.handle)
	require.ErrorIs(t, err, ErrStopped)
}

// A transport failure -- endpoint down, DNS gone, TLS rejected -- returns
// before the response handler runs. Accounting done inside the handler would
// miss it entirely, leaving the provider serving a stale membership with no
// metric and no log to say why. This is the most likely failure an operator
// will actually hit, so it is pinned separately from the status-code path.
func TestTransportFailureIsCountedAndKeepsLastGood(t *testing.T) {
	srv := newMutableServer(t, nativeBody)
	d := startDiscoverer(t, testOptions(srv.URL, do.FormatTrickster, 25*time.Millisecond))
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.Len(t, col.next(t), 1, "membership before the endpoint fails")

	before := testutil.ToFloat64(
		metrics.DiscoveryRefreshErrors.WithLabelValues("test-httpsd", "http_sd"))
	srv.Close() // nothing is listening now

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(
			metrics.DiscoveryRefreshErrors.WithLabelValues("test-httpsd", "http_sd")) > before
	}, 10*time.Second, 10*time.Millisecond,
		"a transport failure produced no refresh error")
	require.Empty(t, col.ch, "the last-good membership must be kept, not replaced")
}
