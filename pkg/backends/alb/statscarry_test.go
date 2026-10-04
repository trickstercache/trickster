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

package alb

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/lb"

	"github.com/stretchr/testify/require"
)

// generation builds one config generation: an ALB over members a and b, started
func generation(t *testing.T, albName, mechanism string, members ...string) (*Client, backends.Backends) {
	t.Helper()
	o := bo.New()
	o.Provider = providers.ALB
	o.ALBOptions = &ao.Options{MechanismName: mechanism, Pool: ao.Members(members...)}
	require.NoError(t, o.Initialize(albName))
	cl, err := NewClient(albName, o, nil, nil, nil, nil)
	require.NoError(t, err)
	clients := backends.Backends{albName: cl}
	for _, name := range members {
		mo := bo.New()
		mo.Name = name
		b, err := backends.New(name, mo, nil, http.NotFoundHandler(), nil)
		require.NoError(t, err)
		clients[name] = b
	}
	require.NoError(t, StartALBPools(clients, nil))
	return cl.(*Client), clients
}

func memberStats(c *Client) map[string]*lb.Stats {
	out := make(map[string]*lb.Stats)
	for _, tgt := range c.Pool().ConfiguredTargets() {
		out[tgt.Name()] = tgt.Member().Stats()
	}
	return out
}

// a reload rebuilds every ALB; a mechanism that ranks members by what it has learned of them
// must not start over each time
func TestStatsSurviveAReload(t *testing.T) {
	first, _ := generation(t, "carry-alb", "lt", "a", "b")
	before := memberStats(first)
	first.StopPool()

	second, _ := generation(t, "carry-alb", "lt", "b", "c")
	defer second.StopPool()
	after := memberStats(second)
	require.Same(t, before["b"], after["b"], "a member kept across the reload keeps its stats")
	require.NotNil(t, after["c"])
	require.NotSame(t, before["a"], after["c"])

	// a member dropped in one generation comes back fresh in a later one
	second.StopPool()
	third, _ := generation(t, "carry-alb", "lt", "a", "b")
	defer third.StopPool()
	require.NotSame(t, before["a"], memberStats(third)["a"])
	require.Same(t, before["b"], memberStats(third)["b"])
}

func TestStatsAreNotCarriedWhereTheyAreNotKept(t *testing.T) {
	first, _ := generation(t, "untracked-carry-alb", "rr", "a")
	before := memberStats(first)
	first.StopPool()
	second, _ := generation(t, "untracked-carry-alb", "rr", "a")
	defer second.StopPool()
	require.NotSame(t, before["a"], memberStats(second)["a"], "round robin keeps no stats to carry")
	require.Nil(t, carryStats("untracked-carry-alb", "a"))
}

// an ALB that leaves the config takes what was carried for it
func TestCarriedStatsAreForgottenWithTheirALB(t *testing.T) {
	gone, _ := generation(t, "departing-alb", "lc", "a")
	gone.StopPool()
	require.NotNil(t, carryStats("departing-alb", "a"))
	staying, _ := generation(t, "staying-alb", "lc", "a")
	defer staying.StopPool()
	require.Nil(t, carryStats("departing-alb", "a"))
	require.NotNil(t, carryStats("staying-alb", "a"))

	rememberStats("staying-alb", nil)
	require.Nil(t, carryStats("staying-alb", "a"))
}
