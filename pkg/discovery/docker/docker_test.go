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

package docker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	dockeropts "github.com/trickstercache/trickster/v2/pkg/discovery/docker/options"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// fakeDaemon serves a canned container list and records what was asked
type fakeDaemon struct {
	*httptest.Server
	requests chan *url.URL
	body     string
	status   int
}

func newFakeDaemon(t *testing.T, body string) *fakeDaemon {
	f := &fakeDaemon{requests: make(chan *url.URL, 16), body: body, status: 200}
	f.Server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			select {
			case f.requests <- r.URL:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
		}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDaemon) nextRequest(t *testing.T) *url.URL {
	t.Helper()
	select {
	case u := <-f.requests:
		return u
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a request")
		return nil
	}
}

func fakeOptions(endpoint string) *do.Options {
	return &do.Options{
		Name:     "test-docker",
		Provider: "docker",
		Docker:   &dockeropts.Options{},
		HTTP:     &do.HTTPOptions{Endpoint: endpoint},
	}
}

func oneContainer() string {
	return `[{"Id":"0123456789abcdef","Names":["/prom"],"Image":"prom/prometheus",
	  "State":"running","Status":"Up 2 days (healthy)",
	  "Ports":[{"PrivatePort":9090,"Type":"tcp"}],
	  "NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.18.0.2"}}}}]`
}

func TestNewRequiresDockerOptions(t *testing.T) {
	_, err := New("test", nil)
	require.Error(t, err)
	_, err = New("test", &do.Options{Provider: "docker"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "'docker' options block")
}

// The endpoint defaults to the well-known socket, so a config that names
// no endpoint is the common case rather than an error.
func TestEndpointDefaultsToTheSocket(t *testing.T) {
	p, err := newProvider("test", &do.Options{
		Provider: "docker", Docker: &dockeropts.Options{}})
	require.NoError(t, err)
	require.Equal(t, "http://"+socketHost, p.endpoint,
		"a socket request needs a syntactically valid but meaningless authority")
}

func TestEndpointSchemes(t *testing.T) {
	for name, tc := range map[string]struct {
		endpoint string
		wantErr  string
		wantBase string
	}{
		"tcp becomes http":        {endpoint: "tcp://dockerhost:2375", wantBase: "http://dockerhost:2375"},
		"http passes through":     {endpoint: "http://dockerhost:2375", wantBase: "http://dockerhost:2375"},
		"https passes through":    {endpoint: "https://dockerhost:2376", wantBase: "https://dockerhost:2376"},
		"unix needs a path":       {endpoint: "unix://", wantErr: "names no socket path"},
		"tcp needs a host":        {endpoint: "tcp://", wantErr: "names no host"},
		"unknown scheme is fatal": {endpoint: "ssh://dockerhost", wantErr: "must use the unix, tcp, http or https scheme"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := newProvider("test", fakeOptions(tc.endpoint))
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantBase, p.endpoint)
		})
	}
}

// tcp:// is the DOCKER_HOST spelling; whether it is http or https on the
// wire depends on whether TLS was configured, which is how a remote
// daemon's mutual TLS is set up.
func TestTCPEndpointBecomesHTTPSWithTLS(t *testing.T) {
	o := fakeOptions("tcp://dockerhost:2376")
	o.HTTP.TLS = &to.Options{InsecureSkipVerify: true}
	p, err := newProvider("test", o)
	require.NoError(t, err)
	require.Equal(t, "https://dockerhost:2376", p.endpoint)
}

// TLS on a unix socket is a config mistake worth naming rather than
// silently ignoring.
func TestTLSOnSocketIsRejected(t *testing.T) {
	o := fakeOptions("unix:///var/run/docker.sock")
	o.HTTP.TLS = &to.Options{InsecureSkipVerify: true}
	_, err := newProvider("test", o)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot apply tls to a unix socket")
}

// The API version is pinned into the path, so the response shape cannot
// change under Trickster when the host upgrades Docker.
func TestRequestShape(t *testing.T) {
	f := newFakeDaemon(t, oneContainer())
	s := launch(t, f.URL, &do.Query{})
	defer s.Stop()

	u := f.nextRequest(t)
	require.Equal(t, "/"+dockeropts.DefaultAPIVersion+containersPath, u.Path)

	var filters map[string][]string
	require.NoError(t, json.Unmarshal([]byte(u.Query().Get("filters")), &filters))
	require.Equal(t, []string{stateRunning}, filters["status"],
		"only running containers by default, not everything that ever ran")
}

func TestAPIVersionIsConfigurable(t *testing.T) {
	f := newFakeDaemon(t, oneContainer())
	o := fakeOptions(f.URL)
	o.Docker.APIVersion = "v1.44"
	p, err := newProvider("test-docker", o)
	require.NoError(t, err)
	run, err := p.newSubscription(&do.Query{}, func(discovery.Snapshot) {})
	require.NoError(t, err)
	run.Launch(t.Context())
	defer run.Stop()
	require.Equal(t, "/v1.44"+containersPath, f.nextRequest(t).Path)
}

// An operator's own status filter is honored exactly, rather than being
// merged with the default.
func TestExplicitStatusFilterWins(t *testing.T) {
	f := newFakeDaemon(t, oneContainer())
	s := launch(t, f.URL, &do.Query{
		Filters: map[string][]string{
			"status": {"running", "paused"},
			"label":  {"com.example.discover=yes"},
		}})
	defer s.Stop()

	var filters map[string][]string
	require.NoError(t, json.Unmarshal(
		[]byte(f.nextRequest(t).Query().Get("filters")), &filters))
	require.Equal(t, []string{"running", "paused"}, filters["status"])
	require.Equal(t, []string{"com.example.discover=yes"}, filters["label"])
}

func TestPollEmitsMembers(t *testing.T) {
	f := newFakeDaemon(t, oneContainer())
	snaps := make(chan discovery.Snapshot, 4)
	o := fakeOptions(f.URL)
	p, err := newProvider("test-docker", o)
	require.NoError(t, err)
	run, err := p.newSubscription(&do.Query{},
		func(s discovery.Snapshot) { snaps <- s })
	require.NoError(t, err)
	run.Launch(t.Context())
	defer run.Stop()

	select {
	case snap := <-snaps:
		require.Len(t, snap, 1)
		require.Equal(t, "172.18.0.2:9090", snap[0].Address)
		require.Equal(t, discovery.Ready, snap[0].Ready)
		require.Equal(t, "http", snap[0].Scheme)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a snapshot")
	}
}

// A daemon error must surface as a refresh failure with the daemon's own
// message, which is how a bad filter name or an unsupported api_version
// becomes legible.
func TestDaemonErrorIsCountedAndExplained(t *testing.T) {
	f := newFakeDaemon(t, `{"message":"invalid filter 'nope'"}`)
	f.status = http.StatusBadRequest
	before := counterValue(t, "test-docker")

	s := launch(t, f.URL, &do.Query{})
	defer s.Stop()
	f.nextRequest(t)

	require.Eventually(t, func() bool {
		return counterValue(t, "test-docker") > before
	}, 5*time.Second, 20*time.Millisecond)
}

// The daemon explains itself in the body; that explanation is the whole
// value of the error. A live run against Docker 29.6.1 caught this being
// dropped: reading the body in truncating mode allocates the full bound
// and fails the short read, discarding the message.
func TestCheckStatusCarriesTheDaemonMessage(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body: io.NopCloser(strings.NewReader(
			`{"message":"invalid filter 'nonsense'"}`)),
	}
	err := checkStatus(resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid filter 'nonsense'",
		"the daemon's own message must survive")
	require.Contains(t, err.Error(), "400")
}

// A body that is not the expected document still yields a usable error
func TestCheckStatusFallsBackToTheStatusCode(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader("not json")),
	}
	err := checkStatus(resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

func TestCheckStatusPassesSuccess(t *testing.T) {
	require.NoError(t, checkStatus(&http.Response{StatusCode: 200}))
}

// A transport failure keeps the last-good membership rather than emitting
// an empty snapshot, which would drain the pool on a blip.
func TestTransportFailureEmitsNothing(t *testing.T) {
	snaps := make(chan discovery.Snapshot, 4)
	p, err := newProvider("test-docker", fakeOptions("http://127.0.0.1:1"))
	require.NoError(t, err)
	run, err := p.newSubscription(&do.Query{},
		func(s discovery.Snapshot) { snaps <- s })
	require.NoError(t, err)
	run.Launch(t.Context())
	defer run.Stop()

	select {
	case s := <-snaps:
		t.Fatalf("emitted %v on a transport failure", s)
	case <-time.After(300 * time.Millisecond):
	}
}

// The socket path is what carries the destination, so the provider must
// actually dial it rather than the URL's authority.
func TestUnixSocketIsDialed(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(oneContainer()))
		})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	snaps := make(chan discovery.Snapshot, 4)
	p, err := newProvider("test-docker", fakeOptions("unix://"+sock))
	require.NoError(t, err)
	run, err := p.newSubscription(&do.Query{},
		func(s discovery.Snapshot) { snaps <- s })
	require.NoError(t, err)
	run.Launch(t.Context())
	defer run.Stop()

	select {
	case snap := <-snaps:
		require.Len(t, snap, 1)
		require.Equal(t, "172.18.0.2:9090", snap[0].Address)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out; the socket was not dialed")
	}
}

func TestStopIsIdempotent(t *testing.T) {
	f := newFakeDaemon(t, oneContainer())
	s := launch(t, f.URL, &do.Query{})
	s.Stop()
	s.Stop()
}

func TestLaunchAfterStopDoesNothing(t *testing.T) {
	f := newFakeDaemon(t, oneContainer())
	p, err := newProvider("test-docker", fakeOptions(f.URL))
	require.NoError(t, err)
	run, err := p.newSubscription(&do.Query{}, func(discovery.Snapshot) {})
	require.NoError(t, err)
	run.Stop()
	run.Launch(context.Background())
}

// launch builds and starts a subscription, discarding snapshots
func launch(t *testing.T, endpoint string, q *do.Query) discovery.SubscriptionRunner {
	t.Helper()
	p, err := newProvider("test-docker", fakeOptions(endpoint))
	require.NoError(t, err)
	run, err := p.newSubscription(q, func(discovery.Snapshot) {})
	require.NoError(t, err)
	run.Launch(t.Context())
	return run
}

// counterValue reads the refresh-error counter for a discoverer
func counterValue(t *testing.T, name string) float64 {
	t.Helper()
	c, err := metrics.DiscoveryRefreshErrors.GetMetricWithLabelValues(
		name, "docker")
	require.NoError(t, err)
	var m dto.Metric
	require.NoError(t, c.(prometheus.Metric).Write(&m))
	return m.GetCounter().GetValue()
}
