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

package setup

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/static"
	so "github.com/trickstercache/trickster/v2/pkg/backends/static/options"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

func TestShutdownNilSafe(t *testing.T) {
	Shutdown(nil)
	Shutdown(&instance.ServerInstance{})
}

func TestShutdownStopsHealthChecks(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	si := &instance.ServerInstance{HealthChecker: healthcheck.New()}
	_, err = si.HealthChecker.Register("t", "t", &ho.Options{
		Verb: http.MethodGet, Scheme: u.Scheme, Host: u.Host, Path: "/",
		Interval: timeconv.Duration(10 * time.Millisecond),
	}, srv.Client())
	require.NoError(t, err)
	require.Eventually(t, func() bool { return hits.Load() > 0 },
		5*time.Second, 5*time.Millisecond, "the target was never probed")

	Shutdown(si)
	stopped := hits.Load()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, stopped, hits.Load(), "the target was probed after Shutdown")
}

func TestShutdownStopsStaticClients(t *testing.T) {
	o := bo.New()
	o.Provider = providers.Static
	o.Static = so.New()
	o.Static.Root = t.TempDir()
	client, err := static.NewClient("site", o, nil, nil, nil, nil)
	require.NoError(t, err)
	clients := backends.Backends{"site": client}
	static.StartClients(clients)

	before := runtime.NumGoroutine()
	Shutdown(&instance.ServerInstance{Backends: clients})
	require.Eventually(t, func() bool { return runtime.NumGoroutine() < before },
		5*time.Second, 5*time.Millisecond, "the static client's watcher kept running after Shutdown")
}

func TestShutdownStopsDiscovery(t *testing.T) {
	si, c, clients := newDiscoveryFixture(t, unavailableDiscoverer(),
		&do.Query{Service: "svc"}, ao.StartupPolicyRetry)
	require.NoError(t, applyDiscoveryConfig(si, c, clients, nil, nil, nil))
	require.NotEmpty(t, si.PoolManagers)
	si.Backends = clients

	Shutdown(si)
	require.Nil(t, si.PoolManagers)
	require.Nil(t, si.Discoverers)
}
