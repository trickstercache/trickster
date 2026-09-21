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
// Package p2c is the power of two choices strategy: two members are drawn at random and the
// flow goes to the less loaded of them. It approximates least connections at a cost that does
// not grow with the pool.
package p2c

import (
	"math/rand/v2"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Name is the strategy's name.
const Name = "power_of_two_choices"

// New returns a power of two choices selector.
func New() lb.Selector {
	return selector{}
}

type selector struct{}

func (selector) Name() string { return Name }

func (selector) Needs() lb.Needs { return lb.NeedInflight }

func (selector) Prepare(snap *lb.Snapshot) lb.Prepared {
	members := snap.Members
	p := &prepared{members: members, n: uint64(len(members)), uniform: true}
	for _, m := range members {
		if m.Weight() != members[0].Weight() {
			p.uniform = false
			break
		}
	}
	if p.uniform {
		return p
	}
	// member i owns the span [cumulative[i-1], cumulative[i]) of the sampling space
	p.cumulative = make([]uint64, len(members))
	for i, m := range members {
		p.total += uint64(m.Weight()) // #nosec G115 -- a member's weight is at least 1
		p.cumulative[i] = p.total
	}
	return p
}

type prepared struct {
	members    []*lb.Member
	n          uint64
	uniform    bool
	cumulative []uint64
	total      uint64
}

func (p *prepared) Select(lb.Flow) *lb.Member {
	if p.n == 1 {
		return p.members[0]
	}
	r := rand.Uint64() // #nosec G404 -- load spreading, not a secret
	hi, lo := r>>32, r&0xffffffff
	var a, b *lb.Member
	if p.uniform {
		i := hi * p.n >> 32
		j := lo * (p.n - 1) >> 32
		if j >= i {
			j++
		}
		a, b = p.members[i], p.members[j]
	} else {
		a, b = p.weightedPair(hi, lo)
	}
	// the lower in-flight count per unit of weight wins, compared without dividing; a tie
	// goes to the first draw, which keeps an idle pool's split proportional to weight
	loadA := a.Stats().Inflight() * int64(b.Weight())
	loadB := b.Stats().Inflight() * int64(a.Weight())
	if loadB < loadA {
		return b
	}
	return a
}

// weightedPair draws two distinct members with probability proportional to weight: the
// second draw is made over the sampling space with the first member's span cut out
func (p *prepared) weightedPair(hi, lo uint64) (first, second *lb.Member) {
	i := p.find(scale(hi, p.total))
	var start uint64
	if i > 0 {
		start = p.cumulative[i-1]
	}
	width := p.cumulative[i] - start
	k := scale(lo, p.total-width)
	if k >= start {
		k += width
	}
	return p.members[i], p.members[p.find(k)]
}

// scale maps 32 random bits onto [0, n); exact to within one part in 2^32 for n below 2^32,
// and still in range above that
func scale(r32, n uint64) uint64 {
	if n>>32 == 0 {
		return r32 * n >> 32
	}
	return (r32<<32 | r32) % n
}

// find returns the member whose span holds k, which must be below total
func (p *prepared) find(k uint64) int {
	lo, hi := 0, len(p.cumulative)-1
	for lo < hi {
		mid := int(uint(lo+hi) >> 1) // #nosec G115 -- the sum of two slice indexes
		if p.cumulative[mid] > k {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}
