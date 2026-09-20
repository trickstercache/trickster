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
package p2c_test

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
	"github.com/trickstercache/trickster/v2/pkg/lb/p2c"
)

func TestConformance(t *testing.T) {
	lbtest.Run(t, p2c.New, lbtest.Options{})
}

func TestWeights(t *testing.T) {
	lbtest.RunWeighted(t, p2c.New, lbtest.WeightOptions{})
}

func BenchmarkSelect(b *testing.B) {
	lbtest.Bench(b, p2c.New)
}

func newBalancer(t *testing.T, weights ...int) (*lb.Balancer, []*lb.Member) {
	t.Helper()
	members, _ := lbtest.Members(weights...)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return lb.NewBalancer(p2c.New(), lb.BalancerOptions{Pool: p}), members
}

func TestIdentity(t *testing.T) {
	s := p2c.New()
	if s.Name() != p2c.Name || s.Needs() != lb.NeedInflight {
		t.Errorf("name %q needs %b", s.Name(), s.Needs())
	}
}

// with flows held open, the spread stays far tighter than random placement would leave it
func TestBalancesHeldFlows(t *testing.T) {
	b, members := newBalancer(t, 1, 1, 1, 1, 1, 1, 1, 1)
	for range 8000 {
		b.Pick(lb.Flow{})
	}
	for _, m := range members {
		if got := m.Stats().Inflight(); got < 960 || got > 1040 {
			t.Errorf("%s holds %d of 8000 held flows, want 1000 within 40", m.Name(), got)
		}
	}
}

// a member already carrying a load is passed over for an idle one whenever the two are drawn
func TestPrefersTheIdleMember(t *testing.T) {
	b, members := newBalancer(t, 1, 1)
	var busy *lb.Member
	for range 50 {
		pk, _ := b.Pick(lb.Flow{})
		if busy == nil {
			busy = pk.Member()
		}
		if pk.Member() != busy {
			pk.Done(lb.OutcomeOK)
		}
	}
	before := busy.Stats().Inflight()
	for range 200 {
		pk, _ := b.Pick(lb.Flow{})
		if pk.Member() == busy {
			t.Fatalf("picked the member holding %d flows over an idle one", before)
		}
		pk.Done(lb.OutcomeOK)
	}
	_ = members
}

// weights beyond 32 bits of total still sample in range
func TestHugeWeights(t *testing.T) {
	b, members := newBalancer(t, 1<<31, 1<<31, 1<<31, 7)
	seen := make(map[*lb.Member]bool)
	for range 2000 {
		pk, ok := b.Pick(lb.Flow{})
		if !ok {
			t.Fatal("no pick")
		}
		seen[pk.Member()] = true
		pk.Done(lb.OutcomeOK)
	}
	for _, m := range members[:3] {
		if !seen[m] {
			t.Errorf("%s was never drawn", m.Name())
		}
	}
}
