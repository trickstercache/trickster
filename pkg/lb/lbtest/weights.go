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
package lbtest

import (
	"math"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// WeightOptions describes how a strategy is expected to honor member weights.
type WeightOptions struct {
	// Tolerance is how far a member's share of picks may sit from its share of the pool's
	// weight, as an absolute fraction; zero means 0.03.
	Tolerance float64
	// Keyed marks a strategy whose weights divide the key space rather than the load: it is
	// tested with many distinct keys instead of a load simulation.
	Keyed bool
	// LoadOnly marks a strategy that ranks members, so its weights bias the split only once
	// work is in flight; idle, its best-ranked member rightly takes every flow.
	LoadOnly bool
}

// simulated weights and what one unit of weight can finish per tick
var (
	shareWeights = []int{1, 2, 5}
	unitCapacity = 4
)

// RunWeighted holds a strategy to shares proportional to weight: over an idle pool, where
// nothing distinguishes the members but their weights, and under sustained load against
// members whose capacity is proportional to their weight.
func RunWeighted(t *testing.T, newSelector func() lb.Selector, o WeightOptions) {
	runWeighted(realT{t}, newSelector, o)
}

func runWeighted(t suiteT, newSelector func() lb.Selector, o WeightOptions) {
	if o.Tolerance <= 0 {
		o.Tolerance = 0.03
	}
	if o.Keyed {
		t.Run("key space is shared by weight", func(t suiteT) { testKeySpace(t, newSelector, o) })
		return
	}
	if !o.LoadOnly {
		t.Run("an idle pool is shared by weight", func(t suiteT) { testIdleShares(t, newSelector, o) })
	}
	t.Run("sustained load is shared by weight", func(t suiteT) { testLoadShares(t, newSelector, o) })
}

func assertShares(t reporter, members []*lb.Member, picks map[*lb.Member]int, tolerance float64) {
	t.Helper()
	var total, weight int
	for _, m := range members {
		total += picks[m]
		weight += m.Weight()
	}
	for _, m := range members {
		got := float64(picks[m]) / float64(total)
		want := float64(m.Weight()) / float64(weight)
		if math.Abs(got-want) > tolerance {
			t.Errorf("%s (weight %d of %d) took %.3f of %d picks, want %.3f within %.3f",
				m.Name(), m.Weight(), weight, got, total, want, tolerance)
		}
	}
}

func testIdleShares(t suiteT, newSelector func() lb.Selector, o WeightOptions) {
	members, _ := Members(shareWeights...)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	f := newFlows()
	picks := make(map[*lb.Member]int)
	for range 40000 {
		pk, ok := b.Pick(f.next())
		if !ok {
			t.Fatal("no pick")
		}
		picks[pk.Member()]++
		// every member answers alike, and nothing is ever left in flight
		pk.Established(10 * time.Millisecond)
		pk.Done(lb.OutcomeOK)
	}
	assertShares(t, members, picks, o.Tolerance)
}

func testLoadShares(t suiteT, newSelector func() lb.Selector, o WeightOptions) {
	members, _ := Members(shareWeights...)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	f := newFlows()
	var capacity int
	for _, w := range shareWeights {
		capacity += w * unitCapacity
	}
	// arrivals run at nine tenths of what the pool can finish, so queues form and drain
	arrivals := capacity * 9 / 10
	open := make(map[*lb.Member][]lb.Pick, len(members))
	picks := make(map[*lb.Member]int)
	for range 3000 {
		for _, m := range members {
			done := min(len(open[m]), m.Weight()*unitCapacity)
			for _, pk := range open[m][:done] {
				pk.Done(lb.OutcomeOK)
			}
			open[m] = open[m][done:]
		}
		for range arrivals {
			pk, ok := b.Pick(f.next())
			if !ok {
				t.Fatal("no pick")
			}
			pk.Established(10 * time.Millisecond)
			picks[pk.Member()]++
			open[pk.Member()] = append(open[pk.Member()], pk)
		}
	}
	for _, m := range members {
		for _, pk := range open[m] {
			pk.Done(lb.OutcomeOK)
		}
		if backlog := len(open[m]); backlog > 4*m.Weight()*unitCapacity {
			t.Errorf("%s was left %d flows behind, more than four ticks of its capacity", m.Name(), backlog)
		}
	}
	assertShares(t, members, picks, o.Tolerance)
}

func testKeySpace(t suiteT, newSelector func() lb.Selector, o WeightOptions) {
	members, _ := Members(shareWeights...)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	f := newFlows()
	picks := make(map[*lb.Member]int)
	for range 200000 {
		pk, ok := b.Pick(f.next())
		if !ok {
			t.Fatal("no pick")
		}
		picks[pk.Member()]++
		pk.Done(lb.OutcomeOK)
	}
	assertShares(t, members, picks, o.Tolerance)
}
