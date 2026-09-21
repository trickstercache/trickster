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

package stream

import (
	"slices"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func addrsOf(routes []l4.Route) []string {
	out := make([]string, len(routes))
	for i, r := range routes {
		out[i] = r.Addr()
	}
	return out
}

func TestRaceConnectsToSeveralMembers(t *testing.T) {
	m := origins(t, 5)
	unroutable := origin(t, "no-address", "")
	sick := down(m[4], 1)
	u := FromBackend(newALB(t, "race-alb", "race", up(m[0], 1), up(unroutable, 1), up(m[1], 1), up(m[2], 1), up(m[3], 1), sick))
	racer, ok := u.(l4.Racer)
	if !ok {
		t.Fatal("a race mechanism does not race")
	}
	f := clientFlow(l4.ProtocolTCP, "198.51.100.1:1000", "")
	// the default width of 4 covers the four members that are up and can be dialed, and each
	// flow starts one member further on
	first, second := addrsOf(racer.Race(f)), addrsOf(racer.Race(f))
	if len(first) != ao.DefaultRaceWidth || slices.Contains(first, "10.0.0.5:9000") {
		t.Fatalf("race = %v", first)
	}
	if first[1] != second[0] {
		t.Errorf("consecutive races start at %s and %s", first[0], second[0])
	}
	if r, ok := u.Pick(f); !ok || r.Addr() != "10.0.0.1:9000" || !r.Final() {
		t.Errorf("pick = %v, %v", r, ok)
	}
	if _, mirrors := u.(l4.Mirrorer); mirrors && len(u.(l4.Mirrorer).Mirror(f, nil)) != 0 {
		t.Error("a race mirrors")
	}

	narrow := func(o *ao.Options) { o.Stream = &ao.StreamOptions{RaceWidth: 2} }
	u = FromBackend(newALBWith(t, "narrow-alb", "connect_race", narrow, up(m[0], 1), up(m[1], 1), up(m[2], 1)))
	if got := u.(l4.Racer).Race(f); len(got) != 2 {
		t.Errorf("a race of width 2 connects to %d members", len(got))
	}

	empty := FromBackend(newALB(t, "empty-alb", "race", down(m[0], 1), up(unroutable, 1)))
	if got := empty.(l4.Racer).Race(f); len(got) != 0 {
		t.Errorf("a pool with nothing to dial races %v", addrsOf(got))
	}
	if _, ok := empty.Pick(f); ok {
		t.Error("a pool with nothing to dial picked a member")
	}
}

func TestMirrorCopiesToEveryOtherMember(t *testing.T) {
	m := origins(t, 4)
	primary := up(m[0], 1)
	u := FromBackend(newALB(t, "mirror-alb", "mirror", primary, up(m[1], 1), down(m[2], 1), up(m[3], 1)))
	f := clientFlow(l4.ProtocolUDP, "198.51.100.1:1000", "")
	r, ok := u.Pick(f)
	if !ok || r.Addr() != "10.0.0.1:9000" {
		t.Fatalf("pick = %v, %v", r, ok)
	}
	if got := addrsOf(u.(l4.Mirrorer).Mirror(f, r)); !slices.Equal(got, []string{"10.0.0.2:9000", "10.0.0.4:9000"}) {
		t.Errorf("mirrors = %v", got)
	}
	// a mirror has one route to offer a connection, so nothing to race
	if got := u.(l4.Racer).Race(f); len(got) != 1 || got[0].Addr() != "10.0.0.1:9000" {
		t.Errorf("race = %v", addrsOf(got))
	}
	// the next member answers while the first is down
	primary.status.Set(healthcheck.StatusFailing)
	if r, ok := u.Pick(f); !ok || r.Addr() != "10.0.0.2:9000" {
		t.Errorf("pick with the first member down = %v, %v", r, ok)
	}
	alone := FromBackend(newALB(t, "alone-alb", "udp_mirror", up(m[0], 1)))
	if got := alone.(l4.Mirrorer).Mirror(f, nil); got != nil {
		t.Errorf("a pool of one mirrors to %v", addrsOf(got))
	}
}

func TestSpreadRoutesCountTheirMember(t *testing.T) {
	m := origins(t, 2)
	u := FromBackend(newALB(t, "counted-alb", "race", up(m[0], 1), up(m[1], 1)))
	f := clientFlow(l4.ProtocolTCP, "198.51.100.1:1000", "")
	f.Listener = "spread-counts"
	count := func(member, result string) float64 {
		return testutil.ToFloat64(metrics.ProxyStreamMemberConnections.WithLabelValues(f.Listener, f.Protocol, member, result))
	}
	active := func(member string) float64 {
		return testutil.ToFloat64(metrics.ProxyStreamMemberActiveConnections.WithLabelValues(f.Listener, f.Protocol, member))
	}
	routes := u.(l4.Racer).Race(f)
	won, lost := routes[0], routes[1]
	won.Dialed(time.Millisecond, nil)
	won.FirstByte()
	lost.Dialed(0, l4.ErrAbandoned)
	if active("m1") != 1 || active("m0") != 0 {
		t.Errorf("active = m0 %v, m1 %v", active("m0"), active("m1"))
	}
	won.Closed(nil)
	if active("m1") != 0 || count("m1", ResultProxied) != 1 || count("m0", ResultDialFailed) != 0 {
		t.Error("a relayed connection was not counted for the member that won it alone")
	}
	failed := u.(l4.Racer).Race(f)[0]
	failed.Dialed(time.Millisecond, errRefused)
	if count("m0", ResultDialFailed) != 1 {
		t.Error("a failed connect was not counted")
	}
	unreachable := u.(l4.Racer).Race(f)[0]
	unreachable.Dialed(time.Millisecond, nil)
	unreachable.Closed(errRefused)
	if count("m1", ResultUnreachable) != 1 {
		t.Error("an unreachable member was not counted")
	}
}

type poolless struct{}

func (poolless) Spread() types.Spread { return types.SpreadRace }

func (poolless) Pool() pool.Pool { return nil }

func TestSpreadBeforeItsPoolStarts(t *testing.T) {
	u := newSpread(poolless{}, nil)
	if _, ok := u.Pick(l4.Flow{}); ok || len(u.Race(l4.Flow{})) != 0 || u.Mirror(l4.Flow{}, nil) != nil {
		t.Error("a load balancer with no pool committed a flow")
	}
	if ao.MaxMirrorMembers != l4.MaxUDPMirrors+1 {
		t.Error("the pool a mirror may have is not what the relay will copy a flow to")
	}
}
