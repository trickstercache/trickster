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
	"flag"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// recorder is a suiteT that notes which checks failed instead of failing a real test, so the
// suite can be shown to reject a strategy that breaks its contract
type recorder struct {
	mtx      sync.Mutex
	path     string
	failed   map[string][]string
	cleanups []func()
}

// fatal unwinds a check the way t.Fatal ends a test
type fatal struct{}

func newRecorder() *recorder {
	return &recorder{failed: make(map[string][]string)}
}

func (r *recorder) note(msg string) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.failed[r.path] = append(r.failed[r.path], msg)
}

func (r *recorder) Helper() {}

func (r *recorder) Error(args ...any) { r.note(fmt.Sprint(args...)) }

func (r *recorder) Errorf(format string, args ...any) { r.note(fmt.Sprintf(format, args...)) }

func (r *recorder) Fatal(args ...any) {
	r.note(fmt.Sprint(args...))
	panic(fatal{})
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.note(fmt.Sprintf(format, args...))
	panic(fatal{})
}

func (r *recorder) Cleanup(fn func()) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.cleanups = append(r.cleanups, fn)
}

func (r *recorder) Run(name string, check func(suiteT)) bool {
	r.mtx.Lock()
	r.path = name
	r.mtx.Unlock()
	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(fatal); !ok {
					panic(v)
				}
			}
		}()
		check(r)
	}()
	r.mtx.Lock()
	cleanups := r.cleanups
	r.cleanups = nil
	r.mtx.Unlock()
	for _, fn := range slices.Backward(cleanups) {
		fn()
	}
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return len(r.failed[name]) == 0
}

func (r *recorder) failures(check string) []string {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	return slices.Clone(r.failed[check])
}

func (r *recorder) failedChecks() []string {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	var out []string
	for name, msgs := range r.failed {
		if len(msgs) > 0 {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// broken is a strategy built from the ways one can break the contract. Left at its zero
// value it spreads flows by key, correctly.
type broken struct {
	name      string
	needs     lb.Needs
	stale     bool // keeps selecting from the first snapshot it ever saw
	first     bool // always selects the first member
	declines  bool // selects nothing
	allocates bool // allocates on the selection path
	hangs     bool // never returns from a selection over more than one member
	foreign   bool // selects a member that belongs to no pool, once the pool has company
	drifts    bool // changes what it needs between calls
	rotates   bool // plain rotation, which is exact only when it ignores no weights
	exact     bool // weighted rotation over contiguous spans: exact apportionment
	// keeps the weights it first saw for each member name, so it is exact until one changes
	staleWeights bool

	calls   atomic.Uint64
	members atomic.Pointer[[]*lb.Member]
	weights sync.Map
}

type brokenPrepared struct {
	s       *broken
	members []*lb.Member
	turns   []*lb.Member
}

var sink atomic.Pointer[[]byte]

var stranger = lb.NewMember(lb.MemberOptions{Name: "stranger"})

func (s *broken) Name() string { return s.name }

func (s *broken) Needs() lb.Needs {
	if s.drifts && s.calls.Add(1)%2 == 0 {
		return s.needs | lb.NeedKey
	}
	return s.needs
}

func (s *broken) Prepare(snap *lb.Snapshot) lb.Prepared {
	if s.stale {
		s.members.CompareAndSwap(nil, &snap.Members)
		return &brokenPrepared{s: s, members: *s.members.Load()}
	}
	p := &brokenPrepared{s: s, members: snap.Members}
	if !s.exact && !s.staleWeights {
		return p
	}
	var total int
	for _, m := range snap.Members {
		total += m.Weight()
	}
	if total > 1<<16 {
		// too long a rotation to lay out turn by turn; such pools are not checked for exactness
		return p
	}
	for _, m := range snap.Members {
		w := m.Weight()
		if s.staleWeights {
			first, _ := s.weights.LoadOrStore(m.Name(), w)
			w = first.(int)
		}
		// one entry per turn: a rotation over it gives each member exactly its weight
		for range w {
			p.turns = append(p.turns, m)
		}
	}
	return p
}

func (p *brokenPrepared) Select(f lb.Flow) *lb.Member {
	s := p.s
	switch {
	case s.declines:
		return nil
	case s.hangs && len(p.members) > 1:
		select {}
	case s.foreign && len(p.members) > 1:
		return stranger
	case s.first:
		return p.members[0]
	case s.rotates:
		return p.members[s.calls.Add(1)%uint64(len(p.members))]
	case len(p.turns) > 0:
		return p.turns[s.calls.Add(1)%uint64(len(p.turns))]
	}
	if s.allocates {
		b := make([]byte, 64)
		sink.Store(&b)
	}
	return p.members[f.Key%uint64(len(p.members))]
}

func suiteOf(mutate func(*broken)) func() lb.Selector {
	return func() lb.Selector {
		s := &broken{name: "broken"}
		if mutate != nil {
			mutate(s)
		}
		return s
	}
}

// the recorder must not be the reason a check passes or fails
func TestRecorderAcceptsAConformingStrategy(t *testing.T) {
	r := newRecorder()
	run(r, suiteOf(nil), Options{})
	if failed := r.failedChecks(); len(failed) != 0 {
		t.Fatalf("a conforming strategy failed %v: %v", failed, r.failures(failed[0]))
	}
	exact := newRecorder()
	run(exact, suiteOf(func(s *broken) { s.exact = true }), Options{ExactWeights: true})
	if failed := exact.failedChecks(); len(failed) != 0 {
		t.Fatalf("an exact strategy failed %v: %v", failed, exact.failures(failed[0]))
	}
	weighted := newRecorder()
	runWeighted(weighted, suiteOf(func(s *broken) { s.exact = true }), WeightOptions{Tolerance: 0.001})
	if failed := weighted.failedChecks(); len(failed) != 0 {
		t.Fatalf("an exact strategy failed %v: %v", failed, weighted.failures(failed[0]))
	}
	ran := false
	r.Run("cleanup order", func(t suiteT) {
		t.Cleanup(func() { ran = true })
	})
	if !ran {
		t.Error("the recorder dropped a cleanup")
	}
}

// each broken strategy must be rejected, by the check that exists to catch it
func TestSuiteRejectsBrokenStrategies(t *testing.T) {
	saved := terminationLimit
	terminationLimit = 250 * time.Millisecond
	t.Cleanup(func() { terminationLimit = saved })

	for _, test := range []struct {
		name   string
		break_ func(*broken)
		opts   Options
		checks []string
		says   string
		// hangs marks a strategy that only the check with a watchdog can be run against
		hangs bool
	}{
		{
			name: "has no name", break_: func(s *broken) { s.name = "" },
			checks: []string{"identity"}, says: "must have a name",
		},
		{
			name: "needs vary", break_: func(s *broken) { s.drifts = true },
			checks: []string{"identity"}, says: "needs must not vary",
		},
		{
			name: "selects nothing", break_: func(s *broken) { s.declines = true },
			checks: []string{
				"no eligible member", "picks only eligible members", "every member is reachable",
				"repick excludes the failed member", "in-flight accounting balances",
				"adversarial snapshots terminate", "zero allocations", "concurrent picks and swaps",
			},
			says: "was not picked",
		},
		{
			name: "selects outside the snapshot", break_: func(s *broken) { s.stale = true },
			checks: []string{"picks only eligible members", "repick excludes the failed member"},
			says:   "not eligible",
		},
		{
			name: "selects a member of no pool", break_: func(s *broken) { s.foreign = true },
			checks: []string{
				"picks only eligible members", "adversarial snapshots terminate", "concurrent picks and swaps",
			},
			says: "never in the pool",
		},
		{
			name: "starves members", break_: func(s *broken) { s.first = true },
			checks: []string{"every member is reachable"}, says: "was never picked",
		},
		{
			name: "allocates per pick", break_: func(s *broken) { s.allocates = true },
			checks: []string{"zero allocations"}, says: "allocates",
		},
		{
			name: "never returns", break_: func(s *broken) { s.hangs = true }, hangs: true,
			checks: []string{"adversarial snapshots terminate"}, says: "did not terminate",
		},
		{
			name: "claims exact weights it does not keep", break_: func(s *broken) { s.rotates = true },
			opts:   Options{ExactWeights: true},
			checks: []string{"exact weights", "exact weights after a live weight change"},
			says:   "want exactly its weight",
		},
		{
			// exact over a stable pool, so only the live-change check can catch it
			name: "misses a live weight change", break_: func(s *broken) { s.staleWeights = true },
			opts:   Options{ExactWeights: true},
			checks: []string{"exact weights after a live weight change"},
			says:   "want exactly its weight",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newRecorder()
			if test.hangs {
				r.Run("adversarial snapshots terminate", func(st suiteT) { testAdversarial(st, suiteOf(test.break_)) })
			} else {
				run(r, suiteOf(test.break_), test.opts)
			}
			failed := r.failedChecks()
			for _, check := range test.checks {
				if !slices.Contains(failed, check) {
					t.Errorf("%q passed a strategy that %s; checks that did fail: %v", check, test.name, failed)
				}
			}
			var said bool
			for _, check := range test.checks {
				for _, msg := range r.failures(check) {
					said = said || strings.Contains(msg, test.says)
				}
			}
			if !said {
				t.Errorf("no failure mentioned %q: %v", test.says, r.failed)
			}
		})
	}
}

// a strategy that ignores weights must fail every form of the weighted suite
func TestWeightedSuiteRejectsAStrategyThatIgnoresWeights(t *testing.T) {
	for name, o := range map[string]WeightOptions{
		"by load":   {},
		"by key":    {Keyed: true},
		"load only": {LoadOnly: true, Tolerance: 0.05},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRecorder()
			runWeighted(r, suiteOf(nil), o)
			failed := r.failedChecks()
			if len(failed) == 0 {
				t.Fatal("the weighted suite passed a strategy that spreads flows evenly whatever the weights")
			}
			for _, check := range failed {
				for _, msg := range r.failures(check) {
					if !strings.Contains(msg, "picks, want") && !strings.Contains(msg, "flows behind") {
						t.Errorf("%s: unexpected failure %q", check, msg)
					}
				}
			}
			if o.LoadOnly && slices.Contains(failed, "an idle pool is shared by weight") {
				t.Error("a load-only strategy was held to the idle split")
			}
		})
	}
	// and a strategy that lets one member fall behind is caught by its backlog
	r := newRecorder()
	runWeighted(r, suiteOf(func(s *broken) { s.first = true }), WeightOptions{LoadOnly: true})
	var backlog bool
	for _, msg := range r.failures("sustained load is shared by weight") {
		backlog = backlog || strings.Contains(msg, "flows behind")
	}
	if !backlog {
		t.Errorf("a member buried under every flow was not reported as behind: %v", r.failed)
	}
}

// a strategy that selects nothing fails the weighted suite outright, in every form
func TestWeightedSuiteRejectsAStrategyThatDeclines(t *testing.T) {
	for _, o := range []WeightOptions{{}, {Keyed: true}} {
		r := newRecorder()
		runWeighted(r, suiteOf(func(s *broken) { s.declines = true }), o)
		failed := r.failedChecks()
		want := 2
		if o.Keyed {
			want = 1
		}
		if len(failed) != want {
			t.Errorf("keyed %v: %d checks failed, want %d: %v", o.Keyed, len(failed), want, r.failed)
		}
		for _, check := range failed {
			if msgs := r.failures(check); len(msgs) != 1 || msgs[0] != "no pick" {
				t.Errorf("%s: %v", check, msgs)
			}
		}
	}
	// the exact-weights checks stop at the first pick that is refused, too
	r := newRecorder()
	run(r, suiteOf(func(s *broken) { s.declines = true }), Options{ExactWeights: true})
	for _, check := range []string{"exact weights", "exact weights after a live weight change"} {
		if msgs := r.failures(check); len(msgs) != 1 || msgs[0] != "no pick" {
			t.Errorf("%s: %v", check, msgs)
		}
	}
}

// Bench is part of the exported suite: it must run to completion over every shape it offers
func TestBenchRuns(t *testing.T) {
	saved := flag.Lookup("test.benchtime").Value.String()
	if err := flag.Set("test.benchtime", "1x"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = flag.Set("test.benchtime", saved) })
	var ran atomic.Int64
	res := testing.Benchmark(func(b *testing.B) {
		Bench(b, func() lb.Selector {
			ran.Add(1)
			return &broken{name: "benched"}
		})
	})
	if ran.Load() != 8 {
		t.Errorf("Bench built %d balancers, want one per size and shape", ran.Load())
	}
	_ = res
}
