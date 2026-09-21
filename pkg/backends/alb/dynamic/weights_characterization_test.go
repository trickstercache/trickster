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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/discovery"

	"github.com/stretchr/testify/require"
)

func weightedMember(name, addr string, weight int) discovery.Member {
	m := member(name, addr)
	m.Weight = weight
	return m
}

func poolTargets(t *testing.T, c *alb.Client) map[string]*pool.Target {
	t.Helper()
	p := c.Pool()
	require.NotNil(t, p)
	out := make(map[string]*pool.Target)
	for _, tgt := range p.ConfiguredTargets() {
		out[tgt.Name()] = tgt
	}
	return out
}

// a discovered member's weight reaches its pool target unchanged, and a live weight change
// re-weights that member in place: same backend, same health status, siblings untouched
func TestDiscoveredWeightsReachThePool(t *testing.T) {
	m, c, _ := newTestManager(t, &ao.DiscoveryOptions{
		DiscovererName: "d", TemplateBackend: "rp-template",
	})
	m.ApplySnapshot(discovery.Snapshot{
		weightedMember("m1", "10.0.0.1:8080", 0),
		weightedMember("m2", "10.0.0.2:8080", 3),
		weightedMember("m3", "10.0.0.3:8080", 5),
	})
	before := poolTargets(t, c)
	require.Len(t, before, 3)
	require.Equal(t, 1, before["myalb-m1"].Weight(), "an unset weight is 1")
	require.Equal(t, 3, before["myalb-m2"].Weight())
	require.Equal(t, 5, before["myalb-m3"].Weight())

	m.ApplySnapshot(discovery.Snapshot{
		weightedMember("m1", "10.0.0.1:8080", 0),
		weightedMember("m2", "10.0.0.2:8080", 7),
		weightedMember("m3", "10.0.0.3:8080", 5),
	})
	after := poolTargets(t, c)
	require.Len(t, after, 3)
	require.Equal(t, 7, after["myalb-m2"].Weight())
	require.Same(t, before["myalb-m2"].Backend(), after["myalb-m2"].Backend(),
		"a weight change must not rebuild the member's backend")
	require.Same(t, before["myalb-m2"].HealthStatus(), after["myalb-m2"].HealthStatus(),
		"a weight change must not reset the member's health")
	require.Same(t, before["myalb-m2"].Member().Stats(), after["myalb-m2"].Member().Stats(),
		"a weight change must not reset the member's runtime stats")
	require.Equal(t, 7, after["myalb-m2"].Member().Weight())
	require.Same(t, before["myalb-m1"], after["myalb-m1"], "an unchanged member keeps its target")
	require.Same(t, before["myalb-m3"], after["myalb-m3"], "an unchanged member keeps its target")
}
