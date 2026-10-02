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
)

func TestPoolSelectsFromTheLowestLiveTier(t *testing.T) {
	health := map[string]*fakeHealth{}
	member := func(name string, tier int) *Member {
		health[name] = newHealth(1)
		return NewMember(MemberOptions{Name: name, Tier: tier, Health: health[name]})
	}
	obs := &recordingObserver{}
	// a standby listed ahead of the members it stands by for is a standby all the same
	p := mustPool(t, []*Member{
		member("standby", 1), member("a", 0), member("last-resort", 2), member("b", -3),
	}, 1, PoolOptions{Observer: obs})
	expect := func(tier int, want ...string) {
		t.Helper()
		snap := p.Snapshot()
		if got := names(snap); !slices.Equal(got, want) || snap.Tier != tier {
			t.Fatalf("snapshot = tier %d %v, want tier %d %v", snap.Tier, got, tier, want)
		}
		events := obs.kinds(EventSnapshot)
		if last := events[len(events)-1]; last.Tier != tier || last.Eligible != len(want) {
			t.Fatalf("event = %+v", last)
		}
	}
	if got := p.Configured()[3].Tier(); got != 0 {
		t.Errorf("a negative tier = %d, want 0", got)
	}
	expect(0, "a", "b")
	health["a"].set(-1)
	expect(0, "b")
	health["b"].set(-1)
	expect(1, "standby")
	health["standby"].set(-1)
	expect(2, "last-resort")
	health["last-resort"].set(-1)
	expect(0)
	// one primary returning takes every flow back from the standbys
	health["standby"].set(1)
	health["last-resort"].set(1)
	expect(1, "standby")
	health["b"].set(1)
	expect(0, "b")
}

type headSelector struct{}

type headOf []*Member

func (headSelector) Name() string { return "head" }

func (headSelector) Needs() Needs { return 0 }

func (headSelector) Prepare(snap *Snapshot) Prepared { return headOf(snap.Members) }

func (h headOf) Select(Flow) *Member { return h[0] }

func TestEjectingTheOnlyPrimaryFailsOver(t *testing.T) {
	primary := NewMember(MemberOptions{Name: "primary"})
	standby := NewMember(MemberOptions{Name: "standby", Tier: 1})
	p := mustPool(t, []*Member{primary, standby}, 0)
	b := NewBalancer(headSelector{}, BalancerOptions{
		Pool: p, Ejection: EjectionOptions{Failures: 1, MaxPercent: 50},
	})
	pk, ok := b.Pick(Flow{})
	if !ok || pk.Member() != primary {
		t.Fatalf("pick = %v, %v", pk.Member(), ok)
	}
	pk.Done(OutcomeConnectFailed)
	if got := names(p.Snapshot()); !slices.Equal(got, []string{"standby"}) {
		t.Fatalf("after ejection = %v", got)
	}
}
