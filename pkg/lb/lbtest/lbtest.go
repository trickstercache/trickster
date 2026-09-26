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
// Package lbtest is the conformance suite for lb.Selector implementations. A strategy that
// passes Run honors the contract the balancer and every plane adapter rely on.
package lbtest

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Options describes what a strategy promises beyond the common contract.
type Options struct {
	// ExactWeights holds the strategy to exact apportionment: every run of total-weight
	// consecutive picks against a stable pool gives each member exactly its weight.
	ExactWeights bool
}

const (
	passing int32 = 1
	failing int32 = -1
	floor         = 1
)

// flows yields keyed flows from a fixed seed, so a run is reproducible
type flows struct{ r *rand.Rand }

func newFlows() *flows {
	return &flows{r: rand.New(rand.NewPCG(0x1b, 0x5eed))} // #nosec G404 -- reproducible test keys, not secrets
}

func (f *flows) next() lb.Flow {
	return lb.Flow{Key: f.r.Uint64(), HasKey: true}
}

// Members returns one named, passing member per weight, with the Health that drives each.
func Members(weights ...int) ([]*lb.Member, []*Health) {
	members := make([]*lb.Member, len(weights))
	healths := make([]*Health, len(weights))
	for i, w := range weights {
		healths[i] = NewHealth(passing)
		members[i] = lb.NewMember(lb.MemberOptions{
			Name: "member-" + strconv.Itoa(i), Weight: w, Health: healths[i], Value: i,
		})
	}
	return members, healths
}

func newPool(t reporter, members []*lb.Member) *lb.Pool {
	t.Helper()
	p, err := lb.NewPool(members, floor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p
}

func uniform(n int) []int {
	weights := make([]int, n)
	for i := range weights {
		weights[i] = 1
	}
	return weights
}

// Run holds the selectors that newSelector returns to the lb.Selector contract. Each call of
// newSelector must return a new instance.
func Run(t *testing.T, newSelector func() lb.Selector, o Options) {
	run(realT{t}, newSelector, o)
}

func run(t suiteT, newSelector func() lb.Selector, o Options) {
	t.Run("identity", func(t suiteT) { testIdentity(t, newSelector) })
	t.Run("no eligible member", func(t suiteT) { testNoEligibleMember(t, newSelector) })
	t.Run("picks only eligible members", func(t suiteT) { testOnlyEligible(t, newSelector) })
	t.Run("every member is reachable", func(t suiteT) { testReachable(t, newSelector) })
	t.Run("repick excludes the failed member", func(t suiteT) { testRepick(t, newSelector) })
	t.Run("a pinned member is honored", func(t suiteT) { testPinned(t, newSelector) })
	t.Run("a pin the pool cannot honor falls through", func(t suiteT) { testPinFallsThrough(t, newSelector) })
	t.Run("draining members take no new flows", func(t suiteT) { testDraining(t, newSelector) })
	t.Run("in-flight accounting balances", func(t suiteT) { testInflight(t, newSelector) })
	t.Run("adversarial snapshots terminate", func(t suiteT) { testAdversarial(t, newSelector) })
	t.Run("zero allocations", func(t suiteT) { testZeroAlloc(t, newSelector) })
	t.Run("concurrent picks and swaps", func(t suiteT) { testConcurrent(t, newSelector) })
	if o.ExactWeights {
		t.Run("exact weights", func(t suiteT) { testExactWeights(t, newSelector) })
		t.Run("exact weights after a live weight change", func(t suiteT) {
			testExactAfterWeightChange(t, newSelector)
		})
	}
}

func testIdentity(t suiteT, newSelector func() lb.Selector) {
	a, b := newSelector(), newSelector()
	if a == nil || a.Name() == "" {
		t.Fatal("a selector must have a name")
	}
	if first := a.Needs(); first != b.Needs() || first != a.Needs() {
		t.Error("a strategy's needs must not vary")
	}
	if bal := lb.NewBalancer(a); bal.Needs() != a.Needs() || bal.Selector() != a {
		t.Error("the balancer does not report its selector")
	}
}

func testNoEligibleMember(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	b := lb.NewBalancer(newSelector())
	if _, ok := b.Pick(f.next()); ok {
		t.Error("picked with no pool")
	}
	b.SetPool(newPool(t, nil))
	if _, ok := b.Pick(f.next()); ok {
		t.Error("picked from an empty pool")
	}
	members, healths := Members(1, 1)
	for _, h := range healths {
		h.Set(failing)
	}
	b.SetPool(newPool(t, members))
	if _, ok := b.Pick(f.next()); ok {
		t.Error("picked from a pool with no eligible member")
	}
	if _, ok := b.Repick(f.next(), nil); ok {
		t.Error("repicked from a pool with no eligible member")
	}
	// a member that recovers is picked by the very next call
	healths[1].Set(passing)
	pk, ok := b.Pick(f.next())
	if !ok || pk.Member() != members[1] {
		t.Error("the recovered member was not picked")
	}
	pk.Done(lb.OutcomeOK)
	b.SetPool(nil)
	if _, ok := b.Pick(f.next()); ok || b.Pool() != nil {
		t.Error("picked after the pool was removed")
	}
}

func testOnlyEligible(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	for _, weights := range [][]int{{1}, {1, 1}, {4}, {1, 3}, {2, 1, 5, 1, 1}} {
		members, healths := Members(weights...)
		b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
		for down := range members {
			healths[down].Set(failing)
			for range 200 {
				pk, ok := b.Pick(f.next())
				if len(members) == 1 {
					if ok {
						t.Fatalf("weights %v: picked the only member while it was failing", weights)
					}
					continue
				}
				if !ok {
					t.Fatalf("weights %v: no pick with eligible members", weights)
				}
				if pk.Member() == members[down] || !slices.Contains(members, pk.Member()) {
					t.Fatalf("weights %v: picked %q, which is not eligible", weights, pk.Member().Name())
				}
				pk.Done(lb.OutcomeOK)
			}
			healths[down].Set(passing)
		}
	}
}

func testReachable(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	for _, weights := range [][]int{uniform(4), {1, 2, 3, 4}} {
		members, _ := Members(weights...)
		b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
		seen := make(map[*lb.Member]int)
		// some work is always in flight: a strategy that ranks members may, by design, give
		// an idle pool's every flow to its best member
		var held [32]lb.Pick
		for i := range 4000 {
			pk, ok := b.Pick(f.next())
			if !ok {
				t.Fatal("no pick with eligible members")
			}
			seen[pk.Member()]++
			held[i%len(held)].Done(lb.OutcomeOK)
			held[i%len(held)] = pk
		}
		for _, pk := range held {
			pk.Done(lb.OutcomeOK)
		}
		for _, m := range members {
			if seen[m] == 0 {
				t.Errorf("weights %v: %s was never picked in 4000 flows", weights, m.Name())
			}
		}
	}
}

func testRepick(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	members, _ := Members(1, 3, 1)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	for range 300 {
		pk, ok := b.Pick(f.next())
		if !ok {
			t.Fatal("no pick")
		}
		pk.Done(lb.OutcomeConnectFailed)
		again, ok := b.Repick(f.next(), pk.Member())
		if !ok || again.Member() == pk.Member() || !slices.Contains(members, again.Member()) {
			t.Fatalf("repick after %s chose %v", pk.Member().Name(), again.Member())
		}
		again.Done(lb.OutcomeOK)
	}
	only, _ := Members(1)
	single := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, only)})
	if _, ok := single.Repick(f.next(), only[0]); ok {
		t.Error("repicked the failed member of a pool of one")
	}
}

func pinnedTo(f lb.Flow, m *lb.Member) lb.Flow {
	f.Pin, f.HasPin = m.Hash(), true
	return f
}

func testPinned(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	members, _ := Members(1, 2, 1)
	// a standby and a draining member keep the flows pinned to them as surely as a live member
	all := append(slices.Clone(members),
		lb.NewMember(lb.MemberOptions{Name: "standby", Tier: 1, Health: NewHealth(passing)}),
		lb.NewMember(lb.MemberOptions{Name: "draining", Draining: true, Health: NewHealth(passing)}),
	)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, all)})
	for _, m := range all {
		for range 16 {
			pk, ok := b.Pick(pinnedTo(f.next(), m))
			if !ok || pk.Member() != m || !pk.Pinned() {
				t.Fatalf("a flow pinned to %s went to %v, pinned %v", m.Name(), pk.Member(), pk.Pinned())
			}
			pk.Done(lb.OutcomeOK)
		}
	}
}

func testPinFallsThrough(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	members, healths := Members(1, 1, 1)
	unnamed := lb.NewMember(lb.MemberOptions{Health: NewHealth(passing)})
	pool := append(slices.Clone(members), unnamed)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, pool)})
	// the strategy sees member 0 before it fails, so one that keeps selecting it is caught too
	for range 8 {
		if pk, ok := b.Pick(f.next()); ok {
			pk.Done(lb.OutcomeOK)
		}
	}
	healths[0].Set(failing)
	for _, pin := range []lb.Flow{
		{Pin: members[0].Hash(), HasPin: true},     // an ineligible member
		{Pin: members[0].Hash() ^ 1, HasPin: true}, // no member at all
		{Pin: unnamed.Hash(), HasPin: true},        // an unnamed member
		{Pin: members[1].Hash()},                   // a pin the flow does not ask for
	} {
		for range 100 {
			flow := f.next()
			flow.Pin, flow.HasPin = pin.Pin, pin.HasPin
			pk, ok := b.Pick(flow)
			switch {
			case !ok:
				t.Fatal("no pick with eligible members")
			case !slices.Contains(pool, pk.Member()):
				t.Fatalf("a pin to %#x went to %s, which was never in the pool", pin.Pin, pk.Member().Name())
			case pk.Member() == members[0] || pk.Pinned():
				t.Fatalf("a pin to %#x went to %s, pinned %v, rather than to an eligible member the strategy chose",
					pin.Pin, pk.Member().Name(), pk.Pinned())
			}
			pk.Done(lb.OutcomeOK)
		}
	}
}

func testDraining(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	members, healths := Members(1, 2, 1)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	// the strategy sees member 1 take flows, then member 1 starts to drain: the same member,
	// rebuilt with its health and stats, in a new pool
	pickSequence(t, b, 16)
	drained := slices.Clone(members)
	drain := func(i int) {
		drained[i] = lb.NewMember(lb.MemberOptions{
			Name: members[i].Name(), Weight: members[i].Weight(), Health: healths[i],
			Stats: members[i].Stats(), Draining: true,
		})
	}
	drain(1)
	b.SetPool(newPool(t, drained))
	for _, m := range pickSequence(t, b, 2000) {
		switch {
		case m.Name() == drained[1].Name():
			t.Fatalf("picked %s, which is draining and not eligible for new flows", m.Name())
		case !slices.Contains(drained, m):
			t.Fatalf("picked %s, which was never in the pool", m.Name())
		}
	}
	// with every member draining no new flow is taken, and a pinned one still is
	drain(0)
	drain(2)
	b.SetPool(newPool(t, drained))
	if _, ok := b.Pick(f.next()); ok {
		t.Fatal("a pool whose every member is draining took a new flow")
	}
	pk, ok := b.Pick(pinnedTo(f.next(), drained[2]))
	if !ok || pk.Member() != drained[2] || !pk.Pinned() {
		t.Fatal("a flow pinned to a draining member did not reach it")
	}
	pk.Done(lb.OutcomeOK)
}

func testInflight(t suiteT, newSelector func() lb.Selector) {
	f := newFlows()
	members, _ := Members(1, 2, 1)
	sel := newSelector()
	b := lb.NewBalancer(sel, lb.BalancerOptions{Pool: newPool(t, members)})
	var open []lb.Pick
	for i := range 64 {
		// every other flow is pinned, which is accounted for as any other pick is
		flow := f.next()
		if i%2 == 1 {
			flow = pinnedTo(flow, members[i%len(members)])
		}
		pk, ok := b.Pick(flow)
		if !ok {
			t.Fatal("no pick")
		}
		pk.Established(time.Millisecond)
		pk.FirstByte()
		open = append(open, pk)
	}
	var inflight int64
	for _, m := range members {
		inflight += m.Stats().Inflight()
	}
	want := int64(0)
	if sel.Needs().Has(lb.NeedInflight) {
		want = int64(len(open))
	}
	if inflight != want {
		t.Errorf("in-flight while %d picks are open = %d, want %d", len(open), inflight, want)
	}
	for i, pk := range open {
		pk.Done(lb.Outcome(i % 4)) // #nosec G115 -- one of the four outcomes
	}
	for _, m := range members {
		if got := m.Stats().Inflight(); got != 0 {
			t.Errorf("%s holds %d in flight after every pick was done", m.Name(), got)
		}
	}
	// the zero Pick, as returned with false, is safe to report on
	var none lb.Pick
	none.Done(lb.OutcomeOK)
	if none.Member() != nil {
		t.Error("the zero pick has a member")
	}
}

func testAdversarial(t suiteT, newSelector func() lb.Selector) {
	done := make(chan struct{})
	var failure string
	go func() {
		defer close(done)
		f := newFlows()
		for _, weights := range [][]int{{1 << 30, 1}, {1, 1 << 30, 1 << 20, 1}, uniform(257)} {
			members, healths := Members(weights...)
			b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPoolQuiet(members)})
			// all but one ineligible, then all eligible
			for _, h := range healths[1:] {
				h.Set(failing)
			}
			for range 2000 {
				pk, ok := b.Pick(f.next())
				if !ok || pk.Member() != members[0] {
					failure = "did not pick the only eligible member"
					return
				}
				pk.Done(lb.OutcomeOK)
			}
			for _, h := range healths[1:] {
				h.Set(passing)
			}
			for range 20000 {
				pk, ok := b.Pick(f.next())
				if !ok || !slices.Contains(members, pk.Member()) {
					failure = "picked outside the snapshot"
					return
				}
				pk.Done(lb.OutcomeOK)
			}
			b.Pool().Stop()
		}
	}()
	select {
	case <-done:
		if failure != "" {
			t.Error(failure)
		}
	case <-time.After(terminationLimit):
		t.Fatal("selection did not terminate")
	}
}

// terminationLimit is how long the adversarial snapshots may take before selection is
// judged not to terminate
var terminationLimit = 30 * time.Second

// newPoolQuiet builds a pool off the test goroutine, where t.Fatal is not allowed
func newPoolQuiet(members []*lb.Member) *lb.Pool {
	p, err := lb.NewPool(members, floor)
	if err != nil {
		panic(err)
	}
	return p
}

func testZeroAlloc(t suiteT, newSelector func() lb.Selector) {
	for _, weights := range [][]int{uniform(6), {3, 1, 3, 1, 3, 1}} {
		members, _ := Members(weights...)
		b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
		keyed := newFlows().next()
		for _, flow := range []lb.Flow{keyed, pinnedTo(keyed, members[1])} {
			// the first pick of a snapshot prepares it
			if pk, ok := b.Pick(flow); ok {
				pk.Done(lb.OutcomeOK)
			}
			allocs := testing.AllocsPerRun(1000, func() {
				pk, ok := b.Pick(flow)
				if !ok {
					t.Fatal("no pick")
				}
				pk.Done(lb.OutcomeOK)
			})
			if allocs != 0 {
				t.Errorf("weights %v, pinned %v: a pick allocates %v times", weights, flow.HasPin, allocs)
			}
		}
	}
}

func testConcurrent(t suiteT, newSelector func() lb.Selector) {
	members, healths := Members(1, 2, 1, 3, 1, 1)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	stop := make(chan struct{})
	var churn sync.WaitGroup
	churn.Go(func() {
		pools := []*lb.Pool{}
		defer func() {
			for _, p := range pools {
				p.Stop()
			}
		}()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// member 0 stays up so a pick is always possible
			h := healths[1+i%(len(healths)-1)]
			h.Set(failing)
			if i%8 == 0 {
				p := newPoolQuiet(members[:2+i%(len(members)-1)])
				pools = append(pools, p)
				b.SetPool(p)
			}
			h.Set(passing)
		}
	})
	var pickers sync.WaitGroup
	for range 8 {
		pickers.Go(func() {
			f := newFlows()
			for range 5000 {
				pk, ok := b.Pick(f.next())
				if !ok {
					t.Error("no pick although one member never fails")
					return
				}
				if !slices.Contains(members, pk.Member()) {
					t.Error("picked a member that was never in the pool")
					return
				}
				pk.Done(lb.OutcomeOK)
			}
		})
	}
	pickers.Wait()
	close(stop)
	churn.Wait()
	for _, m := range members {
		if got := m.Stats().Inflight(); got != 0 {
			t.Errorf("%s holds %d in flight after every pick was done", m.Name(), got)
		}
	}
}

func pickSequence(t reporter, b *lb.Balancer, n int) []*lb.Member {
	t.Helper()
	f := newFlows()
	seq := make([]*lb.Member, n)
	for i := range seq {
		pk, ok := b.Pick(f.next())
		if !ok {
			t.Fatal("no pick")
		}
		seq[i] = pk.Member()
		pk.Done(lb.OutcomeOK)
	}
	return seq
}

// assertWindows fails unless every run of total-weight consecutive picks is exact
func assertWindows(t reporter, seq, members []*lb.Member) {
	t.Helper()
	var total int
	for _, m := range members {
		total += m.Weight()
	}
	counts := make(map[*lb.Member]int, len(members))
	for i, m := range seq {
		counts[m]++
		if i >= total {
			counts[seq[i-total]]--
		}
		if i < total-1 {
			continue
		}
		for _, want := range members {
			if counts[want] != want.Weight() {
				t.Fatalf("picks %d-%d gave %s %d, want exactly its weight %d",
					i-total+1, i, want.Name(), counts[want], want.Weight())
			}
		}
	}
}

func testExactWeights(t suiteT, newSelector func() lb.Selector) {
	// a pool large enough to leave any small-pool fast path a strategy may have
	large := make([]int, 67)
	for i := range large {
		large[i] = 1 + i%4
	}
	for _, weights := range [][]int{uniform(1), uniform(5), {4}, {1, 3, 2}, {7, 1, 1}, {2, 2}, {1, 1, 1, 9}, large} {
		members, _ := Members(weights...)
		b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
		var total int
		for _, w := range weights {
			total += w
		}
		assertWindows(t, pickSequence(t, b, 9*total+3), members)
	}
}

func testExactAfterWeightChange(t suiteT, newSelector func() lb.Selector) {
	members, healths := Members(1, 3, 2)
	b := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(t, members)})
	pickSequence(t, b, 17)
	// member 1 is rebuilt with a new weight, keeping its health and stats, in a new pool
	reweighted := slices.Clone(members)
	reweighted[1] = lb.NewMember(lb.MemberOptions{
		Name: members[1].Name(), Weight: 5, Health: healths[1], Stats: members[1].Stats(),
	})
	b.SetPool(newPool(t, reweighted))
	assertWindows(t, pickSequence(t, b, 40), reweighted)
	// a member that drops out leaves the rest exact among themselves
	healths[0].Set(failing)
	assertWindows(t, pickSequence(t, b, 40), reweighted[1:])
}

// Bench measures a pick at several pool sizes, uniform and weighted, from one goroutine and
// from many. A strategy should stay flat, or close to it, as the pool grows.
func Bench(b *testing.B, newSelector func() lb.Selector) {
	for _, n := range []int{2, 8, 64, 512} {
		for _, shape := range []string{"uniform", "weighted"} {
			weights := uniform(n)
			if shape == "weighted" {
				for i := 0; i < n; i += 2 {
					weights[i] = 3
				}
			}
			members, _ := Members(weights...)
			bal := lb.NewBalancer(newSelector(), lb.BalancerOptions{Pool: newPool(b, members)})
			name := shape + "/n=" + strconv.Itoa(n)
			b.Run(name, func(b *testing.B) {
				f := newFlows()
				b.ReportAllocs()
				for b.Loop() {
					pk, _ := bal.Pick(f.next())
					pk.Done(lb.OutcomeOK)
				}
			})
			b.Run(name+"/parallel", func(b *testing.B) {
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					f := newFlows()
					for pb.Next() {
						pk, _ := bal.Pick(f.next())
						pk.Done(lb.OutcomeOK)
					}
				})
			})
		}
	}
}
