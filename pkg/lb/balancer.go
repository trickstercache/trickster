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
	"math"
	"sync/atomic"
	"time"
)

// Pick is one committed selection, returned by value. Its holder reports what became of the
// flow; each report costs nothing unless the strategy declared a need for it.
type Pick struct {
	member   *Member
	balancer *Balancer
	// monotonic nanoseconds since the process's epoch; set only for a strategy that needs latency
	start int64
}

// epoch anchors the monotonic clock readings that picks are timed with
var epoch = time.Now()

func monotonic() int64 {
	return int64(time.Since(epoch))
}

// Member returns the selected member, whose Value is what the caller dispatches to.
func (p Pick) Member() *Member {
	return p.member
}

// Established reports that the member was reached and how long that took, as a latency
// sample. A caller reports whichever of Established and FirstByte is its latency signal.
func (p Pick) Established(d time.Duration) {
	if p.tracks(NeedLatency) {
		p.member.stats.observe(float64(d), time.Now().UnixNano(), p.balancer.decay)
	}
}

// FirstByte reports the first sign of a response from the member; the time since the pick is
// a latency sample.
func (p Pick) FirstByte() {
	if p.tracks(NeedLatency) {
		p.member.stats.observe(float64(monotonic()-p.start), time.Now().UnixNano(), p.balancer.decay)
	}
}

// Done reports that the flow ended. It must be called exactly once per Pick, including when
// the work panics, or the member's in-flight count leaks. A failed outcome records a latency
// penalty, so a member that fails fast never looks fast.
func (p Pick) Done(o Outcome) {
	if p.member == nil || p.balancer == nil || p.balancer.needs == 0 {
		return
	}
	st := p.member.stats
	if p.balancer.needs.Has(NeedInflight) {
		st.inflight.Add(-1)
	}
	switch o {
	case OutcomeOK:
		if st.fails.Load() != 0 {
			st.fails.Store(0)
		}
	case OutcomeFailed, OutcomeConnectFailed:
		st.fails.Add(1)
		if p.balancer.needs.Has(NeedLatency) {
			p.penalize(st)
		}
	case OutcomeCanceled:
	}
}

func (p Pick) tracks(n Needs) bool {
	return p.member != nil && p.balancer != nil && p.balancer.needs.Has(n)
}

// penalize records a sample no lower than the penalty, the time the failure took, and twice
// the current average, so repeated failures push a member further back, up to a ceiling
func (p Pick) penalize(st *Stats) {
	b := p.balancer
	sample := max(b.penalty, float64(monotonic()-p.start), 2*math.Float64frombits(st.latency.Load()))
	st.latency.Store(math.Float64bits(min(sample, b.penalty*penaltyCeiling)))
	st.stamp.Store(time.Now().UnixNano())
}

const (
	// DefaultLatencyDecay is the time constant of the latency average.
	DefaultLatencyDecay = 10 * time.Second
	// DefaultLatencyPenalty is the least latency a failed outcome is recorded as.
	DefaultLatencyPenalty = 5 * time.Second
	// penaltyCeiling caps a penalized average, as a multiple of the penalty
	penaltyCeiling = 12
)

// LatencyOptions tune how latency samples are averaged. Zero values take the defaults.
type LatencyOptions struct {
	// Decay is the time constant with which an average yields to lower samples.
	Decay time.Duration
	// Penalty is the least latency a failed outcome is recorded as.
	Penalty time.Duration
}

// LatencyTuner is optionally implemented by a Selector that needs latency, to tune how the
// balancer averages the samples it records for that strategy.
type LatencyTuner interface {
	Latency() LatencyOptions
}

// BalancerOptions are the optional settings of a Balancer.
type BalancerOptions struct {
	// Pool is the balancer's first pool; SetPool installs one later.
	Pool *Pool
}

// Balancer binds a strategy to a swappable pool. It is the Picker behind every pick-one
// mechanism, on any plane, and is safe for concurrent use.
type Balancer struct {
	selector Selector
	needs    Needs
	// nanoseconds, as float64 so the sampling path converts nothing
	decay    float64
	penalty  float64
	pool     atomic.Pointer[Pool]
	prepared atomic.Pointer[preparedSnapshot]
}

// preparedSnapshot pairs a Prepared with the snapshot it was built from. The pairing is by
// snapshot identity: generations restart with each pool, so they do not identify a snapshot.
type preparedSnapshot struct {
	snap     *Snapshot
	prepared Prepared
}

// NewBalancer returns a Balancer for selector, which it then owns.
func NewBalancer(selector Selector, opts ...BalancerOptions) *Balancer {
	b := &Balancer{
		selector: selector, needs: selector.Needs(),
		decay: float64(DefaultLatencyDecay), penalty: float64(DefaultLatencyPenalty),
	}
	if t, ok := selector.(LatencyTuner); ok {
		lo := t.Latency()
		if lo.Decay > 0 {
			b.decay = float64(lo.Decay)
		}
		if lo.Penalty > 0 {
			b.penalty = float64(lo.Penalty)
		}
	}
	if len(opts) > 0 && opts[0].Pool != nil {
		b.pool.Store(opts[0].Pool)
	}
	return b
}

// SetPool replaces the pool that picks are made from; nil leaves the balancer with none. The
// strategy's state carries over, so a rotation continues across a change of membership.
func (b *Balancer) SetPool(p *Pool) {
	b.pool.Store(p)
}

// Pool returns the balancer's current pool, or nil.
func (b *Balancer) Pool() *Pool {
	return b.pool.Load()
}

// Selector returns the balancer's strategy.
func (b *Balancer) Selector() Selector {
	return b.selector
}

// Needs returns the Needs of the balancer's strategy.
func (b *Balancer) Needs() Needs {
	return b.needs
}

// Pick commits one flow to an eligible member of the current pool.
func (b *Balancer) Pick(f Flow) (Pick, bool) {
	p := b.pool.Load()
	if p == nil {
		return Pick{}, false
	}
	snap := p.Snapshot()
	if len(snap.Members) == 0 {
		return Pick{}, false
	}
	ps := b.prepared.Load()
	if ps == nil || ps.snap != snap {
		ps = b.prepare(snap)
	}
	return b.commit(ps.prepared.Select(f))
}

// prepare is the rare path taken on the first pick of each snapshot. Racing callers build
// equal values; one that stores a superseded value is corrected by the next pick.
func (b *Balancer) prepare(snap *Snapshot) *preparedSnapshot {
	ps := &preparedSnapshot{snap: snap, prepared: b.selector.Prepare(snap)}
	b.prepared.Store(ps)
	return ps
}

func (b *Balancer) commit(m *Member) (Pick, bool) {
	if m == nil {
		return Pick{}, false
	}
	if b.needs == 0 {
		return Pick{member: m, balancer: b}, true
	}
	if b.needs.Has(NeedInflight) {
		m.stats.inflight.Add(1)
	}
	pk := Pick{member: m, balancer: b}
	if b.needs.Has(NeedLatency) {
		pk.start = monotonic()
	}
	return pk, true
}

// Repick commits the flow to an eligible member other than failed, for a caller retrying work
// that failed could not take. It is not a selection path: it prepares a one-off snapshot.
func (b *Balancer) Repick(f Flow, failed *Member) (Pick, bool) {
	p := b.pool.Load()
	if p == nil {
		return Pick{}, false
	}
	snap := p.Snapshot()
	others := make([]*Member, 0, len(snap.Members))
	for _, m := range snap.Members {
		if m != failed {
			others = append(others, m)
		}
	}
	if len(others) == 0 {
		return Pick{}, false
	}
	return b.commit(b.selector.Prepare(&Snapshot{Members: others, Gen: snap.Gen}).Select(f))
}
