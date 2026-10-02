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
package lt_test

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
	"github.com/trickstercache/trickster/v2/pkg/lb/lt"
)

func newSelector() lb.Selector { return lt.New(lt.Options{}) }

func TestConformance(t *testing.T) {
	lbtest.Run(t, newSelector, lbtest.Options{})
}

func TestWeights(t *testing.T) {
	lbtest.RunWeighted(t, newSelector, lbtest.WeightOptions{LoadOnly: true})
}

func BenchmarkSelect(b *testing.B) {
	lbtest.Bench(b, newSelector)
}

func newBalancer(t *testing.T, o lt.Options, weights ...int) (*lb.Balancer, []*lb.Member) {
	t.Helper()
	members, _ := lbtest.Members(weights...)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return lb.NewBalancer(lt.New(o), lb.BalancerOptions{Pool: p}), members
}

// serve runs n flows one at a time, each member answering in its own latency, and returns
// how many each member took
func serve(t *testing.T, b *lb.Balancer, n int, latency map[*lb.Member]time.Duration) map[*lb.Member]int {
	t.Helper()
	counts := make(map[*lb.Member]int)
	for range n {
		pk, ok := b.Pick(lb.Flow{})
		if !ok {
			t.Fatal("no pick")
		}
		counts[pk.Member()]++
		pk.Established(latency[pk.Member()])
		pk.Done(lb.OutcomeOK)
	}
	return counts
}

func TestIdentityAndTuning(t *testing.T) {
	s := lt.New(lt.Options{})
	if s.Name() != lt.Name || s.Needs() != lb.NeedInflight|lb.NeedLatency {
		t.Errorf("name %q needs %b", s.Name(), s.Needs())
	}
	if got := s.(lb.LatencyTuner).Latency(); got.Decay != lb.DefaultLatencyDecay || got.Penalty != lb.DefaultLatencyPenalty {
		t.Errorf("default tuning = %+v", got)
	}
	tuned := lt.New(lt.Options{Decay: time.Minute, Penalty: time.Second}).(lb.LatencyTuner).Latency()
	if tuned.Decay != time.Minute || tuned.Penalty != time.Second {
		t.Errorf("tuning = %+v", tuned)
	}
}

func TestPrefersTheFasterMember(t *testing.T) {
	b, m := newBalancer(t, lt.Options{}, 1, 1, 1)
	latency := map[*lb.Member]time.Duration{m[0]: 80 * time.Millisecond, m[1]: 5 * time.Millisecond, m[2]: 40 * time.Millisecond}
	serve(t, b, 30, latency)
	counts := serve(t, b, 300, latency)
	// all but the odd flow that re-tries a member whose average has faded
	if counts[m[1]] < 290 {
		t.Errorf("with the pool idle the fastest member took %d of 300 flows", counts[m[1]])
	}
}

// resolution is nanoseconds: a member answering in 200µs still ranks ahead of one at 900µs
func TestSubMillisecondMembersAreRanked(t *testing.T) {
	b, m := newBalancer(t, lt.Options{}, 1, 1)
	latency := map[*lb.Member]time.Duration{m[0]: 900 * time.Microsecond, m[1]: 200 * time.Microsecond}
	serve(t, b, 20, latency)
	if counts := serve(t, b, 100, latency); counts[m[1]] < 95 {
		t.Errorf("the 200µs member took %d of 100 flows", counts[m[1]])
	}
}

// in-flight work multiplies a member's score, so load spills to slower members instead of
// queueing on the fastest
func TestLoadSpillsToSlowerMembers(t *testing.T) {
	b, m := newBalancer(t, lt.Options{}, 1, 1)
	latency := map[*lb.Member]time.Duration{m[0]: 10 * time.Millisecond, m[1]: 30 * time.Millisecond}
	serve(t, b, 20, latency)
	for range 40 {
		pk, _ := b.Pick(lb.Flow{})
		pk.Established(latency[pk.Member()])
	}
	fast, slow := m[0].Stats().Inflight(), m[1].Stats().Inflight()
	if slow == 0 || fast <= slow || fast > 4*slow {
		t.Errorf("held flows = %d fast and %d slow; want about 3 to 1", fast, slow)
	}
}

// a member with no sample is scored at its peers' mean: it gets work, but not all of it
func TestColdMemberIsNeitherFloodedNorStarved(t *testing.T) {
	members, healths := lbtest.Members(1, 1, 1)
	healths[2].Set(-1)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: p})
	latency := map[*lb.Member]time.Duration{
		members[0]: 10 * time.Millisecond, members[1]: 30 * time.Millisecond, members[2]: 20 * time.Millisecond,
	}
	serve(t, b, 40, latency)
	// the cold member joins while its peers are busy
	healths[2].Set(1)
	var held []lb.Pick
	for range 60 {
		pk, _ := b.Pick(lb.Flow{})
		held = append(held, pk)
	}
	cold := members[2].Stats().Inflight()
	if cold == 0 {
		t.Error("the cold member was starved")
	}
	if cold > 40 {
		t.Errorf("the cold member was flooded with %d of 60 held flows", cold)
	}
	for _, pk := range held {
		pk.Done(lb.OutcomeOK)
	}
}

// a member that fails in a millisecond must not win on speed
func TestFastFailingMemberLoses(t *testing.T) {
	b, m := newBalancer(t, lt.Options{}, 1, 1)
	healthy, failing := m[0], m[1]
	counts := make(map[*lb.Member]int)
	for range 400 {
		pk, _ := b.Pick(lb.Flow{})
		counts[pk.Member()]++
		if pk.Member() == failing {
			pk.Established(time.Millisecond)
			pk.Done(lb.OutcomeFailed)
			continue
		}
		pk.Established(50 * time.Millisecond)
		pk.Done(lb.OutcomeOK)
	}
	if counts[failing] > 8 {
		t.Errorf("the fast-failing member took %d of 400 flows", counts[failing])
	}
	if failing.Stats().Latency() < lb.DefaultLatencyPenalty {
		t.Errorf("a failure was recorded as %v", failing.Stats().Latency())
	}
	_ = healthy
}

// a penalty fades while the member is passed over, so it is tried again and, once it answers
// well, forgiven
func TestDecayForgives(t *testing.T) {
	b, m := newBalancer(t, lt.Options{Decay: 20 * time.Millisecond, Penalty: 200 * time.Millisecond}, 1, 1)
	good, recovering := m[0], m[1]
	latency := map[*lb.Member]time.Duration{good: 10 * time.Millisecond, recovering: 2 * time.Millisecond}
	for recovering.Stats().Failures() == 0 {
		pk, _ := b.Pick(lb.Flow{})
		if pk.Member() == recovering {
			pk.Done(lb.OutcomeFailed)
			continue
		}
		pk.Established(latency[good])
		pk.Done(lb.OutcomeOK)
	}
	if counts := serve(t, b, 20, latency); counts[recovering] != 0 {
		t.Fatalf("a freshly penalized member took %d of 20 flows", counts[recovering])
	}
	// 200ms fades below the peer's 10ms after ln(20) decays, about 60ms
	deadline := time.Now().Add(5 * time.Second)
	for recovering.Stats().Failures() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the penalized member was never tried again")
		}
		time.Sleep(5 * time.Millisecond)
		serve(t, b, 1, latency)
	}
	time.Sleep(100 * time.Millisecond)
	if counts := serve(t, b, 50, latency); counts[recovering] < 45 {
		t.Errorf("the recovered, faster member took %d of 50 flows", counts[recovering])
	}
}

// with the pool idle, a member with no sample still gets a turn: it ties with the fastest
// member rather than waiting behind it for load that may never come
func TestColdMemberIsTriedWhenIdle(t *testing.T) {
	b, m := newBalancer(t, lt.Options{}, 1, 1, 1)
	latency := map[*lb.Member]time.Duration{m[0]: 5 * time.Millisecond, m[1]: 9 * time.Millisecond, m[2]: 300 * time.Millisecond}
	counts := serve(t, b, 60, latency)
	for _, member := range m {
		if counts[member] == 0 {
			t.Errorf("%s was never tried in 60 sequential flows: %v", member.Name(), counts)
		}
	}
	if counts[m[2]] > 6 {
		t.Errorf("the slow member took %d of 60 flows once it had been measured", counts[m[2]])
	}
	// when the only sampled members are failing, a cold one is scored against them
	failing, fresh := newBalancer(t, lt.Options{}, 1, 1)
	pk, _ := failing.Pick(lb.Flow{})
	pk.Done(lb.OutcomeFailed)
	next, _ := failing.Pick(lb.Flow{})
	if next.Member() == pk.Member() {
		t.Errorf("a member with no sample lost to one that had just failed")
	}
	next.Done(lb.OutcomeOK)
	_ = fresh
}
