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
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
)

var (
	// ErrNilMember is returned by NewPool for a nil member.
	ErrNilMember = errors.New("lb: nil pool member")
	// ErrDuplicateMember is returned by NewPool when two members share a name.
	ErrDuplicateMember = errors.New("lb: duplicate pool member name")
)

// Snapshot is an immutable view of a pool's eligible members. Holders may retain it, and must
// not modify it.
type Snapshot struct {
	// Members holds only the members whose status met the floor, in pool order
	Members []*Member
	// Gen increases with each snapshot a pool publishes, starting at 1
	Gen uint64
}

// PoolOptions are the optional settings of a Pool.
type PoolOptions struct {
	// Observer receives the pool's events; nil discards them.
	Observer Observer
}

// Pool is a fixed membership whose eligible subset is republished as member health changes.
// Reading the subset costs one atomic load.
type Pool struct {
	members  []*Member
	floor    int32
	observer Observer
	snap     atomic.Pointer[Snapshot]
	// held across read-build-publish so snapshots are published in order
	mtx     sync.Mutex
	gen     uint64
	stopped bool
	subs    []Subscription
}

// NewPool returns a started Pool over members, whose snapshots hold the members with a health
// status of at least floor. Membership is fixed: a change of membership is a new Pool.
func NewPool(members []*Member, floor int, opts ...PoolOptions) (*Pool, error) {
	names := make(map[string]struct{}, len(members))
	for _, m := range members {
		if m == nil {
			return nil, ErrNilMember
		}
		if m.name == "" {
			continue
		}
		if _, dup := names[m.name]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateMember, m.name)
		}
		names[m.name] = struct{}{}
	}
	p := &Pool{members: slices.Clone(members), floor: clampFloor(floor)}
	if len(opts) > 0 {
		p.observer = opts[0].Observer
	}
	p.snap.Store(&Snapshot{})
	// subscribe before the first build, so a transition between the two forces a rebuild
	// that queues behind the build rather than being lost
	subs := make([]Subscription, 0, len(p.members))
	for _, m := range p.members {
		if n, ok := m.health.(Notifier); ok {
			subs = append(subs, n.OnChange(p.onChange))
		}
	}
	p.mtx.Lock()
	p.subs = subs
	p.mtx.Unlock()
	p.Refresh()
	return p, nil
}

func clampFloor(floor int) int32 {
	const lo, hi = -1 << 31, 1<<31 - 1
	return int32(min(max(floor, lo), hi)) // #nosec G115 -- clamped to the int32 range
}

// Snapshot returns the pool's current eligible members; never nil.
func (p *Pool) Snapshot() *Snapshot {
	return p.snap.Load()
}

// Configured returns every member of the pool, eligible or not, in pool order.
func (p *Pool) Configured() []*Member {
	return slices.Clone(p.members)
}

// Len returns the number of configured members.
func (p *Pool) Len() int {
	return len(p.members)
}

// Floor returns the minimum health status of an eligible member.
func (p *Pool) Floor() int {
	return int(p.floor)
}

// onChange rebuilds only when a transition carries the member across the floor
func (p *Pool) onChange(prev, next int32) {
	if (prev >= p.floor) == (next >= p.floor) {
		return
	}
	p.Refresh()
}

// Refresh rebuilds and publishes the snapshot from the members' current statuses. A pool
// whose members announce their transitions does this on its own. No-op once stopped.
func (p *Pool) Refresh() {
	if ev, ok := p.rebuild(); ok && p.observer != nil {
		p.observer.Observe(ev)
	}
}

func (p *Pool) rebuild() (ev Event, ok bool) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	defer func() {
		if r := recover(); r != nil {
			ev, ok = Event{Kind: EventPanic, Panic: r, Stack: debug.Stack()}, true
		}
	}()
	if p.stopped {
		return Event{}, false
	}
	// statuses are read here, not taken from a callback's arguments, which may be stale
	eligible := make([]*Member, 0, len(p.members))
	for _, m := range p.members {
		if m.eligible(p.floor) {
			eligible = append(eligible, m)
		}
	}
	p.gen++
	p.snap.Store(&Snapshot{Members: eligible, Gen: p.gen})
	return Event{Kind: EventSnapshot, Gen: p.gen, Eligible: len(eligible), Configured: len(p.members)}, true
}

// Stop ends the pool's subscriptions. Its last snapshot stays readable and is never replaced.
func (p *Pool) Stop() {
	p.mtx.Lock()
	subs := p.subs
	p.subs = nil
	p.stopped = true
	p.mtx.Unlock()
	for _, s := range subs {
		s.Unsubscribe()
	}
}
