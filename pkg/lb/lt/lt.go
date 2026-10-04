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
// Package lt is the least time strategy: each flow goes to the member with the lowest
// latency average, scaled by its flows in flight and its weight. It reads every member per pick.
//
// A weight is a bias, not a share: it divides the member's score, so under load the split
// follows the weights, while an idle pool gives every flow to its best-scoring member.
//
// An average fades while its member is passed over, so a member ranked behind its peers on
// an old sample, or on a failure's penalty, is tried again rather than left there for good.
package lt

import (
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// Name is the strategy's name.
const Name = "least_time"

// Options tune the strategy. Zero values take the core's defaults.
type Options struct {
	// Decay is the time constant with which a member's latency average yields to lower
	// samples, and with which a failure's penalty fades while the member gets no work.
	Decay time.Duration
	// Penalty is the least latency a failed flow is recorded as.
	Penalty time.Duration
}

// New returns a least time selector.
func New(o Options) lb.Selector {
	s := &selector{opts: o}
	if s.opts.Decay <= 0 {
		s.opts.Decay = lb.DefaultLatencyDecay
	}
	if s.opts.Penalty <= 0 {
		s.opts.Penalty = lb.DefaultLatencyPenalty
	}
	return s
}

type selector struct {
	opts Options
	// rotation among members that tie, which is all of them until one has a sample
	pos atomic.Uint64
}

func (s *selector) Name() string { return Name }

func (s *selector) Needs() lb.Needs { return lb.NeedInflight | lb.NeedLatency }

// Latency tells the balancer how to average the samples it records for this strategy.
func (s *selector) Latency() lb.LatencyOptions {
	return lb.LatencyOptions{Decay: s.opts.Decay, Penalty: s.opts.Penalty}
}

func (s *selector) Prepare(snap *lb.Snapshot) lb.Prepared {
	return &prepared{pos: &s.pos, members: snap.Members, decay: s.opts.Decay}
}

type prepared struct {
	pos     *atomic.Uint64
	members []*lb.Member
	decay   time.Duration
}

func (p *prepared) Select(lb.Flow) *lb.Member {
	// a member with no sample yet is scored as its fastest healthy peer: it ties with the
	// best rather than beating it, so it shares that member's flows until its own first
	// sample ranks it, however idle the pool is. Its flows in flight bound the burst.
	now, decay := time.Now(), p.decay
	var best, bestAny float64
	for _, m := range p.members {
		st := m.Stats()
		l := float64(st.Faded(now, decay))
		if l <= 0 {
			continue
		}
		if bestAny == 0 || l < bestAny {
			bestAny = l
		}
		if st.Failures() == 0 && (best == 0 || l < best) {
			best = l
		}
	}
	cold := 1.0
	switch {
	case best > 0:
		cold = best
	case bestAny > 0:
		cold = bestAny
	}
	return lb.Least(p.members, p.pos, func(m *lb.Member) float64 {
		st := m.Stats()
		latency := float64(st.Faded(now, decay))
		if latency <= 0 {
			latency = cold
		}
		return latency * float64(st.Inflight()+1) / float64(m.Weight())
	})
}
