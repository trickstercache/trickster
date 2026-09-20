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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
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
	lp, ok := lb.PickLeafFunc(outer, func(level lb.Picker, from *lb.Member) lb.Flow {
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
	flow := func(lb.Picker, *lb.Member) lb.Flow { return lb.Flow{} }
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
