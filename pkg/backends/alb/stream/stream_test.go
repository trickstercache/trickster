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
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

func TestFromBackend(t *testing.T) {
	if FromBackend(nil) != nil {
		t.Error("nil backend yields an upstream")
	}
	if FromBackend(origin(t, "hostless", "")) != nil {
		t.Error("a backend without an origin host yields an upstream")
	}
	r, ok := FromBackend(origin(t, "o", "10.0.0.1:9000")).Pick(l4.Flow{})
	if !ok || r.Addr() != "10.0.0.1:9000" || !r.Final() {
		t.Errorf("origin backend = %v, %v", r, ok)
	}
	if _, ok := FromBackend(origin(t, "gone", "unresolved.kgw.invalid:1")).Pick(l4.Flow{}); ok {
		t.Error("a backend under .invalid dials")
	}
	// a load balancer that does not commit a flow to one member has nothing to relay to
	if FromBackend(newALB(t, "fanout", "fr", up(origin(t, "a", "10.0.0.1:1"), 1))) != nil {
		t.Error("a fanout load balancer yields an upstream")
	}
	pooled := FromBackend(newALB(t, "alb", "rr", up(origin(t, "a", "10.0.0.1:1"), 1)))
	r, ok = pooled.Pick(l4.Flow{})
	if !ok || r.Addr() != "10.0.0.1:1" || r.Final() {
		t.Errorf("pooled route = %v, %v; a pool's route is not final", r, ok)
	}
}

func TestEveryWindowIsExact(t *testing.T) {
	a := origin(t, "a", "10.0.0.1:1")
	b := origin(t, "b", "10.0.0.2:1")
	c := origin(t, "c", "10.0.0.3:1")
	gone := origin(t, "gone", "unresolved.kgw.invalid:1")
	hostless := origin(t, "hostless", "")
	failing := origin(t, "failing", "10.0.0.9:1")

	t.Run("uniform", func(t *testing.T) {
		u := FromBackend(newALB(t, "alb", "rr", up(a, 1), up(b, 1), up(c, 1)))
		assertEveryWindowExact(t, pickN(u, 20), map[string]int{"10.0.0.1:1": 1, "10.0.0.2:1": 1, "10.0.0.3:1": 1})
	})
	t.Run("weighted", func(t *testing.T) {
		u := FromBackend(newALB(t, "alb", "rr", up(a, 1), up(b, 3), up(c, 2)))
		assertEveryWindowExact(t, pickN(u, 40), map[string]int{"10.0.0.1:1": 1, "10.0.0.2:1": 3, "10.0.0.3:1": 2})
	})
	t.Run("an unhealthy member holds no share", func(t *testing.T) {
		u := FromBackend(newALB(t, "alb", "rr", up(a, 2), down(failing, 5), up(b, 1)))
		assertEveryWindowExact(t, pickN(u, 20), map[string]int{"10.0.0.1:1": 2, "10.0.0.2:1": 1})
	})
	t.Run("a refusing member consumes and refuses exactly its weight", func(t *testing.T) {
		u := FromBackend(newALB(t, "alb", "rr", up(a, 1), up(gone, 3)))
		assertEveryWindowExact(t, pickN(u, 24), map[string]int{"10.0.0.1:1": 1, refusedKey: 3})
	})
	t.Run("a member with nothing to dial refuses its own share", func(t *testing.T) {
		u := FromBackend(newALB(t, "alb", "rr", up(a, 1), up(hostless, 1)))
		assertEveryWindowExact(t, pickN(u, 12), map[string]int{"10.0.0.1:1": 1, refusedKey: 1})
	})
}

func TestRefusesWithoutALiveMember(t *testing.T) {
	a := origin(t, "a", "10.0.0.1:1")
	b := origin(t, "b", "10.0.0.2:1")
	for name, u := range map[string]l4.Upstream{
		"all unhealthy": FromBackend(newALB(t, "down", "rr", down(a, 1), down(b, 2))),
		"empty":         FromBackend(newALB(t, "empty", "rr")),
	} {
		for range 6 {
			if r, ok := u.Pick(l4.Flow{}); ok {
				t.Fatalf("%s: dialed %s", name, r.Addr())
			}
		}
	}
	// a member that recovers is dialed by the very next flow
	member := down(a, 1)
	u := FromBackend(newALB(t, "recovering", "rr", member))
	if _, ok := u.Pick(l4.Flow{}); ok {
		t.Fatal("dialed a failing member")
	}
	member.status.Set(healthcheck.StatusPassing)
	if r, ok := u.Pick(l4.Flow{}); !ok || r.Addr() != "10.0.0.1:1" {
		t.Error("a recovered member was not dialed")
	}
}

func TestNestedApportionment(t *testing.T) {
	// outer 3:1 over two inner pools, the first of which is itself weighted 2:1
	newOuter := func(t *testing.T, inner func(backends.Backend, int) spec) l4.Upstream {
		inner1 := newALB(t, "inner1", "rr", inner(origin(t, "a", "10.1.0.1:1"), 2), inner(origin(t, "b", "10.1.0.2:1"), 1))
		inner2 := newALB(t, "inner2", "rr", up(origin(t, "c", "10.2.0.1:1"), 1))
		return FromBackend(newALB(t, "outer", "rr", up(inner1, 3), up(inner2, 1)))
	}
	t.Run("each level apportions by its own weights", func(t *testing.T) {
		// 12 selections hold 9 for inner1, a whole number of its rotations of 3
		got := tally(pickN(newOuter(t, up), 36))
		want := map[string]int{"10.1.0.1:1": 18, "10.1.0.2:1": 9, "10.2.0.1:1": 9}
		if !maps.Equal(got, want) {
			t.Errorf("nested apportionment = %v, want %v", got, want)
		}
	})
	t.Run("an inner pool with no live member refuses its share", func(t *testing.T) {
		assertEveryWindowExact(t, pickN(newOuter(t, down), 16), map[string]int{refusedKey: 3, "10.2.0.1:1": 1})
	})
	t.Run("a pool nested beyond the depth bound is not followed", func(t *testing.T) {
		l2 := newALB(t, "l2", "rr", up(origin(t, "z", "10.9.0.1:1"), 1))
		l1 := newALB(t, "l1", "rr", up(l2, 1))
		if r, ok := FromBackend(newALB(t, "l0", "rr", up(l1, 1))).Pick(l4.Flow{}); ok {
			t.Errorf("a pool three deep was followed: %s", r.Addr())
		}
	})
}

// what the relay reports of a route reaches the member's stats, at every level it passed
func TestRouteFeedbackReachesTheMember(t *testing.T) {
	inner := newALB(t, "inner", "lc", up(origin(t, "a", "10.0.0.1:1"), 1))
	outer := newALB(t, "outer", "lc", up(inner, 1))
	u := FromBackend(outer)
	leaf := inner.(*alb.Client).Pool().ConfiguredTargets()[0].Member().Stats()
	top := outer.(*alb.Client).Pool().ConfiguredTargets()[0].Member().Stats()
	inflight := func() [2]int64 { return [2]int64{top.Inflight(), leaf.Inflight()} }

	r, ok := u.Pick(l4.Flow{})
	if !ok || inflight() != [2]int64{1, 1} {
		t.Fatalf("after a pick: %v, in flight %v", ok, inflight())
	}
	r.Dialed(3*time.Millisecond, nil)
	r.FirstByte()
	if inflight() != [2]int64{1, 1} {
		t.Errorf("an open connection is no longer in flight: %v", inflight())
	}
	r.Closed(nil)
	if inflight() != [2]int64{0, 0} || leaf.Failures() != 0 {
		t.Errorf("after close: in flight %v, %d failures", inflight(), leaf.Failures())
	}

	// a failed dial ends the route and counts against the member, not against the pool above it
	r, _ = u.Pick(l4.Flow{})
	r.Dialed(time.Millisecond, errors.New("connection refused"))
	if inflight() != [2]int64{0, 0} || leaf.Failures() != 1 || top.Failures() != 0 {
		t.Errorf("after a failed dial: in flight %v, failures %d and %d", inflight(), top.Failures(), leaf.Failures())
	}
	// a route the relay gave up before dialing says nothing about the member
	r, _ = u.Pick(l4.Flow{})
	r.Dialed(0, l4.ErrAbandoned)
	if inflight() != [2]int64{0, 0} || leaf.Failures() != 1 {
		t.Errorf("after an abandoned route: in flight %v, %d failures", inflight(), leaf.Failures())
	}
	// nor does a member that must refuse its share
	gone := newALB(t, "refusing", "lc", up(origin(t, "gone", "unresolved.kgw.invalid:1"), 1))
	if _, ok := FromBackend(gone).Pick(l4.Flow{}); ok {
		t.Fatal("a refusing member was dialed")
	}
	st := gone.(*alb.Client).Pool().ConfiguredTargets()[0].Member().Stats()
	if st.Inflight() != 0 || st.Failures() != 0 {
		t.Errorf("a refused share left %d in flight and %d failures", st.Inflight(), st.Failures())
	}
}
