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
package rr_test

import (
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lbtest"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
)

func TestConformance(t *testing.T) {
	lbtest.Run(t, rr.New, lbtest.Options{ExactWeights: true})
}

func TestWeights(t *testing.T) {
	lbtest.RunWeighted(t, rr.New, lbtest.WeightOptions{Tolerance: 0.001})
}

func BenchmarkSelect(b *testing.B) {
	lbtest.Bench(b, rr.New)
}

func balancerAt(t *testing.T, start uint64, weights ...int) *lb.Balancer {
	t.Helper()
	members, _ := lbtest.Members(weights...)
	p, err := lb.NewPool(members, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return lb.NewBalancer(rr.NewAt(start), lb.BalancerOptions{Pool: p})
}

func sequence(t *testing.T, b *lb.Balancer, n int) []int {
	t.Helper()
	seq := make([]int, n)
	for i := range seq {
		pk, ok := b.Pick(lb.Flow{})
		if !ok {
			t.Fatal("no pick")
		}
		seq[i] = pk.Member().Value.(int)
	}
	return seq
}

func TestIdentity(t *testing.T) {
	s := rr.New()
	if s.Name() != rr.Name || s.Needs() != 0 {
		t.Errorf("name %q needs %d", s.Name(), s.Needs())
	}
}

// a heavier member's turns are spread through the rotation, not taken back to back
func TestSequence(t *testing.T) {
	for _, test := range []struct {
		weights []int
		want    []int
	}{
		{[]int{1, 1, 1}, []int{1, 2, 0, 1, 2, 0}},
		{[]int{3, 1}, []int{0, 0, 1, 0, 0, 0, 1, 0}},
		{[]int{1, 3, 2}, []int{1, 2, 1, 1, 2, 0, 1, 2, 1, 1, 2, 0}},
		{[]int{2, 2}, []int{1, 0, 1, 0}},
	} {
		if got := sequence(t, balancerAt(t, 0, test.weights...), len(test.want)); !slices.Equal(got, test.want) {
			t.Errorf("weights %v: sequence = %v, want %v", test.weights, got, test.want)
		}
	}
}

// replicas started together must not all begin on the same member
func TestRandomStart(t *testing.T) {
	first := make(map[int]bool)
	for range 64 {
		members, _ := lbtest.Members(1, 1, 1, 1)
		p, err := lb.NewPool(members, 1)
		if err != nil {
			t.Fatal(err)
		}
		b := lb.NewBalancer(rr.New(), lb.BalancerOptions{Pool: p})
		first[sequence(t, b, 1)[0]] = true
		p.Stop()
	}
	if len(first) < 3 {
		t.Errorf("64 new selectors began on only %d of 4 members", len(first))
	}
}

// no member is handed more than its fair share, plus a turn or two, of any run of turns:
// the opposite of serving a weight-9 member nine times running
func TestSpread(t *testing.T) {
	big := make([]int, 40)
	for i := range big {
		big[i] = 1 + i%7
	}
	heavy := make([]int, 40)
	for i := range heavy {
		heavy[i] = 150 + i
	}
	for _, weights := range [][]int{
		{3, 1}, {1, 3, 2}, {9, 1}, {1, 1, 1, 9}, {7, 1, 1}, {5, 3, 2}, {100, 10, 1}, {2, 3}, big,
		// beyond the laid-out schedule: walked by stride, over few members and over many
		{4000, 3000, 2000, 1000}, {50000, 1}, {6000, 1, 1, 1}, heavy,
	} {
		var total int
		for _, w := range weights {
			total += w
		}
		seq := sequence(t, balancerAt(t, 0, weights...), 2*total)
		worst := 0
		for m, w := range weights {
			// prefix[i] is how many of the first i turns went to m
			prefix := make([]int, len(seq)+1)
			for i, got := range seq {
				prefix[i+1] = prefix[i]
				if got == m {
					prefix[i+1]++
				}
			}
			// every run length of a short rotation; a sample of them for a long one
			step := 1 + total/64
			for k := 1; k <= total; k += step {
				fair := (w*k + total - 1) / total
				for start := 0; start+k <= len(seq); start++ {
					worst = max(worst, prefix[start+k]-prefix[start]-fair)
				}
			}
		}
		if limit := spreadLimit(total); worst > limit {
			t.Errorf("weights %v: a member took %d turns more than its fair share of a run, limit %d",
				weights, worst, limit)
		}
	}
}

// a laid-out rotation keeps every member within one turn of fair; a strided one drifts a
// little further, as any fixed stride must
func spreadLimit(total int) int {
	if total <= 4096 {
		return 1
	}
	return 4
}

// the rotation belongs to the selector: a pool swapped for one of the same membership
// continues it rather than restarting it
func TestRotationSurvivesPoolSwap(t *testing.T) {
	members, _ := lbtest.Members(2, 1, 3)
	b := lb.NewBalancer(rr.NewAt(0))
	var got []int
	for range 6 {
		p, err := lb.NewPool(members, 1)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Stop()
		b.SetPool(p)
		got = append(got, sequence(t, b, 5)...)
	}
	period := got[:6]
	for i, m := range got {
		if m != period[i%6] {
			t.Fatalf("pick %d = member %d; the rotation restarted across a swap: %v", i, m, got)
		}
	}
	counts := make(map[int]int)
	for _, m := range period {
		counts[m]++
	}
	if counts[0] != 2 || counts[1] != 1 || counts[2] != 3 {
		t.Errorf("one rotation = %v", period)
	}
}
