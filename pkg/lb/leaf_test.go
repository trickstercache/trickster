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
package lb_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/hrw"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
	"github.com/trickstercache/trickster/v2/pkg/lb/lc"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

// nested is a member payload that is itself balanced
type nested struct{ picker lb.Picker }

func (n nested) Picker() lb.Picker { return n.picker }

func poolOf(t *testing.T, s lb.Selector, members ...*lb.Member) *lb.Balancer {
	t.Helper()
	p, err := lb.NewPool(members, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return lb.NewBalancer(s, lb.BalancerOptions{Pool: p})
}

func leaf(name string, weight int) *lb.Member {
	return lb.NewMember(lb.MemberOptions{Name: name, Weight: weight, Value: name})
}

func TestPickLeafOfAFlatPool(t *testing.T) {
	a := leaf("a", 1)
	b := poolOf(t, lc.New(), a)
	lp, ok := lb.PickLeaf(b, lb.Flow{})
	if !ok || lp.Member() != a || lp.Depth() != 1 {
		t.Fatalf("leaf = %v at depth %d, %v", lp.Member(), lp.Depth(), ok)
	}
	if a.Stats().Inflight() != 1 {
		t.Errorf("in flight = %d", a.Stats().Inflight())
	}
	lp.Established(time.Millisecond)
	lp.FirstByte()
	lp.Done(lb.OutcomeOK)
	if a.Stats().Inflight() != 0 {
		t.Errorf("in flight after Done = %d", a.Stats().Inflight())
	}
	var none lb.LeafPick
	none.Done(lb.OutcomeOK)
	if none.Member() != nil || none.Depth() != 0 {
		t.Error("the zero leaf pick has a member")
	}
	if _, ok := lb.PickLeaf(nil, lb.Flow{}); ok {
		t.Error("picked a leaf from no picker")
	}
	if _, ok := lb.PickLeaf(lb.NewBalancer(rr.New()), lb.Flow{}); ok {
		t.Error("picked a leaf from a balancer with no pool")
	}
}

// each level apportions by its own weights, and reports reach every level passed through
func TestPickLeafFollowsNestedPools(t *testing.T) {
	a, b, c := leaf("a", 2), leaf("b", 1), leaf("c", 1)
	inner1 := poolOf(t, lc.New(), a, b)
	inner2 := poolOf(t, lc.New(), c)
	m1 := lb.NewMember(lb.MemberOptions{Name: "inner1", Weight: 3, Value: nested{inner1}})
	m2 := lb.NewMember(lb.MemberOptions{Name: "inner2", Weight: 1, Value: nested{inner2}})
	outer := poolOf(t, lc.New(), m1, m2)

	var held []lb.LeafPick
	for range 12 {
		lp, ok := lb.PickLeaf(outer, lb.Flow{})
		if !ok || lp.Depth() != 2 {
			t.Fatalf("pick at depth %d, %v", lp.Depth(), ok)
		}
		held = append(held, lp)
	}
	// 12 held flows: 9 and 3 at the outer level; 6 and 3 within inner1
	for m, want := range map[*lb.Member]int64{m1: 9, m2: 3, a: 6, b: 3, c: 3} {
		if got := m.Stats().Inflight(); got != want {
			t.Errorf("%s holds %d flows, want %d", m.Name(), got, want)
		}
	}
	for _, lp := range held {
		lp.Done(lb.OutcomeOK)
	}
	for _, m := range []*lb.Member{m1, m2, a, b, c} {
		if got := m.Stats().Inflight(); got != 0 {
			t.Errorf("%s holds %d flows after every pick was done", m.Name(), got)
		}
	}
}

// an inner pool with no eligible member refuses its share; the outer level is released, and
// the share is not handed to a sibling
func TestPickLeafRefusesAnEmptyInnerPoolsShare(t *testing.T) {
	members, healths := lbtest.Members(1)
	healths[0].Set(-1)
	down, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer down.Stop()
	empty := lb.NewMember(lb.MemberOptions{Name: "empty", Value: nested{lb.NewBalancer(lc.New(), lb.BalancerOptions{Pool: down})}})
	live := lb.NewMember(lb.MemberOptions{Name: "live", Value: nested{poolOf(t, lc.New(), leaf("c", 1))}})
	outer := poolOf(t, rr.NewAt(0), empty, live)
	var served, refused int
	for range 8 {
		lp, ok := lb.PickLeaf(outer, lb.Flow{})
		if !ok {
			refused++
			continue
		}
		served++
		lp.Done(lb.OutcomeOK)
	}
	if served != 4 || refused != 4 {
		t.Errorf("served %d, refused %d; want the empty inner pool's half refused", served, refused)
	}
	outerLC := poolOf(t, lc.New(), lb.NewMember(lb.MemberOptions{Name: "empty2", Value: nested{
		lb.NewBalancer(lc.New(), lb.BalancerOptions{Pool: down}),
	}}))
	if _, ok := lb.PickLeaf(outerLC, lb.Flow{}); ok {
		t.Fatal("picked through an empty inner pool")
	}
	if got := outerLC.Pool().Configured()[0].Stats().Inflight(); got != 0 {
		t.Errorf("the refused pick left %d in flight at the outer level", got)
	}
}

// members nested deeper than the bound are refused, and nothing is left in flight
func TestPickLeafDepthBound(t *testing.T) {
	l2 := poolOf(t, lc.New(), leaf("z", 1))
	mid := lb.NewMember(lb.MemberOptions{Name: "l1-member", Value: nested{l2}})
	l1 := poolOf(t, lc.New(), mid)
	top := lb.NewMember(lb.MemberOptions{Name: "l0-member", Value: nested{l1}})
	l0 := poolOf(t, lc.New(), top)
	if _, ok := lb.PickLeaf(l0, lb.Flow{}); ok {
		t.Fatal("a pool three deep was followed")
	}
	if top.Stats().Inflight() != 0 || mid.Stats().Inflight() != 0 {
		t.Errorf("the refused pick left %d and %d in flight", top.Stats().Inflight(), mid.Stats().Inflight())
	}
	// a payload that could offer a picker but has none is a leaf
	plain := lb.NewMember(lb.MemberOptions{Name: "plain", Value: nested{}})
	if lp, ok := lb.PickLeaf(poolOf(t, lc.New(), plain), lb.Flow{}); !ok || lp.Member() != plain {
		t.Error("a member with no picker of its own was not taken as the leaf")
	}
	if allocs := testing.AllocsPerRun(200, func() {
		lp, _ := lb.PickLeaf(l1, lb.Flow{})
		lp.Done(lb.OutcomeOK)
	}); allocs != 0 {
		t.Errorf("a nested leaf pick allocates %v", allocs)
	}
}

// each level is asked with its own flow, and told which member led to it
func TestPickLeafFuncKeysEachLevel(t *testing.T) {
	a, b := leaf("a", 1), leaf("b", 1)
	inner := poolOf(t, &keyed{}, a, b)
	via := lb.NewMember(lb.MemberOptions{Name: "inner", Value: nested{inner}})
	outer := poolOf(t, &keyed{}, via)
	var asked []*lb.Member
	lp, ok := lb.PickLeafFunc(outer, func(_ int, level lb.Picker, from *lb.Member) lb.Flow {
		asked = append(asked, from)
		if level == lb.Picker(inner) {
			return lb.Flow{Key: 1, HasKey: true}
		}
		return lb.Flow{}
	})
	if !ok || lp.Member() != b {
		t.Fatalf("leaf = %v; the inner level's key selects b", lp.Member())
	}
	if len(asked) != 2 || asked[0] != nil || asked[1] != via {
		t.Errorf("levels were asked via %v", asked)
	}
	if lp.Level(0).Member() != via || lp.Level(1).Member() != b || lp.Level(2).Member() != nil || lp.Level(-1).Member() != nil {
		t.Error("Level does not return each level's pick")
	}
	lp.Done(lb.OutcomeOK)
}

// a failure belongs to the leaf: the pool above it is released, not blamed
func TestLeafPickBlamesOnlyTheLeaf(t *testing.T) {
	a := leaf("a", 1)
	inner := poolOf(t, lc.New(), a)
	via := lb.NewMember(lb.MemberOptions{Name: "inner", Value: nested{inner}})
	outer := poolOf(t, lc.New(), via)
	for _, o := range []lb.Outcome{lb.OutcomeConnectFailed, lb.OutcomeFailed} {
		lp, _ := lb.PickLeaf(outer, lb.Flow{})
		lp.Done(o)
	}
	if a.Stats().Failures() != 2 || via.Stats().Failures() != 0 {
		t.Errorf("failures: leaf %d, the pool above it %d", a.Stats().Failures(), via.Stats().Failures())
	}
	if a.Stats().Inflight() != 0 || via.Stats().Inflight() != 0 {
		t.Error("a failed leaf pick left something in flight")
	}
	lp, _ := lb.PickLeaf(outer, lb.Flow{})
	lp.Done(lb.OutcomeOK)
	if a.Stats().Failures() != 0 {
		t.Error("a success did not clear the leaf's failures")
	}
}

// a retry avoids the member that failed, at whichever level holds it
func TestRepickLeafAvoidsTheFailedMember(t *testing.T) {
	a, b := leaf("a", 1), leaf("b", 1)
	inner := poolOf(t, rr.NewAt(0), a, b)
	outer := poolOf(t, rr.NewAt(0), lb.NewMember(lb.MemberOptions{Name: "inner", Value: nested{inner}}))
	flow := func(int, lb.Picker, *lb.Member) lb.Flow { return lb.Flow{} }
	for range 10 {
		lp, ok := lb.RepickLeafFunc(outer, flow, a)
		if !ok || lp.Member() != b {
			t.Fatalf("retry chose %v", lp.Member())
		}
		lp.Done(lb.OutcomeOK)
	}
	only := poolOf(t, rr.NewAt(0), a)
	if _, ok := lb.RepickLeafFunc(only, flow, a); ok {
		t.Error("retried onto the only member, which had failed")
	}
	// a picker that cannot avoid a member is asked for an ordinary pick
	if lp, ok := lb.RepickLeafFunc(plainPicker{only}, flow, b); !ok || lp.Member() != a {
		t.Errorf("plain picker retry = %v, %v", lp.Member(), ok)
	}
}

// plainPicker hides a balancer's Repick
type plainPicker struct{ b *lb.Balancer }

func (p plainPicker) Needs() lb.Needs { return p.b.Needs() }

func (p plainPicker) Pick(f lb.Flow) (lb.Pick, bool) { return p.b.Pick(f) }

// a retry avoids every member the flow has failed on, and moves on from a pool that has no
// other member left to the pools beside it
func TestRepickLeafAvoidsEveryFailedMember(t *testing.T) {
	a, b, c, d := leaf("a", 1), leaf("b", 1), leaf("c", 1), leaf("d", 1)
	flow := func(int, lb.Picker, *lb.Member) lb.Flow { return lb.Flow{} }
	flat := poolOf(t, lc.New(), a, b, c)
	failed := []*lb.Member{a, b}
	for range 10 {
		lp, ok := lb.RepickLeafFunc(flat, flow, failed...)
		if !ok || lp.Member() != c {
			t.Fatalf("retry chose %v", lp.Member())
		}
		lp.Done(lb.OutcomeOK)
	}
	if _, ok := lb.RepickLeafFunc(flat, flow, a, b, c); ok {
		t.Error("retried although every member had failed")
	}
	if len(failed) != 2 || cap(failed) != 2 {
		t.Error("the caller's list of failed members was modified")
	}

	left := lb.NewMember(lb.MemberOptions{Name: "left", Value: nested{poolOf(t, lc.New(), a, b)}})
	right := lb.NewMember(lb.MemberOptions{Name: "right", Value: nested{poolOf(t, lc.New(), c, d)}})
	outer := poolOf(t, rr.NewAt(0), left, right)
	for range 10 {
		lp, ok := lb.RepickLeafFunc(outer, flow, a, b, c)
		if !ok || lp.Member() != d {
			t.Fatalf("nested retry chose %v, %v", lp.Member(), ok)
		}
		lp.Done(lb.OutcomeOK)
	}
	if _, ok := lb.RepickLeafFunc(outer, flow, a, b, c, d); ok {
		t.Error("nested retry found a member although every leaf had failed")
	}
	for _, m := range []*lb.Member{a, b, c, d, left, right} {
		if m.Stats().Inflight() != 0 {
			t.Errorf("%s left %d in flight", m.Name(), m.Stats().Inflight())
		}
	}
}

// singletons builds n pools of one leaf each, as the members of an outer pool
func singletons(t *testing.T, n int) (pools, leaves []*lb.Member) {
	t.Helper()
	for i := range n {
		m := leaf("leaf-"+strconv.Itoa(i), 1)
		leaves = append(leaves, m)
		pools = append(pools, lb.NewMember(lb.MemberOptions{
			Name: "pool-" + strconv.Itoa(i), Value: nested{poolOf(t, rr.NewAt(0), m)},
		}))
	}
	return pools, leaves
}

// however many pools a retry finds spent on its way, it reaches a leaf that is still untried.
// An affinity strategy is the hard case: every retry starts at the same pool, and meets the
// spent ones in the same order, one per pass.
func TestRepickLeafReachesTheLastUntriedLeaf(t *testing.T) {
	const n = 24
	pools, leaves := singletons(t, n)
	outer := poolOf(t, hrw.New(), pools...)
	flow := func(_ int, p lb.Picker, _ *lb.Member) lb.Flow {
		if p.Needs().Has(lb.NeedKey) {
			return lb.Flow{Key: lb.HashString("one-client"), HasKey: true}
		}
		return lb.Flow{}
	}
	// fail every leaf in the order the one client is offered them, down to the last
	var failed []*lb.Member
	for range n {
		lp, ok := lb.RepickLeafFunc(outer, flow, failed...)
		if !ok {
			t.Fatalf("gave up with %d of %d leaves still untried", n-len(failed), n)
		}
		if slices.Contains(failed, lp.Member()) {
			t.Fatalf("retried onto %s, which had failed", lp.Member().Name())
		}
		failed = append(failed, lp.Member())
		lp.Done(lb.OutcomeConnectFailed)
	}
	if _, ok := lb.RepickLeafFunc(outer, flow, failed...); ok {
		t.Error("found a member although every leaf had failed")
	}
	for _, m := range append(pools, leaves...) {
		if m.Stats().Inflight() != 0 {
			t.Errorf("%s left %d in flight", m.Name(), m.Stats().Inflight())
		}
	}
}

// a picker that cannot avoid members offers a spent pool again, which ends the search
func TestRepickLeafEndsWhenAPickerCannotAvoid(t *testing.T) {
	pools, leaves := singletons(t, 3)
	flow := func(int, lb.Picker, *lb.Member) lb.Flow { return lb.Flow{} }
	outer := plainPicker{poolOf(t, rr.NewAt(0), pools...)}
	if _, ok := lb.RepickLeafFunc(outer, flow, leaves...); ok {
		t.Error("found a member although every leaf had failed")
	}
	// and one of its pools that still has a leaf to offer is found on the way round
	if lp, ok := lb.RepickLeafFunc(outer, flow, leaves[0], leaves[1]); !ok || lp.Member() != leaves[2] {
		t.Errorf("retry = %v, %v", lp.Member(), ok)
	} else {
		lp.Done(lb.OutcomeOK)
	}
}

// reaching the leaf ends its run of failed connects, under whichever pools it was picked through
func TestLeafPickReachedEndsTheLeafsRun(t *testing.T) {
	a, b := leaf("a", 1), leaf("b", 1)
	p, err := lb.NewPool([]*lb.Member{a, b}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	inner := lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: p, Ejection: lb.EjectionOptions{Failures: 5}})
	outer := poolOf(t, rr.NewAt(0), lb.NewMember(lb.MemberOptions{Name: "inner", Value: nested{inner}}))
	var failedOn *lb.Member
	for range 2 {
		lp, _ := lb.PickLeaf(outer, lb.Flow{})
		if failedOn == nil {
			failedOn = lp.Member()
		}
		if lp.Member() == failedOn {
			lp.Done(lb.OutcomeConnectFailed)
			continue
		}
		lp.Done(lb.OutcomeCanceled)
	}
	if failedOn.Stats().ConnectFailures() != 1 {
		t.Fatalf("connect failures = %d", failedOn.Stats().ConnectFailures())
	}
	for range 2 {
		lp, _ := lb.PickLeaf(outer, lb.Flow{})
		lp.Reached()
		lp.Done(lb.OutcomeOK)
	}
	if failedOn.Stats().ConnectFailures() != 0 {
		t.Error("reaching the leaf did not end its run of failed connects")
	}
}

// emptyPools builds n members that are each a pool with no member to offer, which an outer
// pool that is not told of their health goes on selecting
func emptyPools(t testing.TB, n int) []*lb.Member {
	t.Helper()
	out := make([]*lb.Member, n)
	for i := range out {
		p, err := lb.NewPool(nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Stop)
		b := lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: p})
		out[i] = lb.NewMember(lb.MemberOptions{Name: "empty-" + strconv.Itoa(i), Value: nested{b}})
	}
	return out
}

// lastSelector always chooses the last member, so a retry over pools that are all empty but
// the last meets every empty one before it, and counts how often it is prepared
type lastSelector struct{ prepares *int }

type lastOf []*lb.Member

func (s lastSelector) Name() string { return "last" }

func (s lastSelector) Needs() lb.Needs { return 0 }

func (s lastSelector) Prepare(snap *lb.Snapshot) lb.Prepared {
	*s.prepares++
	return lastOf(snap.Members)
}

func (l lastOf) Select(lb.Flow) *lb.Member { return l[len(l)-1] }

// worstCase is an outer pool whose strategy prefers a member with nothing to offer, followed
// in its order of preference by every other empty member, and only then by the one live pool
func worstCase(t testing.TB, empties int) (outer *lb.Balancer, live *lb.Member, prepares *int) {
	t.Helper()
	live = leaf("live", 1)
	lp, err := lb.NewPool([]*lb.Member{live}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lp.Stop)
	livePool := lb.NewMember(lb.MemberOptions{
		Name: "live-pool", Value: nested{lb.NewBalancer(rr.NewAt(0), lb.BalancerOptions{Pool: lp})},
	})
	// the choice is the last member; the search then runs on from the first, so the live pool,
	// placed just ahead of the last, is the final member it comes to
	members := emptyPools(t, empties)
	members = append(members[:empties-1:empties-1], livePool, members[empties-1])
	op, err := lb.NewPool(members, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(op.Stop)
	prepares = new(int)
	return lb.NewBalancer(lastSelector{prepares}, lb.BalancerOptions{Pool: op}), live, prepares
}

// a retry that must pass many members with nothing to offer does work in proportion to their
// number: each level is asked once and prepared once, however many of its members are spent
func TestRepickLeafWorkIsLinearInSpentMembers(t *testing.T) {
	for _, empties := range []int{10, 1000, 4000} {
		outer, live, prepares := worstCase(t, empties)
		var asked, skipped int
		flow := func(int, lb.Picker, *lb.Member) lb.Flow { asked++; return lb.Flow{} }
		failed := leaf("failed-elsewhere", 1)
		lp, ok := lb.RepickLeafFunc(countingSkips{outer, &skipped}, flow, failed)
		if !ok || lp.Member() != live || lp.Depth() != 2 {
			t.Fatalf("%d empties: retry = %v, %v", empties, lp.Member(), ok)
		}
		lp.Done(lb.OutcomeOK)
		// the outer level once, and once for each member looked into: the empties and the live pool
		if want := empties + 2; asked != want {
			t.Errorf("%d empties: %d levels asked for a flow, want %d", empties, asked, want)
		}
		if *prepares != 1 {
			t.Errorf("%d empties: the outer strategy was prepared %d times, want once", empties, *prepares)
		}
		// every outer member is tested against the failed set once, never once per spent member
		if want := empties + 1; skipped != want {
			t.Errorf("%d empties: %d exclusion checks at the outer level, want %d", empties, skipped, want)
		}
		for _, m := range outer.Pool().Configured() {
			if m.Stats().Inflight() != 0 {
				t.Fatalf("%s left %d in flight", m.Name(), m.Stats().Inflight())
			}
		}
	}
}

// countingSkips counts the exclusion checks made on behalf of one level
type countingSkips struct {
	*lb.Balancer
	n *int
}

func (c countingSkips) Alternatives(f lb.Flow, skip func(*lb.Member) bool) []*lb.Member {
	return c.Balancer.Alternatives(f, func(m *lb.Member) bool { *c.n++; return skip(m) })
}

func BenchmarkRepickLeafPastEmptyPools(b *testing.B) {
	for _, empties := range []int{100, 1000, 10000} {
		b.Run(strconv.Itoa(empties), func(b *testing.B) {
			outer, _, _ := worstCase(b, empties)
			flow := func(int, lb.Picker, *lb.Member) lb.Flow { return lb.Flow{} }
			failed := leaf("failed-elsewhere", 1)
			b.ReportAllocs()
			for b.Loop() {
				lp, ok := lb.RepickLeafFunc(outer, flow, failed)
				if !ok {
					b.Fatal("no leaf")
				}
				lp.Done(lb.OutcomeOK)
			}
		})
	}
}

// a retry refuses what a first pick refuses: members nested deeper than a leaf pick follows,
// and a level whose strategy declines to choose
func TestRepickLeafRefusals(t *testing.T) {
	flow := func(int, lb.Picker, *lb.Member) lb.Flow { return lb.Flow{} }
	a := leaf("a", 1)
	inner := lb.NewMember(lb.MemberOptions{Name: "inner", Value: nested{poolOf(t, rr.NewAt(0), a)}})
	middle := lb.NewMember(lb.MemberOptions{Name: "middle", Value: nested{poolOf(t, rr.NewAt(0), inner)}})
	outer := poolOf(t, rr.NewAt(0), middle)
	if _, ok := lb.RepickLeafFunc(outer, flow, leaf("other", 1)); ok {
		t.Error("a retry followed members nested deeper than a first pick may")
	}
	for _, m := range []*lb.Member{a, inner, middle} {
		if m.Stats().Inflight() != 0 {
			t.Errorf("%s left %d in flight", m.Name(), m.Stats().Inflight())
		}
	}
	declines := poolOf(t, &countingSelector{refuse: true}, leaf("b", 1), leaf("c", 1))
	if alts := declines.Alternatives(lb.Flow{}, nil); alts != nil {
		t.Errorf("alternatives of a strategy that declines = %v", alts)
	}
	if _, ok := lb.RepickLeafFunc(declines, flow); ok {
		t.Error("a retry chose for a strategy that declined to")
	}
	if alts := lb.NewBalancer(rr.NewAt(0)).Alternatives(lb.Flow{}, nil); alts != nil {
		t.Error("a balancer with no pool has alternatives")
	}
}

// strayOf always chooses one member, whether or not it was offered it
type straySelector struct{ stray *lb.Member }

type strayOf struct{ stray *lb.Member }

func (s straySelector) Name() string { return "stray" }

func (s straySelector) Needs() lb.Needs { return 0 }

func (s straySelector) Prepare(*lb.Snapshot) lb.Prepared { return strayOf(s) }

func (s strayOf) Select(lb.Flow) *lb.Member { return s.stray }

// a strategy's choice comes first among the alternatives exactly as Pick would commit to it,
// so one that chooses outside what it was offered is no better hidden on a retry
func TestAlternativesHonorTheStrategysChoice(t *testing.T) {
	a, b, stray := leaf("a", 1), leaf("b", 1), leaf("stray", 1)
	alts := poolOf(t, straySelector{stray}, a, b).Alternatives(lb.Flow{}, nil)
	if !slices.Equal(alts, []*lb.Member{stray, a, b}) {
		t.Errorf("alternatives = %v", alts)
	}
	// the choice leads, and the others follow in pool order from it
	c, d := leaf("c", 1), leaf("d", 1)
	alts = poolOf(t, straySelector{c}, a, b, c, d).Alternatives(lb.Flow{}, func(m *lb.Member) bool { return m == b })
	if !slices.Equal(alts, []*lb.Member{c, d, a}) {
		t.Errorf("alternatives = %v", alts)
	}
}
