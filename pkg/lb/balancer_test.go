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
)

// countingSelector always takes the first member, and counts how often it was prepared
type countingSelector struct {
	needs    lb.Needs
	prepares int
	refuse   bool
}

type firstMember struct {
	members []*lb.Member
	refuse  bool
}

func (s *countingSelector) Name() string { return "first" }

func (s *countingSelector) Needs() lb.Needs { return s.needs }

func (s *countingSelector) Prepare(snap *lb.Snapshot) lb.Prepared {
	s.prepares++
	return &firstMember{members: snap.Members, refuse: s.refuse}
}

func (p *firstMember) Select(lb.Flow) *lb.Member {
	if p.refuse {
		return nil
	}
	return p.members[0]
}

func TestNeeds(t *testing.T) {
	n := lb.NeedKey | lb.NeedLatency
	if !n.Has(lb.NeedKey) || !n.Has(lb.NeedKey|lb.NeedLatency) || n.Has(lb.NeedInflight) || !n.Has(0) {
		t.Errorf("needs %b misreports what it has", n)
	}
}

// a snapshot is prepared once, however many picks it serves, and again only when it changes
func TestBalancerPreparesOncePerSnapshot(t *testing.T) {
	members, healths := lbtest.Members(1, 1, 1)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	sel := &countingSelector{needs: lb.NeedInflight}
	b := lb.NewBalancer(sel)
	if b.Pool() != nil {
		t.Error("a new balancer has a pool")
	}
	b.SetPool(p)
	for range 100 {
		pk, ok := b.Pick(lb.Flow{})
		if !ok || pk.Member() != members[0] {
			t.Fatal("expected the first member")
		}
	}
	if sel.prepares != 1 {
		t.Errorf("prepared %d times for one snapshot", sel.prepares)
	}
	if got := members[0].Stats().Inflight(); got != 100 {
		t.Errorf("in-flight = %d, want the 100 open picks", got)
	}
	healths[0].Set(-1)
	pk, ok := b.Pick(lb.Flow{})
	if !ok || pk.Member() != members[1] || sel.prepares != 2 {
		t.Errorf("after a transition: member %v, prepared %d times", pk.Member(), sel.prepares)
	}
	pk.Done(lb.OutcomeOK)
	if got := members[1].Stats().Inflight(); got != 0 {
		t.Errorf("in-flight after Done = %d", got)
	}

	// a strategy that needs no in-flight count is not charged for one
	free := lb.NewBalancer(&countingSelector{}, lb.BalancerOptions{Pool: p})
	if pk, ok := free.Pick(lb.Flow{}); !ok || pk.Member().Stats().Inflight() != 0 {
		t.Error("a strategy without NeedInflight moved the in-flight count")
	}
}

// a strategy that selects nothing yields no pick rather than a pick of nothing
func TestBalancerSelectorMayDecline(t *testing.T) {
	members, _ := lbtest.Members(1, 1)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	b := lb.NewBalancer(&countingSelector{refuse: true, needs: lb.NeedInflight}, lb.BalancerOptions{Pool: p})
	if _, ok := b.Pick(lb.Flow{}); ok {
		t.Error("picked although the strategy declined")
	}
	if _, ok := b.Repick(lb.Flow{}, members[0]); ok {
		t.Error("repicked although the strategy declined")
	}
	if _, ok := lb.NewBalancer(&countingSelector{}).Repick(lb.Flow{}, nil); ok {
		t.Error("repicked with no pool")
	}
}

// keyed spreads flows by key and declares every need, so the suite drives all of the balancer
type keyed struct{ members []*lb.Member }

func (*keyed) Name() string { return "keyed" }

func (*keyed) Needs() lb.Needs { return lb.NeedKey | lb.NeedInflight | lb.NeedLatency }

func (*keyed) Prepare(s *lb.Snapshot) lb.Prepared { return &keyed{members: s.Members} }

func (k *keyed) Select(f lb.Flow) *lb.Member {
	return k.members[f.Key%uint64(len(k.members))]
}

func TestBalancerConformance(t *testing.T) {
	lbtest.Run(t, func() lb.Selector { return &keyed{} }, lbtest.Options{})
}

// tuned needs latency and asks for its own averaging
type tuned struct {
	keyed
	opts lb.LatencyOptions
}

func (t *tuned) Latency() lb.LatencyOptions { return t.opts }

func TestLatencyAccounting(t *testing.T) {
	members, _ := lbtest.Members(1)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	st := members[0].Stats()
	b := lb.NewBalancer(&tuned{opts: lb.LatencyOptions{Decay: time.Minute, Penalty: 3 * time.Second}},
		lb.BalancerOptions{Pool: p})

	pk, _ := b.Pick(lb.Flow{})
	pk.Established(40 * time.Millisecond)
	if st.Latency() != 40*time.Millisecond || st.LastSample().IsZero() {
		t.Fatalf("connect sample = %v", st.Latency())
	}
	pk.Done(lb.OutcomeOK)

	pk, _ = b.Pick(lb.Flow{})
	time.Sleep(60 * time.Millisecond)
	pk.FirstByte()
	if got := st.Latency(); got < 60*time.Millisecond || got > 2*time.Second {
		t.Errorf("first-byte sample = %v", got)
	}
	// a caller that gives up says nothing about the member
	before := st.Latency()
	pk.Done(lb.OutcomeCanceled)
	if st.Latency() != before || st.Failures() != 0 {
		t.Errorf("a canceled flow moved the stats: %v, %d failures", st.Latency(), st.Failures())
	}

	// failures record the penalty, then twice the average, up to the ceiling
	for i, want := range []time.Duration{3 * time.Second, 6 * time.Second, 12 * time.Second, 24 * time.Second, 36 * time.Second, 36 * time.Second} {
		pk, _ = b.Pick(lb.Flow{})
		if i%2 == 0 {
			pk.Done(lb.OutcomeFailed)
		} else {
			pk.Done(lb.OutcomeConnectFailed)
		}
		if st.Latency() != want || st.Failures() != int32(i+1) {
			t.Fatalf("failure %d: latency %v, want %v; %d failures", i+1, st.Latency(), want, st.Failures())
		}
	}
	pk, _ = b.Pick(lb.Flow{})
	pk.Done(lb.OutcomeOK)
	if st.Failures() != 0 || st.Inflight() != 0 {
		t.Errorf("after a success: %d failures, %d in flight", st.Failures(), st.Inflight())
	}

	// defaults apply when a strategy does not tune them
	d := lb.NewBalancer(&tuned{}, lb.BalancerOptions{Pool: p})
	fresh, _ := lbtest.Members(1)
	fp, _ := lb.NewPool(fresh, 1)
	defer fp.Stop()
	d.SetPool(fp)
	pk, _ = d.Pick(lb.Flow{})
	pk.Done(lb.OutcomeFailed)
	if got := fresh[0].Stats().Latency(); got != lb.DefaultLatencyPenalty {
		t.Errorf("default penalty = %v", got)
	}
}
