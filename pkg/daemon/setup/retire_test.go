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
	"context"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	cacheoptions "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"

	"github.com/stretchr/testify/require"
)

const (
	retireEvent = "retire"
	closeEvent  = "close"
)

// events records what the instance's backends and caches do, in order
type events struct {
	mu   sync.Mutex
	list []string
	// deadline is the last retirement's
	deadline time.Time
}

func (e *events) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, event)
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.list...)
}

// retireProbe is a backend recording its retirement
type retireProbe struct {
	backends.Backend
	events *events
}

func (p *retireProbe) Retire(ctx context.Context) {
	p.events.mu.Lock()
	p.events.deadline, _ = ctx.Deadline()
	p.events.mu.Unlock()
	p.events.add(retireEvent)
}

func newRetireProbe(t *testing.T, ev *events) *retireProbe {
	t.Helper()
	o := bo.New()
	o.Provider = providers.ReverseProxyShort
	b, err := backends.New("probe", o, nil, lm.NewRouter(), nil)
	require.NoError(t, err)
	return &retireProbe{Backend: b, events: ev}
}

// closeProbe is a cache recording its close
type closeProbe struct {
	cache.Cache
	events *events
}

func (c *closeProbe) Close() error {
	c.events.add(closeEvent)
	return nil
}

func TestShutdownRetiresBackendsBeforeClosingCaches(t *testing.T) {
	ev := &events{}
	si := &instance.ServerInstance{
		Backends: backends.Backends{"probe": newRetireProbe(t, ev)},
		Caches:   cache.Lookup{"probe": &closeProbe{events: ev}},
	}
	const drain = 7 * time.Second
	si.SetMgmtOptions(&mgmt.Options{ShutdownDrainTimeout: timeconv.Duration(drain)})
	start := time.Now()
	Shutdown(si)
	// what the backends still run may write to the caches, so it ends first, within the shutdown drain
	require.Equal(t, []string{retireEvent, closeEvent}, ev.all())
	require.WithinDuration(t, start.Add(drain), ev.deadline, time.Second)
}

func TestApplyConfigRetiresTheClientsOfAFailedApply(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	require.NoError(t, err)
	quietListeners(conf)
	conf.Authenticators = options.Lookup{
		"bad": &options.Options{Name: "bad", Provider: "not-a-provider"},
	}
	ev := &events{}
	clients["probe"] = newRetireProbe(t, ev)
	start := time.Now()
	require.Error(t, ApplyConfig(&instance.ServerInstance{}, conf, clients, nil, nil, listener.NewGroup()))
	// clients that never served are retired at once
	require.Equal(t, []string{retireEvent}, ev.all())
	require.WithinDuration(t, start, ev.deadline, time.Second)
}

func TestApplyConfigKeepsTheClientsOfAnApply(t *testing.T) {
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	require.NoError(t, err)
	quietListeners(conf)
	ev := &events{}
	clients["probe"] = newRetireProbe(t, ev)
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	require.NoError(t, ApplyConfig(si, conf, clients, nil, nil, group))
	t.Cleanup(func() { Shutdown(si) })
	require.Empty(t, ev.all(), "the clients an apply commits serve on")
}

// stuckRetirer is a backend whose retirement takes its whole deadline, as work that ends only then would
type stuckRetirer struct {
	backends.Backend
}

func (stuckRetirer) Retire(ctx context.Context) { <-ctx.Done() }

// blockingStores is a cache client whose stores signal as they start and wait until released
type blockingStores struct {
	cache.Client
	storing, release chan struct{}
}

func (c *blockingStores) Store(string, []byte, time.Duration) error {
	c.storing <- struct{}{}
	<-c.release
	return nil
}

func TestShutdownRetiresAndClosesCachesWithinOneDrain(t *testing.T) {
	const drain = 400 * time.Millisecond
	o := cacheoptions.New()
	client := &blockingStores{Client: memory.New("stuck", o), storing: make(chan struct{}, 1), release: make(chan struct{})}
	t.Cleanup(sync.OnceFunc(func() { close(client.release) }))
	c := manager.NewCache(client, manager.CacheOptions{}, o)
	require.NoError(t, c.Connect())
	go func() { _ = c.Store("k", []byte("v"), time.Minute) }()
	<-client.storing
	si := &instance.ServerInstance{
		Backends: backends.Backends{"stuck": stuckRetirer{Backend: newRetireProbe(t, &events{}).Backend}},
		Caches:   cache.Lookup{"stuck": c},
	}
	si.SetMgmtOptions(&mgmt.Options{ShutdownDrainTimeout: timeconv.Duration(drain)})
	start := time.Now()
	Shutdown(si)
	elapsed := time.Since(start)
	// a backend's work and the cache's operations under way share the one drain, as each could take all of it
	require.GreaterOrEqual(t, elapsed, drain)
	require.Less(t, elapsed, drain*3/2, "shutdown outlasted its drain")
}

// storeAPIMemberConfig is a backend reading raw series over a StoreAPI connection of its own, which retiring it closes
const storeAPIMemberConfig = `
backends:
  member:
    provider: prometheus
    origin_url: 'http://thanos-query:10904'
    prometheus:
      flavor: thanos
      series_endpoint: 'grpc://thanos-sidecar:10901'
`

// recordClients records each client map BootstrapConfigWithOverlay builds until the test ends
func recordClients(t *testing.T) *[]backends.Backends {
	t.Helper()
	prev := newClients
	var built []backends.Backends
	newClients = func(size int) backends.Backends {
		b := prev(size)
		built = append(built, b)
		return b
	}
	t.Cleanup(func() { newClients = prev })
	return &built
}

func TestBootstrapRetiresTheClientsItDoesNotReturn(t *testing.T) {
	valid := writeConfig(t, storeAPIMemberConfig)
	// the sticky cookie check runs once every client is built
	invalid := writeConfig(t, storeAPIMemberConfig+`
  origin:
    provider: rp
    origin_url: 'http://example.com'
  first:
    provider: alb
    alb: {mechanism: rr, pool: [{name: origin}], sticky: {}}
  second:
    provider: alb
    alb: {mechanism: rr, pool: [{name: origin}], sticky: {}}
`)
	for _, tc := range []struct {
		name    string
		args    []string
		failing bool
	}{
		{"routes rules and pools fail", []string{"-config", invalid}, true},
		{"validate config", []string{"-validate-config", "-config", valid}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built := recordClients(t)
			_, clients, err := BootstrapConfig(tc.args...)
			require.Equal(t, tc.failing, err != nil, err)
			require.Nil(t, clients)
			require.Len(t, *built, 1)
			member := (*built)[0]["member"]
			require.NotNil(t, member)
			require.True(t, sourceRetired(member), "a client that never serves is retired")
		})
	}
	built := recordClients(t)
	_, clients, err := BootstrapConfig("-config", valid)
	require.NoError(t, err)
	require.Same(t, (*built)[0]["member"], clients["member"])
	require.False(t, sourceRetired(clients["member"]), "the clients returned serve")
	clients.Retire(0)
}
