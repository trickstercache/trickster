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
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeHealth is a Notifier with the contract the core asks for: callbacks run synchronously,
// outside its lock, and Unsubscribe never waits
type fakeHealth struct {
	status atomic.Int32
	mtx    sync.Mutex
	subs   []*fakeSub
}

type fakeSub struct {
	h    *fakeHealth
	fn   func(prev, next int32)
	dead atomic.Bool
}

func (s *fakeSub) Unsubscribe() {
	s.dead.Store(true)
	s.h.mtx.Lock()
	defer s.h.mtx.Unlock()
	s.h.subs = slices.DeleteFunc(slices.Clone(s.h.subs), func(o *fakeSub) bool { return o == s })
}

func newHealth(status int32) *fakeHealth {
	h := &fakeHealth{}
	h.status.Store(status)
	return h
}

func (h *fakeHealth) Get() int32 { return h.status.Load() }

func (h *fakeHealth) OnChange(fn func(prev, next int32)) Subscription {
	s := &fakeSub{h: h, fn: fn}
	h.mtx.Lock()
	defer h.mtx.Unlock()
	h.subs = append(slices.Clone(h.subs), s)
	return s
}

func (h *fakeHealth) set(next int32) {
	prev := h.status.Swap(next)
	h.mtx.Lock()
	subs := h.subs
	h.mtx.Unlock()
	for _, s := range subs {
		if !s.dead.Load() {
			s.fn(prev, next)
		}
	}
}

func (h *fakeHealth) subscribers() int {
	h.mtx.Lock()
	defer h.mtx.Unlock()
	return len(h.subs)
}

// plainHealth has no Notifier, so its pool follows it only through Refresh
type plainHealth struct{ status atomic.Int32 }

func (h *plainHealth) Get() int32 { return h.status.Load() }

type recordingObserver struct {
	mtx    sync.Mutex
	events []Event
}

func (o *recordingObserver) Observe(ev Event) {
	o.mtx.Lock()
	defer o.mtx.Unlock()
	o.events = append(o.events, ev)
}

func (o *recordingObserver) kinds(k EventKind) []Event {
	o.mtx.Lock()
	defer o.mtx.Unlock()
	var out []Event
	for _, ev := range o.events {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

func names(s *Snapshot) []string {
	out := make([]string, len(s.Members))
	for i, m := range s.Members {
		out[i] = m.Name()
	}
	return out
}

func mustPool(t *testing.T, members []*Member, floor int, opts ...PoolOptions) *Pool {
	t.Helper()
	p, err := NewPool(members, floor, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p
}

func TestNewMember(t *testing.T) {
	h := newHealth(1)
	payload := &struct{}{}
	m := NewMember(MemberOptions{Name: "a", Weight: 3, Health: h, Value: payload})
	if m.Name() != "a" || m.Group() != "a" || m.Weight() != 3 || m.Health() != h || m.Value != payload {
		t.Errorf("unexpected member: %+v", m)
	}
	if m.Stats() == nil {
		t.Fatal("a member always has stats")
	}
	st := m.Stats()
	if st.Inflight() != 0 || st.Latency() != 0 || st.Failures() != 0 || !st.LastSample().IsZero() {
		t.Error("fresh stats are not zero")
	}
	st.stamp.Store(42)
	if st.LastSample().UnixNano() != 42 {
		t.Error("last sample time was not reported")
	}
	grouped := NewMember(MemberOptions{Name: "a2", Group: "shard", Weight: -4, Stats: st})
	if grouped.Group() != "shard" || grouped.Weight() != 1 || grouped.Stats() != st {
		t.Errorf("unexpected member: %+v", grouped)
	}
	// the hash is a fixed function of the name: every process must agree on it
	if got := NewMember(MemberOptions{Name: "a"}).Hash(); got != 0xaf63dc4c8601ec8c || got != m.Hash() {
		t.Errorf("hash of %q = %#x", "a", got)
	}
	if grouped.Hash() == m.Hash() {
		t.Error("different names share a hash")
	}
}

func TestNewPoolRefusesBadMembership(t *testing.T) {
	a := NewMember(MemberOptions{Name: "a"})
	if _, err := NewPool([]*Member{a, nil}, 0); !errors.Is(err, ErrNilMember) {
		t.Errorf("nil member: %v", err)
	}
	if _, err := NewPool([]*Member{a, NewMember(MemberOptions{Name: "a"})}, 0); !errors.Is(err, ErrDuplicateMember) {
		t.Errorf("duplicate member: %v", err)
	}
	// unnamed members are anonymous, not duplicates of one another
	p := mustPool(t, []*Member{NewMember(MemberOptions{}), NewMember(MemberOptions{})}, 0)
	if len(p.Snapshot().Members) != 2 {
		t.Error("anonymous members were refused")
	}
	empty := mustPool(t, nil, 0)
	if s := empty.Snapshot(); s == nil || len(s.Members) != 0 || s.Gen != 1 {
		t.Errorf("empty pool snapshot = %+v", s)
	}
}

func TestPoolSnapshotFollowsHealth(t *testing.T) {
	ha, hb, hc := newHealth(1), newHealth(0), newHealth(-1)
	members := []*Member{
		NewMember(MemberOptions{Name: "a", Health: ha}),
		NewMember(MemberOptions{Name: "b", Health: hb}),
		NewMember(MemberOptions{Name: "c", Health: hc}),
		NewMember(MemberOptions{Name: "always"}),
	}
	obs := &recordingObserver{}
	p := mustPool(t, members, 1, PoolOptions{Observer: obs})
	if p.Len() != 4 || p.Floor() != 1 || len(p.Configured()) != 4 {
		t.Errorf("len %d floor %d", p.Len(), p.Floor())
	}
	p.Configured()[0] = nil
	if p.Configured()[0] != members[0] {
		t.Error("Configured exposed the pool's own slice")
	}
	first := p.Snapshot()
	if got := names(first); !slices.Equal(got, []string{"a", "always"}) || first.Gen != 1 {
		t.Fatalf("initial snapshot = %v gen %d", got, first.Gen)
	}

	// a transition across the floor is visible to the very next Snapshot, in pool order
	hc.set(1)
	if got := names(p.Snapshot()); !slices.Equal(got, []string{"a", "c", "always"}) {
		t.Fatalf("after c passes = %v", got)
	}
	if got := names(first); !slices.Equal(got, []string{"a", "always"}) {
		t.Errorf("a published snapshot was modified: %v", got)
	}

	// a transition that stays on one side of the floor publishes nothing
	before := p.Snapshot()
	hb.set(-1)
	hb.set(-2)
	hb.set(0)
	ha.set(1)
	if p.Snapshot() != before {
		t.Error("a transition that did not cross the floor republished")
	}
	ha.set(0)
	if got := p.Snapshot(); !slices.Equal(names(got), []string{"c", "always"}) || got.Gen != 3 {
		t.Errorf("after a falls = %v gen %d", names(got), got.Gen)
	}
	snaps := obs.kinds(EventSnapshot)
	if len(snaps) != 3 || snaps[2].Gen != 3 || snaps[2].Eligible != 2 || snaps[2].Configured != 4 {
		t.Errorf("snapshot events = %+v", snaps)
	}
}

func TestPoolWithoutNotifierFollowsRefresh(t *testing.T) {
	h := &plainHealth{}
	p := mustPool(t, []*Member{NewMember(MemberOptions{Name: "a", Health: h})}, 1)
	h.status.Store(1)
	if len(p.Snapshot().Members) != 0 {
		t.Fatal("a pool without a Notifier cannot have seen the change")
	}
	p.Refresh()
	if len(p.Snapshot().Members) != 1 {
		t.Error("Refresh did not pick up the change")
	}
}

func TestPoolStop(t *testing.T) {
	h := newHealth(1)
	p := mustPool(t, []*Member{NewMember(MemberOptions{Name: "a", Health: h})}, 1)
	if h.subscribers() != 1 {
		t.Fatalf("subscribers = %d", h.subscribers())
	}
	last := p.Snapshot()
	p.Stop()
	p.Stop()
	if h.subscribers() != 0 {
		t.Error("Stop left a subscription behind")
	}
	h.set(-1)
	p.Refresh()
	// a callback captured before Stop may still arrive; it must publish nothing
	p.onChange(1, -1)
	if p.Snapshot() != last {
		t.Error("a stopped pool republished")
	}
}

// a callback may stop the pool that is calling it
func TestPoolStopFromInsideACallback(t *testing.T) {
	h := newHealth(1)
	p := mustPool(t, []*Member{NewMember(MemberOptions{Name: "a", Health: h})}, 1)
	h.OnChange(func(_, _ int32) { p.Stop() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.set(-1)
		h.set(1)
	}()
	<-done
	if h.subscribers() != 1 {
		t.Errorf("subscribers = %d, want only the test's own", h.subscribers())
	}
}

// many goroutines flipping many members never publish out of order, and the last snapshot
// matches the final statuses
func TestPoolTransitionStorm(t *testing.T) {
	const n = 12
	healths := make([]*fakeHealth, n)
	members := make([]*Member, n)
	for i := range n {
		healths[i] = newHealth(-1)
		members[i] = NewMember(MemberOptions{Name: string(rune('a' + i)), Health: healths[i]})
	}
	p := mustPool(t, members, 1)
	stop := make(chan struct{})
	var watcher sync.WaitGroup
	watcher.Go(func() {
		var last uint64
		for {
			select {
			case <-stop:
				return
			default:
			}
			if gen := p.Snapshot().Gen; gen < last {
				t.Errorf("generation went backwards: %d after %d", gen, last)
				return
			} else {
				last = gen
			}
		}
	})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			for range 300 {
				healths[i].set(1)
				healths[i].set(-1)
			}
			if i%3 == 0 {
				healths[i].set(1)
			}
		})
	}
	wg.Wait()
	close(stop)
	watcher.Wait()
	var want []string
	for i := 0; i < n; i += 3 {
		want = append(want, members[i].Name())
	}
	if got := names(p.Snapshot()); !slices.Equal(got, want) {
		t.Errorf("final snapshot = %v, want %v", got, want)
	}
}

// a transition that lands between subscription and the first build is never lost
func TestPoolTransitionRacingConstruction(t *testing.T) {
	for range 500 {
		h := newHealth(-1)
		var wg sync.WaitGroup
		wg.Go(func() { h.set(1) })
		p, err := NewPool([]*Member{NewMember(MemberOptions{Name: "a", Health: h})}, 1)
		if err != nil {
			t.Fatal(err)
		}
		wg.Wait()
		got := len(p.Snapshot().Members)
		p.Stop()
		if got != 1 {
			t.Fatalf("a transition racing construction was lost: %d members", got)
		}
	}
}

type panickyHealth struct{ armed atomic.Bool }

func (h *panickyHealth) Get() int32 {
	if h.armed.Load() {
		panic("health source blew up")
	}
	return 1
}

// a panic while rebuilding is recovered and reported, leaves the last snapshot in place, and
// does not wedge the pool
func TestPoolRecoversRebuildPanic(t *testing.T) {
	h := &panickyHealth{}
	obs := &recordingObserver{}
	p := mustPool(t, []*Member{NewMember(MemberOptions{Name: "a", Health: h})}, 1, PoolOptions{Observer: obs})
	last := p.Snapshot()
	h.armed.Store(true)
	p.Refresh()
	if p.Snapshot() != last {
		t.Error("a failed rebuild replaced the snapshot")
	}
	panics := obs.kinds(EventPanic)
	if len(panics) != 1 || panics[0].Panic != "health source blew up" || len(panics[0].Stack) == 0 {
		t.Fatalf("panic events = %+v", panics)
	}
	h.armed.Store(false)
	p.Refresh()
	if got := p.Snapshot(); got == last || got.Gen != last.Gen+1 {
		t.Error("the pool did not rebuild after a recovered panic")
	}
	// a panic with no observer is still recovered
	quiet := mustPool(t, []*Member{NewMember(MemberOptions{Name: "a", Health: h})}, 1)
	h.armed.Store(true)
	quiet.Refresh()
}

func TestSnapshotZeroAlloc(t *testing.T) {
	p := mustPool(t, []*Member{NewMember(MemberOptions{Name: "a"})}, 0)
	if allocs := testing.AllocsPerRun(1000, func() { _ = p.Snapshot() }); allocs != 0 {
		t.Errorf("Snapshot allocates %v", allocs)
	}
}

func BenchmarkPoolSnapshot(b *testing.B) {
	members := make([]*Member, 8)
	for i := range members {
		members[i] = NewMember(MemberOptions{Name: string(rune('a' + i)), Health: newHealth(1)})
	}
	p, err := NewPool(members, 1)
	if err != nil {
		b.Fatal(err)
	}
	defer p.Stop()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if len(p.Snapshot().Members) != len(members) {
				b.Fatal("short snapshot")
			}
		}
	})
}
