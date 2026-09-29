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

package lb

import "testing"

type firstSelector struct{}

type firstPrepared []*Member

func (firstSelector) Name() string                 { return "first" }
func (firstSelector) Needs() Needs                 { return NeedInflight }
func (firstSelector) Prepare(s *Snapshot) Prepared { return firstPrepared(s.Members) }
func (p firstPrepared) Select(Flow) *Member        { return p[0] }

func TestPicksNameThePoolTheyCameFrom(t *testing.T) {
	a := NewMember(MemberOptions{Name: "a"})
	b := NewMember(MemberOptions{Name: "b"})
	first := mustPool(t, []*Member{a, b}, 0, PoolOptions{Value: "first"})
	second := mustPool(t, []*Member{b}, 0, PoolOptions{Value: "second"})
	if first.Value() != "first" || mustPool(t, nil, 0).Value() != nil {
		t.Fatal("a pool returns the payload its options set, and nil without one")
	}
	bal := NewBalancer(firstSelector{}, BalancerOptions{Pool: first})
	if pk, ok := bal.Pick(Flow{}); !ok || pk.Pool() != first || pk.Member() != a {
		t.Fatalf("pick from the first pool: %v %v", ok, pk.Pool().Value())
	}
	alternatives := bal.Alternatives(Flow{}, nil)

	bal.SetPool(second)
	if pk, ok := bal.Pick(Flow{Pin: b.Hash(), HasPin: true}); !ok || !pk.Pinned() || pk.Pool() != second {
		t.Fatal("a pinned pick names the pool it was made from")
	}
	if pk, ok := bal.Repick(Flow{}); !ok || pk.Pool() != second {
		t.Fatal("a repick names the pool it was made from")
	}
	// a member listed before the swap is committed only while the pool still holds it
	if _, ok := bal.Commit(alternatives[0]); ok {
		t.Fatal("a member that left the pool was committed")
	}
	if pk, ok := bal.Commit(alternatives[1]); !ok || pk.Pool() != second {
		t.Fatal("a member the new pool holds is committed from it")
	}
	if pk := (Pick{}); pk.Pool() != nil {
		t.Fatal("the zero pick names no pool")
	}
}

func TestPoolHolds(t *testing.T) {
	named := NewMember(MemberOptions{Name: "named"})
	unnamed := NewMember(MemberOptions{})
	p := mustPool(t, []*Member{named, unnamed}, 0)
	if !p.holds(named) || !p.holds(unnamed) || p.holds(NewMember(MemberOptions{Name: "named"})) {
		t.Fatal("a pool holds exactly the members it was built with")
	}
}
