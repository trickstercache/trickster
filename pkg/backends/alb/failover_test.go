/*
 * Copyright 2026 The Trickster Authors
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

package alb

import (
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// graph builds and starts ALBs over origin backends whose health the test drives
type graph struct {
	clients backends.Backends
	health  healthcheck.StatusLookup
}

func newGraph(t *testing.T, origins ...string) *graph {
	t.Helper()
	g := &graph{clients: backends.Backends{}, health: healthcheck.StatusLookup{}}
	for _, name := range origins {
		o := bo.New()
		o.Name = name
		b, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
		require.NoError(t, err)
		g.clients[name] = b
		g.health[name] = healthcheck.NewStatus(name, "", "", healthcheck.StatusPassing, time.Time{}, nil)
	}
	return g
}

func (g *graph) alb(t *testing.T, name string, o *ao.Options) *Client {
	t.Helper()
	bo := bo.New()
	bo.Name = name
	bo.Provider = providers.ALB
	bo.ALBOptions = o
	require.NoError(t, bo.ALBOptions.Initialize(name))
	cl, err := NewClient(name, bo, nil, nil, nil, nil)
	require.NoError(t, err)
	g.clients[name] = cl
	return cl.(*Client)
}

func (g *graph) start(t *testing.T) {
	t.Helper()
	require.NoError(t, StartALBPools(g.clients, g.health))
	t.Cleanup(func() { _ = StopPools(g.clients) })
}

func liveNames(c *Client) []string {
	out := []string{}
	for _, tgt := range c.Pool().Targets() {
		out = append(out, tgt.Name())
	}
	return out
}

func TestBackupMembersStandBy(t *testing.T) {
	g := newGraph(t, "a", "b", "standby")
	c := g.alb(t, "failover-alb", &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{
		{Name: "a"}, {Name: "standby", Backup: true}, {Name: "b"},
	}})
	g.start(t)
	onBackup := func() float64 { return testutil.ToFloat64(metrics.ALBPoolOnBackup.WithLabelValues("failover-alb")) }
	require.Equal(t, []string{"a", "b"}, liveNames(c))
	require.Zero(t, onBackup())
	g.health["a"].Set(healthcheck.StatusFailing)
	require.Equal(t, []string{"b"}, liveNames(c))
	g.health["b"].Set(healthcheck.StatusFailing)
	require.Equal(t, []string{"standby"}, liveNames(c))
	require.Equal(t, 1.0, onBackup())
	pk, ok := c.Picker().Pick(lb.Flow{})
	require.True(t, ok)
	require.Equal(t, "standby", pk.Member().Name())
	pk.Done(lb.OutcomeOK)
	g.health["a"].Set(healthcheck.StatusPassing)
	require.Equal(t, []string{"a"}, liveNames(c))
	require.Zero(t, onBackup())

	// a pool that no longer has a backup no longer reports on one
	series := func() int { return testutil.CollectAndCount(metrics.ALBPoolOnBackup) }
	before := series()
	c.poolMtx.Lock()
	c.staticTargets = c.staticTargets[:1]
	c.swapPool(c.staticTargets)
	c.poolMtx.Unlock()
	require.Equal(t, before-1, series())
}

func TestHealthPropagatesOnlyWhenAsked(t *testing.T) {
	g := newGraph(t, "a1", "a2", "b1")
	inner := g.alb(t, "inner-a", &ao.Options{
		MechanismName: "rr", Pool: ao.Members("a1", "a2"), PropagateHealth: true,
	})
	silent := g.alb(t, "inner-b", &ao.Options{MechanismName: "rr", Pool: ao.Members("b1")})
	outer := g.alb(t, "outer", &ao.Options{
		MechanismName: "rr", Pool: ao.Members("inner-a", "inner-b"), PropagateHealth: true,
	})
	top := g.alb(t, "top", &ao.Options{MechanismName: "rr", Pool: ao.Members("outer")})
	g.start(t)
	require.Nil(t, silent.HealthStatus())
	require.Equal(t, healthcheck.StatusPassing, inner.HealthStatus().Get())
	require.Equal(t, []string{"inner-a", "inner-b"}, liveNames(outer))

	g.health["a1"].Set(healthcheck.StatusFailing)
	require.Equal(t, []string{"inner-a", "inner-b"}, liveNames(outer), "one live member is enough")
	g.health["a2"].Set(healthcheck.StatusFailing)
	require.Equal(t, healthcheck.StatusFailing, inner.HealthStatus().Get())
	require.Equal(t, []string{"inner-b"}, liveNames(outer), "an empty pool sheds its share")

	// a load balancer that does not propagate keeps its share and fails it
	g.health["b1"].Set(healthcheck.StatusFailing)
	require.Equal(t, []string{"inner-b"}, liveNames(outer))
	require.Equal(t, healthcheck.StatusPassing, outer.HealthStatus().Get())
	require.Equal(t, []string{"outer"}, liveNames(top))

	g.health["a2"].Set(healthcheck.StatusPassing)
	require.Equal(t, []string{"inner-a", "inner-b"}, liveNames(outer))
}

func TestHealthPropagatesThroughEveryLevel(t *testing.T) {
	g := newGraph(t, "leaf", "other")
	g.alb(t, "low", &ao.Options{MechanismName: "rr", Pool: ao.Members("leaf"), PropagateHealth: true})
	mid := g.alb(t, "mid", &ao.Options{MechanismName: "rr", Pool: ao.Members("low"), PropagateHealth: true})
	top := g.alb(t, "top", &ao.Options{MechanismName: "rr", Pool: ao.Members("mid", "other")})
	g.start(t)
	require.Equal(t, []string{"mid", "other"}, liveNames(top))
	g.health["leaf"].Set(healthcheck.StatusFailing)
	require.Equal(t, healthcheck.StatusFailing, mid.HealthStatus().Get())
	require.Equal(t, []string{"other"}, liveNames(top))
	g.health["leaf"].Set(healthcheck.StatusPassing)
	require.Equal(t, []string{"mid", "other"}, liveNames(top))
}

func TestSnapshotsOfASupersededPoolAreIgnored(t *testing.T) {
	g := newGraph(t, "a")
	c := g.alb(t, "stale-alb", &ao.Options{MechanismName: "rr", Pool: ao.Members("a"), PropagateHealth: true})
	g.start(t)
	require.Equal(t, healthcheck.StatusPassing, c.HealthStatus().Get())
	stale := poolObserver{c: c, id: c.poolID - 1}
	stale.Observe(lb.Event{Kind: lb.EventSnapshot, Gen: 99, Eligible: 0})
	require.Equal(t, healthcheck.StatusPassing, c.HealthStatus().Get())
	// nor is a snapshot of the current pool that is observed after a newer one
	late := poolObserver{c: c, id: c.poolID}
	late.Observe(lb.Event{Kind: lb.EventSnapshot, Gen: c.lastGen, Eligible: 0})
	late.Observe(lb.Event{Kind: lb.EventPanic})
	require.Equal(t, healthcheck.StatusPassing, c.HealthStatus().Get())
}

type externalRegistrar interface {
	RegisterExternal(name, description string, s *healthcheck.Status)
}

// the daemon registers every ALB with the health checker before it starts the pools; the status
// an ALB keeps for itself must win over the synthetic one, and be the one the checker reports
func TestHealthPropagatesThroughTheHealthChecker(t *testing.T) {
	g := newGraph(t, "a1", "b1")
	inner := g.alb(t, "inner-a", &ao.Options{MechanismName: "rr", Pool: ao.Members("a1"), PropagateHealth: true})
	g.alb(t, "inner-b", &ao.Options{MechanismName: "rr", Pool: ao.Members("b1")})
	outer := g.alb(t, "outer", &ao.Options{MechanismName: "rr", Pool: ao.Members("inner-a", "inner-b")})
	for _, c := range g.clients {
		if c.Configuration().Provider == "" {
			c.Configuration().Provider = providers.ReverseProxyShort
		}
	}
	hc, err := g.clients.StartHealthChecks(nil)
	require.NoError(t, err)
	t.Cleanup(hc.Shutdown)
	for name, st := range g.health {
		hc.(externalRegistrar).RegisterExternal(name, "test", st)
	}
	statuses := hc.Statuses()
	require.Same(t, inner.HealthStatus(), statuses["inner-a"], "the checker reports the ALB's own status")
	require.NotNil(t, statuses["inner-b"], "an ALB with no status of its own is still reported")
	require.NoError(t, StartALBPools(g.clients, statuses))
	t.Cleanup(func() { _ = StopPools(g.clients) })

	require.Equal(t, []string{"inner-a", "inner-b"}, liveNames(outer))
	g.health["a1"].Set(healthcheck.StatusFailing)
	require.Equal(t, healthcheck.StatusFailing, statuses["inner-a"].Get())
	require.Equal(t, []string{"inner-b"}, liveNames(outer))
	g.health["b1"].Set(healthcheck.StatusFailing)
	require.Equal(t, []string{"inner-b"}, liveNames(outer), "an ALB that does not propagate keeps its share")
	g.health["a1"].Set(healthcheck.StatusPassing)
	require.Equal(t, []string{"inner-a", "inner-b"}, liveNames(outer))
}
