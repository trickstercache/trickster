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
// Package hrw is the highest random weight (rendezvous hashing) strategy: a flow's key and
// each member's name hash to a score, and the highest score wins. The same key reaches the
// same member from every process, and losing a member moves only that member's keys. It reads
// every member per pick.
package hrw

import (
	"math"
	"math/rand/v2"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Name is the strategy's name.
const Name = "highest_random_weight"

// New returns a highest random weight selector.
func New() lb.Selector {
	return selector{}
}

type selector struct{}

func (selector) Name() string { return Name }

func (selector) Needs() lb.Needs { return lb.NeedKey }

func (selector) Prepare(snap *lb.Snapshot) lb.Prepared {
	members := snap.Members
	p := &prepared{members: members, hashes: make([]uint64, len(members))}
	uniform := true
	for i, m := range members {
		p.hashes[i] = m.Hash()
		if m.Name() == "" {
			// unnamed members share one name hash; their position tells them apart
			p.hashes[i] = lb.Mix(uint64(i) + 1) // #nosec G115 -- a slice index
		}
		if m.Weight() != members[0].Weight() {
			uniform = false
		}
	}
	if !uniform {
		p.weights = make([]float64, len(members))
		for i, m := range members {
			p.weights[i] = float64(m.Weight())
		}
	}
	return p
}

type prepared struct {
	members []*lb.Member
	hashes  []uint64
	// nil when every member has the same weight, which skips the logarithm
	weights []float64
}

func (p *prepared) Select(f lb.Flow) *lb.Member {
	key := f.Key
	if !f.HasKey {
		// nothing identifies the flow, so it has no affinity to keep: spread it
		key = rand.Uint64() // #nosec G404 -- load spreading, not a secret
	}
	best := 0
	if p.weights == nil {
		var high uint64
		for i, h := range p.hashes {
			if s := lb.Mix(key ^ h); s > high || i == 0 {
				best, high = i, s
			}
		}
		return p.members[best]
	}
	// weighted rendezvous: the score -w/ln(u), with u uniform in (0,1), gives each member a
	// share of the key space proportional to its weight
	high := math.Inf(-1)
	for i, h := range p.hashes {
		u := (float64(lb.Mix(key^h)>>11) + 0.5) / (1 << 53)
		if s := -p.weights[i] / math.Log(u); s > high {
			best, high = i, s
		}
	}
	return p.members[best]
}
