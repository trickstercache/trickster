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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	uropt "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/secret"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"

	"github.com/stretchr/testify/require"
)

var stickySecret = secret.Secret(strings.Repeat("s", secret.MinKeyBytes))

// namedGraph is a graph whose origins answer with their names
func namedGraph(t *testing.T, origins ...string) *graph {
	t.Helper()
	g := &graph{clients: backends.Backends{}, health: healthcheck.StatusLookup{}}
	for _, name := range origins {
		o := bo.New()
		o.Name = name
		b, err := backends.New(name, o, nil, albpool.NamedHandler(name), nil)
		require.NoError(t, err)
		g.clients[name] = b
		g.health[name] = healthcheck.NewStatus(name, "", "", healthcheck.StatusPassing, time.Time{}, nil)
	}
	return g
}

// routedALB is alb with a router that reaches its mechanism, as the ALB's own routes do when it
// is a member of another ALB's pool
func (g *graph) routedALB(t *testing.T, name string, o *ao.Options) *Client {
	t.Helper()
	var c *Client
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.handler.ServeHTTP(w, r) })
	opts := bo.New()
	opts.Name, opts.Provider, opts.ALBOptions = name, providers.ALB, o
	require.NoError(t, o.Initialize(name))
	cl, err := NewClient(name, opts, router, nil, nil, nil)
	require.NoError(t, err)
	c = cl.(*Client)
	g.clients[name] = c
	return c
}

func stickyOpts(pool ao.PoolMemberList) *ao.Options {
	return &ao.Options{MechanismName: "rr", Pool: pool, Sticky: &so.Options{Secret: stickySecret}}
}

// session sends requests to an ALB with the cookie it last issued
type session struct {
	h      http.Handler
	cookie string
}

func (s *session) get(t *testing.T) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	if s.cookie != "" {
		r.Header.Set("Cookie", s.cookie)
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	if line := w.Header().Get("Set-Cookie"); line != "" {
		s.cookie, _, _ = strings.Cut(line, ";")
	}
	return w.Body.String()
}

func TestStickySessionsCarryIntoPooledALBs(t *testing.T) {
	g := namedGraph(t, "e1", "e2", "w1", "w2")
	g.routedALB(t, "east", &ao.Options{MechanismName: "rr", Pool: ao.Members("e1", "e2")})
	g.routedALB(t, "west", &ao.Options{MechanismName: "p2c", Pool: ao.Members("w1", "w2")})
	outer := g.routedALB(t, "regions", stickyOpts(ao.Members("east", "west")))
	g.start(t)
	s := &session{h: outer.Handlers()[providers.ALB]}
	leaf := s.get(t)
	// both levels would rotate on their own: the outer ALB's session keeps the whole path
	for range 8 {
		require.Equal(t, leaf, s.get(t))
	}
}

func TestDrainingMemberKeepsItsSessions(t *testing.T) {
	build := func(drainB bool) *Client {
		g := namedGraph(t, "a", "b")
		c := g.routedALB(t, "drain-alb", stickyOpts(ao.PoolMemberList{{Name: "a"}, {Name: "b", Drain: drainB}}))
		g.start(t)
		return c
	}
	before := build(false).Handlers()[providers.ALB]
	var onB *session
	for range 4 {
		if s := (&session{h: before}); s.get(t) == "b" {
			onB = s
			break
		}
	}
	require.NotNil(t, onB, "no session landed on b")
	// once b drains, its session still reaches it, and no new session does
	after := build(true).Handlers()[providers.ALB]
	onB.h = after
	for range 4 {
		require.Equal(t, "b", onB.get(t))
	}
	for range 8 {
		require.Equal(t, "a", (&session{h: after}).get(t))
	}
}

func TestDrainingPoolNames(t *testing.T) {
	g := namedGraph(t, "a", "b", "c")
	c := g.routedALB(t, "drain-names", &ao.Options{
		MechanismName: "rr",
		Pool:          ao.PoolMemberList{{Name: "c", Drain: true}, {Name: "a"}, {Name: "b", Drain: true}},
	})
	plain := g.routedALB(t, "drain-none", &ao.Options{MechanismName: "rr", Pool: ao.Members("a")})
	g.start(t)
	require.NotNil(t, c.CorePool())
	require.Equal(t, []string{"b", "c"}, c.DrainingPoolNames())
	require.Nil(t, plain.DrainingPoolNames())

	// a mechanism that dispatches without a pool has no draining members
	o := bo.New()
	o.ALBOptions = ao.New()
	o.ALBOptions.MechanismName = names.MechanismUR
	o.ALBOptions.UserRouter = &uropt.Options{DefaultBackend: "a"}
	ur, err := NewClient("drain-ur", o, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, ur.(*Client).CorePool())
	require.Nil(t, ur.(*Client).DrainingPoolNames())
}

func TestStickyTablesAreForgottenWithTheirALB(t *testing.T) {
	g := namedGraph(t, "a")
	o := &ao.Options{MechanismName: "rr", Pool: ao.Members("a"), Sticky: &so.Options{Mode: so.ModeTable}}
	g.routedALB(t, "table-alb", o)
	g.start(t)
	kept := sticky.TableFor("table-alb", o.Sticky)
	require.Same(t, kept, sticky.TableFor("table-alb", o.Sticky), "a running ALB's table was not kept")
	// a reload whose config lacks the ALB, or its sticky block, drops the table
	g2 := namedGraph(t, "a")
	g2.routedALB(t, "table-alb", &ao.Options{MechanismName: "rr", Pool: ao.Members("a")})
	g2.start(t)
	require.NotSame(t, kept, sticky.TableFor("table-alb", o.Sticky))
	ForgetUnusedStickyTables(backends.Backends{})
}

// a table is kept only while its ALB keeps sessions in it: one that switches to tokens and back
// starts over, as its old pins may name members its clients have since left
func TestStickyTableIsNotKeptThroughAnotherMode(t *testing.T) {
	t.Cleanup(func() { ForgetUnusedStickyTables(backends.Backends{}) })
	build := func(mode string) *Client {
		g := namedGraph(t, "a", "b")
		c := g.routedALB(t, "modes-alb", &ao.Options{
			MechanismName: "rr", Pool: ao.Members("a", "b"),
			Sticky: &so.Options{Mode: mode, Secret: stickySecret},
		})
		g.start(t)
		return c
	}
	tableOf := func(c *Client) *sticky.Table {
		return c.handler.(types.PickerMechanism).StickyTable()
	}
	for name, between := range map[string]string{"cookie": so.ModeCookie, "header": so.ModeHeader, "default": ""} {
		t.Run("table then "+name, func(t *testing.T) {
			first := build(so.ModeTable)
			(&session{h: first.Handlers()[providers.ALB]}).get(t)
			require.Equal(t, 1, tableOf(first).Len())
			require.Nil(t, tableOf(build(between)), "the http mode in between keeps no table")
			again := tableOf(build(so.ModeTable))
			require.NotSame(t, tableOf(first), again)
			require.Zero(t, again.Len(), "pins from before the mode change came back")
		})
	}
	// an unchanged table block still carries its pins across a reload
	first := build(so.ModeTable)
	(&session{h: first.Handlers()[providers.ALB]}).get(t)
	require.Same(t, tableOf(first), tableOf(build(so.ModeTable)))
}

// an ALB relays a protocol upgrade to the one backend it sends a request to, which tunnels it; a
// fanout, which sends a request to several, relays none
func TestALBRelaysUpgrades(t *testing.T) {
	g := namedGraph(t, "a")
	for mechanism, want := range map[string]bool{"rr": true, "lt": true, "hrw": true, "fr": false, "nlm": false} {
		o := &ao.Options{MechanismName: mechanism, Pool: ao.Members("a")}
		require.Equal(t, want, g.routedALB(t, "relay-"+mechanism, o).RelaysUpgrades(), mechanism)
	}
	ur, err := NewClient("relay-ur", &bo.Options{Provider: providers.ALB, ALBOptions: &ao.Options{
		MechanismName: "ur", UserRouter: &uropt.Options{DefaultBackend: "a"},
	}}, nil, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, ur.(*Client).RelaysUpgrades(), "the user router sends each request to one backend")
}

// an ALB takes the table for its stream and native flows when a listener first asks for it, and a
// reload keeps that table only while a listener asks for it again before unused tables are forgotten
func TestStickyFlowsTableIsKeptWhileAListenerUsesIt(t *testing.T) {
	t.Cleanup(func() { ForgetUnusedStickyTables(backends.Backends{}) })
	reload := func(listenerAsks bool) *Client {
		g := namedGraph(t, "a", "b")
		c := g.routedALB(t, "flows-alb", &ao.Options{
			MechanismName: "rr", Pool: ao.Members("a", "b"),
			Sticky: &so.Options{Secret: stickySecret},
		})
		require.NoError(t, StartALBPools(g.clients, g.health))
		t.Cleanup(func() { _ = StopPools(g.clients) })
		if listenerAsks {
			require.NotNil(t, c.StickyFlows())
		}
		ForgetUnusedStickyTables(g.clients)
		return c
	}
	httpOnly := reload(false)
	require.Nil(t, httpOnly.flows.Load(), "an ALB no stream or native listener serves took a table")
	c := reload(true)
	flows := c.StickyFlows()
	require.Same(t, flows, c.StickyFlows(), "an ALB made its flows' persistence twice")
	flows.Table().Put(1, sticky.Path{Depth: 1}, time.Now().UnixNano())
	// a listener that asks again after a reload finds the pins; one that does not, loses them
	require.Same(t, flows.Table(), reload(true).StickyFlows().Table())
	reload(false)
	again := reload(true).StickyFlows().Table()
	require.NotSame(t, flows.Table(), again)
	require.Zero(t, again.Len())
}

// a table-mode ALB keeps its requests and its flows in one table, which a reload carries whole
func TestStickyTableIsSharedByEveryPlane(t *testing.T) {
	t.Cleanup(func() { ForgetUnusedStickyTables(backends.Backends{}) })
	g := namedGraph(t, "a")
	c := g.routedALB(t, "planes-alb", &ao.Options{
		MechanismName: "rr", Pool: ao.Members("a"),
		Sticky: &so.Options{Mode: so.ModeTable},
	})
	g.start(t)
	require.Same(t, c.handler.(types.PickerMechanism).StickyTable(), c.StickyFlows().Table())
}

func TestStickyFlowsNeedATable(t *testing.T) {
	g := namedGraph(t, "a")
	for name, o := range map[string]*ao.Options{
		"flows-none":   {MechanismName: "rr", Pool: ao.Members("a")},
		"flows-cookie": {MechanismName: "rr", Pool: ao.Members("a"), Sticky: &so.Options{Mode: so.ModeCookie}},
		"flows-fanout": {MechanismName: "fr", Pool: ao.Members("a")},
	} {
		require.Nil(t, g.routedALB(t, name, o).StickyFlows(), name)
	}
	ur, err := NewClient("flows-ur", &bo.Options{Provider: providers.ALB, ALBOptions: &ao.Options{
		MechanismName: "ur", UserRouter: &uropt.Options{DefaultBackend: "a"},
	}}, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, ur.(*Client).StickyFlows())
}
