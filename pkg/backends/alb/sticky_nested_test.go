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
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"

	"github.com/stretchr/testify/require"
)

func TestStickySessionLeavesAPooledALBWithNothingLeft(t *testing.T) {
	// the configured ALBs move a session off a pooled ALB whose members are all down, and keep it
	// where it moved once they return
	g := namedGraph(t, "e1", "e2", "w1", "w2")
	g.routedALB(t, "east", &ao.Options{MechanismName: "rr", Pool: ao.Members("e1", "e2")})
	g.routedALB(t, "west", &ao.Options{MechanismName: "lc", Pool: ao.Members("w1", "w2")})
	outer := g.routedALB(t, "regions", stickyOpts(ao.Members("east", "west")))
	g.start(t)
	s := &session{h: outer.Handlers()[providers.ALB]}
	leaf := s.get(t)
	region := leaf[:1]
	setRegion := func(status int32) {
		for _, name := range []string{region + "1", region + "2"} {
			g.health[name].Set(status)
		}
	}
	setRegion(healthcheck.StatusFailing)
	moved := s.get(t)
	require.NotEqual(t, region, moved[:1], "the session stayed with the ALB that has no member left")
	setRegion(healthcheck.StatusPassing)
	for range 4 {
		require.Equal(t, moved, s.get(t))
	}
}
