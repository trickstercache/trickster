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

package dynamic

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/discovery"

	"github.com/stretchr/testify/require"
)

const (
	retireTestEndpoint = prop.StoreAPIScheme + "://thanos-sidecar:10901"
	retireTestRounds   = 3
	retireTestWait     = 5 * time.Second
	// retireTestDrain is the drain a removed member's requests get before its client retires
	retireTestDrain = 300 * time.Millisecond
)

// builtMembers records each client a manager's factories build, by name
type builtMembers struct {
	mtx     sync.Mutex
	clients map[string][]backends.Backend
}

func (b *builtMembers) of(name string) []backends.Backend {
	b.mtx.Lock()
	defer b.mtx.Unlock()
	return append([]backends.Backend(nil), b.clients[name]...)
}

func recordMembers(m *Manager) *builtMembers {
	b := &builtMembers{clients: make(map[string][]backends.Backend)}
	factories := make(rt.Lookup, len(m.cfg.Factories))
	for provider, factory := range m.cfg.Factories {
		if factory == nil {
			continue
		}
		factories[provider] = func(name string, o *bo.Options, r http.Handler, c cache.Cache,
			members backends.Backends, f rt.Lookup,
		) (backends.Backend, error) {
			client, err := factory(name, o, r, c, members, f)
			if err == nil {
				b.mtx.Lock()
				b.clients[name] = append(b.clients[name], client)
				b.mtx.Unlock()
			}
			return client, err
		}
	}
	m.cfg.Factories = factories
	return b
}

func newStoreAPIManager(t *testing.T) (*Manager, *builtMembers) {
	t.Helper()
	tmpl := newSeriesTemplate(t, providers.Prometheus, &prop.Options{
		Flavor: prop.FlavorThanos, SeriesEndpoint: retireTestEndpoint,
	})
	m, _, _, _ := newTSMTestManager(t, tsmoptions.EvaluationGlobal, tmpl)
	return m, recordMembers(m)
}

// sourceOf builds the raw series source a global evaluation reads client through, as its first evaluation does
func sourceOf(t *testing.T, client backends.Backend) backends.TSMGlobalSource {
	t.Helper()
	source, err := client.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
	require.NoError(t, err)
	return source
}

func retired(client backends.Backend) bool {
	_, err := client.(backends.TSMGlobalSourceProvider).TSMGlobalSource()
	return err != nil
}

func TestManagerRetiresMemberClientsAfterTheDrain(t *testing.T) {
	m, built := newStoreAPIManager(t)
	m.cfg.DrainTimeout = retireTestDrain
	// a retired client's requests get the first part of their drain undisturbed, however busy the test's host
	drained := func(client backends.Backend, msgAndArgs ...any) {
		t.Helper()
		require.Never(t, func() bool { return retired(client) }, retireTestDrain/3, time.Millisecond, msgAndArgs...)
		require.Eventually(t, func() bool { return retired(client) }, retireTestWait, time.Millisecond)
	}
	var sources []backends.TSMGlobalSource
	var clients []backends.Backend
	// each round moves the member, rebuilding its client, so the last round's is retired once its requests drain
	for round := range retireTestRounds {
		m.ApplySnapshot(discovery.Snapshot{member("m1", "10.0.0."+strconv.Itoa(round+1)+":9009")})
		if round > 0 {
			drained(clients[round-1], "a replaced member's requests get their drain")
			require.Error(t, sources[round-1].(io.Closer).Close(), "the replaced member's connection was closed")
		}
		require.Equal(t, []string{globalTestMember}, m.MemberNames())
		all := built.of(globalTestMember)
		require.Len(t, all, round+1)
		sources, clients = append(sources, sourceOf(t, all[round])), append(clients, all[round])
	}
	// a member that leaves is retired the same way
	m.ApplySnapshot(discovery.Snapshot{member("m2", "10.0.0.9:9009")})
	drained(clients[len(clients)-1])
	m2 := built.of("myalb-m2")
	require.Len(t, m2, 1)
	sourceOf(t, m2[0])
}

func TestManagerStopLeavesMembersToTheALB(t *testing.T) {
	tmpl := newSeriesTemplate(t, providers.Prometheus, &prop.Options{
		Flavor: prop.FlavorThanos, SeriesEndpoint: retireTestEndpoint,
	})
	m, _, _, transports := newTSMTestManager(t, tsmoptions.EvaluationGlobal, tmpl)
	built := recordMembers(m)
	m.cfg.DrainTimeout = retireTestDrain
	names := []string{globalTestMember, "myalb-m2"}
	m.ApplySnapshot(discovery.Snapshot{member("m1", globalTestAddr), member("m2", "10.0.0.2:9009")})
	members := append(built.of(names[0]), built.of(names[1])...)
	require.Len(t, members, 2)
	sources := []backends.TSMGlobalSource{sourceOf(t, members[0]), sourceOf(t, members[1])}
	m.Stop()
	require.Empty(t, m.MemberNames())
	// the stopped manager's ALB still dispatches to its pool's members, as one a failed reload restores serves on
	require.Never(t, func() bool { return retired(members[0]) || retired(members[1]) }, 2*retireTestDrain, time.Millisecond,
		"a stopped manager's members were retired while their ALB could still read them")
	for _, name := range names {
		require.Eventually(t, transports.of(name)[0].closed, retireTestWait, time.Millisecond,
			"the idle connections of a stopped manager's members close after the drain")
	}
	backends.Retire(context.Background(), m.cfg.ALB)
	for i, client := range members {
		require.True(t, retired(client), "the members retire with their ALB")
		require.Error(t, sources[i].(io.Closer).Close(), "the member's connection was closed")
		require.Equal(t, int32(2), transports.of(names[i])[0].web.closes.Load(), "the members' idle connections close again")
	}
	// a member handed to a retired ALB is retired at once
	m3, err := m.instantiateMember("myalb-m3", member("m3", "10.0.0.3:9009"))
	require.NoError(t, err)
	m.cfg.ALB.RetireWith(backends.Backends{"myalb-m3": m3.client})
	require.True(t, retired(m3.client))
}

func TestManagerRetiresClientsOfRefusedMembers(t *testing.T) {
	m, built := newStoreAPIManager(t)
	m.cfg.DrainTimeout = time.Hour
	m.cfg.HealthChecker = failingRegistry{HealthChecker: m.cfg.HealthChecker}
	_, err := m.instantiateMember(globalTestMember, member("m1", globalTestAddr))
	require.ErrorContains(t, err, "registration refused")
	clients := built.of(globalTestMember)
	require.Len(t, clients, 1)
	// a client that never served is retired at once, whatever drain admitted members get
	require.True(t, retired(clients[0]))
}
