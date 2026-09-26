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

package lb

import (
	"slices"
	"testing"
	"time"
)

func pinTo(m *Member) Flow {
	return Flow{Pin: m.Hash(), HasPin: true}
}

func expectPick(t *testing.T, b *Balancer, f Flow, want *Member, pinned bool) {
	t.Helper()
	pk, ok := b.Pick(f)
	if !ok || pk.Member() != want || pk.Pinned() != pinned {
		t.Fatalf("pick = %v, %v, pinned %v; want %s, pinned %v", pk.Member(), ok, pk.Pinned(), want.Name(), pinned)
	}
	pk.Done(OutcomeOK)
}

func TestPinnedPickFollowsEligibility(t *testing.T) {
	health := newHealth(1)
	a := NewMember(MemberOptions{Name: "a"})
	b := NewMember(MemberOptions{Name: "b", Health: health})
	bal := NewBalancer(headSelector{}, BalancerOptions{Pool: mustPool(t, []*Member{a, b}, 1)})
	expectPick(t, bal, pinTo(b), b, true)
	// the strategy chooses when the pin is not asked for or names no member
	expectPick(t, bal, Flow{Pin: b.Hash()}, a, false)
	expectPick(t, bal, Flow{Pin: b.Hash() ^ 1, HasPin: true}, a, false)
	// and when the pinned member falls below the floor, until it recovers
	health.set(0)
	expectPick(t, bal, pinTo(b), a, false)
	// an unavailable member is still one a pin names; a hash of no member is not
	if p := bal.Pool(); !p.Pinnable(b.Hash()) || p.Pinnable(b.Hash()^1) {
		t.Error("Pinnable does not follow membership alone")
	}
	health.set(1)
	expectPick(t, bal, pinTo(b), b, true)
	if _, ok := NewBalancer(headSelector{}).Pick(pinTo(a)); ok {
		t.Error("a balancer with no pool honored a pin")
	}
}

func TestPinnedPickReachesStandbysAndDrainingMembers(t *testing.T) {
	primary := NewMember(MemberOptions{Name: "primary"})
	standby := NewMember(MemberOptions{Name: "standby", Tier: 1})
	draining := NewMember(MemberOptions{Name: "draining", Draining: true})
	p := mustPool(t, []*Member{draining, standby, primary}, 0)
	if got := names(p.Snapshot()); !slices.Equal(got, []string{"primary"}) {
		t.Fatalf("snapshot = %v, want the live primary alone", got)
	}
	if !draining.Draining() || standby.Draining() || !slices.Contains(p.Configured(), draining) {
		t.Error("the draining member is not reported as configured and draining")
	}
	bal := NewBalancer(headSelector{}, BalancerOptions{Pool: p})
	expectPick(t, bal, pinTo(standby), standby, true)
	expectPick(t, bal, pinTo(draining), draining, true)
	expectPick(t, bal, Flow{}, primary, false)
	// with only draining members, new flows find none while pinned flows still find theirs
	bal.SetPool(mustPool(t, []*Member{draining}, 0))
	if _, ok := bal.Pick(Flow{}); ok {
		t.Error("a pool whose every member is draining took a new flow")
	}
	expectPick(t, bal, pinTo(draining), draining, true)
}

func TestDrainingMembersLeaveTheirTier(t *testing.T) {
	obs := &recordingObserver{}
	// a draining primary hands new flows to the standby tier, as a failed one would
	p := mustPool(t, []*Member{
		NewMember(MemberOptions{Name: "primary", Draining: true}),
		NewMember(MemberOptions{Name: "standby", Tier: 1}),
	}, 0, PoolOptions{Observer: obs})
	snap := p.Snapshot()
	if got := names(snap); !slices.Equal(got, []string{"standby"}) || snap.Tier != 1 {
		t.Fatalf("snapshot = tier %d %v, want the standby tier", snap.Tier, got)
	}
	events := obs.kinds(EventSnapshot)
	if last := events[len(events)-1]; last.Eligible != 1 || last.Configured != 2 || last.Tier != 1 {
		t.Errorf("event = %+v", last)
	}
}

func TestDrainingMembersAreNotWatched(t *testing.T) {
	health := newHealth(1)
	draining := NewMember(MemberOptions{Name: "draining", Health: health, Draining: true})
	live := NewMember(MemberOptions{Name: "live", Health: newHealth(1)})
	obs := &recordingObserver{}
	p := mustPool(t, []*Member{draining, live}, 1, PoolOptions{Observer: obs})
	if n := health.subscribers(); n != 0 {
		t.Errorf("the pool watches a draining member's health with %d subscriptions", n)
	}
	before := len(obs.kinds(EventSnapshot))
	health.set(-1)
	if got := len(obs.kinds(EventSnapshot)); got != before {
		t.Errorf("a draining member's transition rebuilt the snapshot %d times", got-before)
	}
	// its health is read when a pin asks for it, so no rebuild is needed to follow it
	bal := NewBalancer(headSelector{}, BalancerOptions{Pool: p})
	expectPick(t, bal, pinTo(draining), live, false)
	health.set(1)
	expectPick(t, bal, pinTo(draining), draining, true)
}

func TestPinsNeedADistinctName(t *testing.T) {
	anonymous := []*Member{NewMember(MemberOptions{}), NewMember(MemberOptions{Weight: 2})}
	bal := NewBalancer(headSelector{}, BalancerOptions{Pool: mustPool(t, anonymous, 0)})
	expectPick(t, bal, pinTo(anonymous[1]), anonymous[0], false)
	if bal.Pool().Pinnable(anonymous[1].Hash()) {
		t.Error("a pin to an unnamed member is reported as naming a member")
	}
	// names that hash alike make an ambiguous pin, so none of them is honored
	head := NewMember(MemberOptions{Name: "head"})
	x := NewMember(MemberOptions{Name: "x"})
	y := NewMember(MemberOptions{Name: "y"})
	z := NewMember(MemberOptions{Name: "z"})
	y.hash, z.hash = x.hash, x.hash
	w := NewMember(MemberOptions{Name: "w"})
	bal.SetPool(mustPool(t, []*Member{head, x, y, z, w}, 0))
	for _, m := range []*Member{x, y, z} {
		expectPick(t, bal, pinTo(m), head, false)
		if bal.Pool().Pinnable(m.Hash()) {
			t.Errorf("an ambiguous pin to %s is reported as naming a member", m.Name())
		}
	}
	expectPick(t, bal, pinTo(w), w, true)
}

func TestPinnedEjectedMemberFallsThrough(t *testing.T) {
	a := NewMember(MemberOptions{Name: "a"})
	b := NewMember(MemberOptions{Name: "b"})
	bal := NewBalancer(headSelector{}, BalancerOptions{
		Pool: mustPool(t, []*Member{a, b}, 0), Ejection: EjectionOptions{Failures: 1, Duration: time.Hour},
	})
	pk, _ := bal.Pick(pinTo(b))
	pk.Done(OutcomeConnectFailed)
	if !b.Stats().Ejected(time.Now()) {
		t.Fatal("the pinned member was not ejected")
	}
	expectPick(t, bal, pinTo(b), a, false)
	// once the ejection has run out the pin reaches it again, whether or not the pool has refreshed
	b.stats.ejectedUntil.Store(time.Now().Add(-time.Second).UnixNano())
	expectPick(t, bal, pinTo(b), b, true)
}

func TestEjectionSparesTheLastMemberTakingNewFlows(t *testing.T) {
	a := NewMember(MemberOptions{Name: "a"})
	draining := NewMember(MemberOptions{Name: "draining", Draining: true})
	p := mustPool(t, []*Member{a, draining}, 0)
	if p.eject(a, time.Now().Add(time.Hour), 100) {
		t.Error("ejected the last member taking new flows because a draining member was up")
	}
	if !p.eject(draining, time.Now().Add(time.Hour), 100) {
		t.Error("a draining member was kept as if it were the last to take new flows")
	}
}

func TestRetriesIgnorePins(t *testing.T) {
	a := NewMember(MemberOptions{Name: "a"})
	b := NewMember(MemberOptions{Name: "b"})
	c := NewMember(MemberOptions{Name: "c"})
	bal := NewBalancer(headSelector{}, BalancerOptions{Pool: mustPool(t, []*Member{a, b, c}, 0)})
	if alts := bal.Alternatives(pinTo(c), nil); len(alts) != 3 || alts[0] != a {
		t.Errorf("alternatives = %v, want the strategy's choice first", names(&Snapshot{Members: alts}))
	}
	pk, ok := bal.Repick(pinTo(c), a)
	if !ok || pk.Member() != b || pk.Pinned() {
		t.Errorf("repick = %v, %v, pinned %v; want b, the strategy's choice", pk.Member(), ok, pk.Pinned())
	}
	pk.Done(OutcomeOK)
}
