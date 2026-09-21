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
// Package rr is the round robin strategy: a lock-free rotation that gives each member exactly
// its weight of every total-weight consecutive selections, with a heavier member's turns
// spread through the rotation rather than taken back to back.
package rr

import (
	"math/bits"
	"math/rand/v2"
	"slices"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Name is the strategy's name.
const Name = "round_robin"

const (
	// scheduleMax is the largest total weight whose rotation is laid out as a table, one
	// entry per turn; larger totals are walked by stride instead
	scheduleMax = 4096
	// linearScanMax is the pool size up to which scanning the spans beats a binary search
	linearScanMax = 16
)

// New returns a round robin selector whose rotation starts at a random turn, so that
// replicas started together do not all send their first flows to the same member.
func New() lb.Selector {
	return NewAt(rand.Uint64()) // #nosec G404 -- a starting offset, not a secret
}

// NewAt returns a round robin selector whose rotation starts after the provided turn, for a
// caller that needs a reproducible sequence.
func NewAt(start uint64) lb.Selector {
	s := &selector{}
	s.pos.Store(start)
	return s
}

type selector struct {
	// the rotation outlives snapshots, so a change of membership does not restart it
	pos atomic.Uint64
}

func (s *selector) Name() string { return Name }

func (s *selector) Needs() lb.Needs { return 0 }

func (s *selector) Prepare(snap *lb.Snapshot) lb.Prepared {
	members := snap.Members
	var total uint64
	uniform := true
	for _, m := range members {
		total += uint64(m.Weight()) // #nosec G115 -- a member's weight is at least 1
		if m.Weight() != members[0].Weight() {
			uniform = false
		}
	}
	if uniform {
		return &rotation{pos: &s.pos, members: members, n: uint64(len(members))}
	}
	if total <= scheduleMax {
		return &schedule{pos: &s.pos, turns: layout(members, total), total: total}
	}
	// member i owns the span [cumulative[i-1], cumulative[i]) of the rotation
	cumulative := make([]uint64, len(members))
	var sum uint64
	for i, m := range members {
		sum += uint64(m.Weight()) // #nosec G115 -- at least 1
		cumulative[i] = sum
	}
	return &strided{pos: &s.pos, members: members, cumulative: cumulative, total: total, stride: stride(total)}
}

// rotation serves members of one weight in turn
type rotation struct {
	pos     *atomic.Uint64
	members []*lb.Member
	n       uint64
}

func (r *rotation) Select(lb.Flow) *lb.Member {
	return r.members[r.pos.Add(1)%r.n]
}

// schedule is one full rotation laid out turn by turn. Any total consecutive turns are one
// pass over it from some offset, so each member is selected exactly its weight of them.
type schedule struct {
	pos   *atomic.Uint64
	turns []*lb.Member
	total uint64
}

func (s *schedule) Select(lb.Flow) *lb.Member {
	return s.turns[s.pos.Add(1)%s.total]
}

// layout spaces each member's turns evenly through the rotation, and staggers the members so
// that those of one weight do not all come due together: member i of n has its j-th turn due
// at (j + (i + 1/2) / n) * total / weight, and turns are taken in order of when they are due
func layout(members []*lb.Member, total uint64) []*lb.Member {
	// a turn is due at numerator / (2 * n * weight), kept as the fraction's parts so that
	// two turns compare by cross-multiplying rather than by dividing
	type turn struct {
		numerator uint64
		weight    uint64
		member    int
	}
	turns := make([]turn, 0, total)
	n := uint64(len(members))
	for i, m := range members {
		w := uint64(m.Weight()) // #nosec G115 -- at least 1
		for j := range w {
			phase := 2*(j*n+uint64(i)) + 1 // #nosec G115 -- a slice index
			turns = append(turns, turn{numerator: phase * total, weight: w, member: i})
		}
	}
	// the products are at most about 4 * scheduleMax^4, inside 64 bits
	slices.SortStableFunc(turns, func(a, b turn) int {
		l, r := a.numerator*b.weight, b.numerator*a.weight
		switch {
		case l < r:
			return -1
		case l > r:
			return 1
		}
		return a.member - b.member
	})
	out := make([]*lb.Member, len(turns))
	for i, t := range turns {
		out[i] = members[t.member]
	}
	return out
}

// strided walks a rotation too long to lay out: turn c lands on position c*stride mod total.
// The stride shares no factor with total, so total consecutive turns land on every position
// once, which keeps the apportionment exact; near total/phi, it also scatters them evenly.
type strided struct {
	pos        *atomic.Uint64
	members    []*lb.Member
	cumulative []uint64
	total      uint64
	stride     uint64
}

func (r *strided) Select(lb.Flow) *lb.Member {
	hi, lo := bits.Mul64(r.pos.Add(1)%r.total, r.stride)
	_, k := bits.Div64(hi, lo, r.total)
	if len(r.cumulative) <= linearScanMax {
		for i, end := range r.cumulative {
			if end > k {
				return r.members[i]
			}
		}
	}
	// binary search for the first member whose span ends beyond k; k < total, so one does
	low, high := 0, len(r.cumulative)-1
	for low < high {
		mid := int(uint(low+high) >> 1) // #nosec G115 -- the sum of two slice indexes
		if r.cumulative[mid] > k {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return r.members[low]
}

// stride returns the number nearest total/phi that shares no factor with total. The search
// ends by the time it has widened to 1, which shares a factor with nothing.
func stride(total uint64) uint64 {
	const invPhi = 0.6180339887498949
	ideal := uint64(float64(total) * invPhi)
	for d := uint64(0); ; d++ {
		for _, s := range [2]uint64{ideal + d, ideal - d} {
			if s >= 1 && s < total && gcd(s, total) == 1 {
				return s
			}
		}
	}
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
