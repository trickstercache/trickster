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

package native_test

import (
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/native"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/stretchr/testify/require"
)

type fixture struct {
	clients backends.Backends
	health  healthcheck.StatusLookup
}

func replicas(t *testing.T, names ...string) *fixture {
	t.Helper()
	f := &fixture{clients: backends.Backends{}, health: healthcheck.StatusLookup{}}
	for _, name := range names {
		o := bo.New()
		o.Name = name
		b, err := backends.New(name, o, nil, http.NotFoundHandler(), nil)
		require.NoError(t, err)
		f.clients[name] = b
		f.health[name] = healthcheck.NewStatus(name, "", "", healthcheck.StatusPassing, time.Time{}, nil)
	}
	return f
}

func (f *fixture) alb(t *testing.T, name string, o *ao.Options) *alb.Client {
	t.Helper()
	b := bo.New()
	b.Name = name
	b.Provider = providers.ALB
	b.ALBOptions = o
	require.NoError(t, o.Initialize(name))
	cl, err := alb.NewClient(name, b, nil, nil, nil, nil)
	require.NoError(t, err)
	f.clients[name] = cl
	return cl.(*alb.Client)
}

func (f *fixture) start(t *testing.T) {
	t.Helper()
	require.NoError(t, alb.StartALBPools(f.clients, f.health))
	t.Cleanup(func() { _ = alb.StopPools(f.clients) })
}

func inflight(c *alb.Client) map[string]int64 {
	out := map[string]int64{}
	for _, tgt := range c.Pool().ConfiguredTargets() {
		out[tgt.Name()] = tgt.Member().Stats().Inflight()
	}
	return out
}

func TestSessionsAreBalancedAndCounted(t *testing.T) {
	f := replicas(t, "r1", "r2")
	c := f.alb(t, "replicas", &ao.Options{MechanismName: "lc", Pool: ao.Members("r1", "r2")})
	f.start(t)
	r := c.RouteResolver()
	require.NotNil(t, r)

	var held []backends.RouteDecision
	for range 4 {
		d, ok := r.ResolveRoute(backends.RouteInput{Username: "app", Authenticated: true})
		require.True(t, ok)
		require.Equal(t, backends.RouteOutcomeSelected, d.Outcome)
		require.True(t, d.Target.Available())
		held = append(held, d)
	}
	// least connections: open sessions are shared out evenly, and stay counted while open
	require.Equal(t, map[string]int64{"r1": 2, "r2": 2}, inflight(c))
	for _, d := range held {
		d.Release()
		d.Release()
	}
	require.Equal(t, map[string]int64{"r1": 0, "r2": 0}, inflight(c), "a session is released once")

	// a replica that is down takes no sessions, and a pool with none left refuses them
	f.health["r1"].Set(healthcheck.StatusFailing)
	for range 3 {
		d, ok := r.ResolveRoute(backends.RouteInput{})
		require.True(t, ok)
		require.Equal(t, "r2", d.Target.Backend.Name())
		d.Release()
	}
	f.health["r2"].Set(healthcheck.StatusFailing)
	d, ok := r.ResolveRoute(backends.RouteInput{})
	require.False(t, ok)
	require.Equal(t, backends.RouteOutcomeUnavailable, d.Outcome)
	require.Nil(t, d.Release)
}

func TestSessionsKeepToAMemberByUserOrAddress(t *testing.T) {
	names := []string{"r1", "r2", "r3", "r4", "r5"}
	f := replicas(t, names...)
	byUser := f.alb(t, "by-user", &ao.Options{
		MechanismName: "hrw", Pool: ao.Members(names...), HRW: ao.HRWOptions{Key: "user"},
	})
	byAddr := f.alb(t, "by-addr", &ao.Options{MechanismName: "hrw", Pool: ao.Members(names...)})
	f.start(t)
	owner := func(c *alb.Client, in backends.RouteInput) string {
		d, ok := c.RouteResolver().ResolveRoute(in)
		require.True(t, ok)
		d.Release()
		return d.Target.Backend.Name()
	}
	here, there := netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("::ffff:203.0.113.9")
	users, addrs := map[string]bool{}, map[string]bool{}
	for i := range 30 {
		user := "tenant" + strconv.Itoa(i)
		first := owner(byUser, backends.RouteInput{Username: user, Client: here})
		users[first] = true
		require.Equal(t, first, owner(byUser, backends.RouteInput{Username: user, Client: there}), user)

		addr := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})
		first = owner(byAddr, backends.RouteInput{Username: "a", Client: addr})
		addrs[first] = true
		require.Equal(t, first, owner(byAddr, backends.RouteInput{Username: "b", Client: netip.AddrFrom16(addr.As16())}))
	}
	require.GreaterOrEqual(t, len(users), 4)
	require.GreaterOrEqual(t, len(addrs), 4)
	// a session with nothing to key on is served all the same
	spread := map[string]bool{}
	for range 60 {
		spread[owner(byUser, backends.RouteInput{})] = true
		spread[owner(byAddr, backends.RouteInput{})] = true
	}
	require.GreaterOrEqual(t, len(spread), 3)
}

func TestNestedLoadBalancersKeyForThemselves(t *testing.T) {
	f := replicas(t, "a1", "a2", "a3", "b1")
	f.alb(t, "inner-a", &ao.Options{
		MechanismName: "hrw", Pool: ao.Members("a1", "a2", "a3"), HRW: ao.HRWOptions{Key: "user"},
	})
	f.alb(t, "inner-b", &ao.Options{MechanismName: "rr", Pool: ao.Members("b1")})
	outer := f.alb(t, "outer", &ao.Options{MechanismName: "rr", Pool: ao.Members("inner-a", "inner-b")})
	f.start(t)
	seen := map[string]bool{}
	for range 20 {
		d, ok := outer.RouteResolver().ResolveRoute(backends.RouteInput{Username: "app"})
		require.True(t, ok)
		seen[d.Target.Backend.Name()] = true
		d.Release()
	}
	require.Len(t, seen, 2, "one user keeps to one member of the keyed pool: %v", seen)
	require.True(t, seen["b1"])
}

func TestOnlySessionStrategiesResolveRoutes(t *testing.T) {
	f := replicas(t, "r1")
	fanout := f.alb(t, "fanout", &ao.Options{MechanismName: "fr", Pool: ao.Members("r1")})
	timed := f.alb(t, "timed", &ao.Options{MechanismName: "lt", Pool: ao.Members("r1")})
	f.start(t)
	require.Nil(t, fanout.RouteResolver())
	require.Nil(t, timed.RouteResolver(), "a native session reports no latency to rank members by")
	require.Nil(t, native.Resolver(nil, nil))
}

// a member whose payload is not a backend cannot take a session, and is not blamed for it
func TestForeignMembersAreRefused(t *testing.T) {
	m := lb.NewMember(lb.MemberOptions{Name: "foreign", Value: 7})
	p, err := lb.NewPool([]*lb.Member{m}, 0)
	require.NoError(t, err)
	defer p.Stop()
	b := lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: p, Ejection: lb.EjectionOptions{Failures: 1}})
	d, ok := native.Resolver(b, nil).ResolveRoute(backends.RouteInput{})
	require.False(t, ok)
	require.Equal(t, backends.RouteOutcomeUnavailable, d.Outcome)
	require.Zero(t, m.Stats().Inflight())
	require.Zero(t, m.Stats().Failures())
}

// the load balancer's healthy_floor alone decides which members take sessions, as it does for
// requests and connections: a route the pool admits is never refused for its status afterwards
func TestSessionsHonorTheHealthyFloor(t *testing.T) {
	statuses := map[string]int32{
		"failing": healthcheck.StatusFailing, "unchecked": healthcheck.StatusUnchecked, "passing": healthcheck.StatusPassing,
	}
	for floor, want := range map[int][]string{
		-1: {"failing", "passing", "unchecked"},
		0:  {"passing", "unchecked"},
		1:  {"passing"},
	} {
		f := replicas(t, "failing", "unchecked", "passing")
		for name, status := range statuses {
			f.health[name].Set(status)
			// probed, so a floor of 1 is not reset for members that could never reach it
			f.clients[name].Configuration().HealthCheck = &ho.Options{Interval: timeconv.Duration(time.Second)}
		}
		c := f.alb(t, "floor"+strconv.Itoa(floor+1), &ao.Options{
			MechanismName: "rr", HealthyFloor: floor, Pool: ao.Members("failing", "unchecked", "passing"),
		})
		f.start(t)
		reached := map[string]bool{}
		for range 12 {
			d, ok := c.RouteResolver().ResolveRoute(backends.RouteInput{})
			require.True(t, ok, "floor %d", floor)
			require.True(t, d.Target.Available(), "floor %d refused %s after selecting it", floor, d.Target.Backend.Name())
			reached[d.Target.Backend.Name()] = true
			d.Release()
		}
		got := make([]string, 0, len(reached))
		for name := range reached {
			got = append(got, name)
		}
		require.ElementsMatch(t, want, got, "floor %d", floor)
	}
}
