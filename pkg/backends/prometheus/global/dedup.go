/*
 * Copyright 2018 The Trickster Authors
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

package global

import (
	"cmp"
	"math"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/merge"

	"github.com/prometheus/prometheus/model/histogram"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
)

// initialPenalty is how far, in milliseconds, penalty deduplication pushes the replicas it didn't pick past
// the first sample, before it knows the sampling interval
const initialPenalty = 5000

// counterFunctions are the select hints whose input penalty deduplication treats as counters
var counterFunctions = map[string]struct{}{"rate": {}, "increase": {}, "irate": {}, "resets": {}}

// replica is one complete label set of a canonical series, with its copies in member order
type replica struct {
	lset   labels.Labels
	member int
	copies []storage.Series
}

type dedupGroup struct {
	lset     labels.Labels
	replicas []*replica
}

// Deduplicate returns one series per label set left without replicaLabels, sorted; copies keep
// every timestamp, earlier members winning ties, and replicas combine per deduplication.
func Deduplicate(members [][]storage.Series, replicaLabels []string, deduplication, function string) []storage.Series {
	groups := make(map[uint64][]*dedupGroup)
	var order []*dedupGroup
	var builder *labels.Builder
	if len(replicaLabels) > 0 {
		builder = labels.NewBuilder(labels.EmptyLabels())
	}
	for member, list := range members {
		for _, s := range list {
			full := s.Labels()
			canonical := full
			if builder != nil {
				builder.Reset(full)
				canonical = builder.Del(replicaLabels...).Labels()
			}
			g := findGroup(groups, canonical)
			if g == nil {
				g = &dedupGroup{lset: canonical}
				groups[canonical.Hash()] = append(groups[canonical.Hash()], g)
				order = append(order, g)
			}
			r := g.findReplica(full)
			if r == nil {
				r = &replica{lset: full, member: member}
				g.replicas = append(g.replicas, r)
			}
			r.copies = append(r.copies, s)
		}
	}
	slices.SortFunc(order, func(a, b *dedupGroup) int { return labels.Compare(a.lset, b.lset) })
	_, counter := counterFunctions[function]
	penalty := deduplication != merge.TSMDeduplicationChain
	out := make([]storage.Series, len(order))
	for i, g := range order {
		// earlier members take precedence, then replica label values in configured order, so a copy
		// without a replica label precedes one with it
		slices.SortFunc(g.replicas, func(a, b *replica) int {
			if c := cmp.Compare(a.member, b.member); c != 0 {
				return c
			}
			for _, name := range replicaLabels {
				if c := cmp.Compare(a.lset.Get(name), b.lset.Get(name)); c != 0 {
					return c
				}
			}
			return labels.Compare(a.lset, b.lset)
		})
		out[i] = &dedupSeries{group: g, penalty: penalty, counter: counter}
	}
	return out
}

func findGroup(groups map[uint64][]*dedupGroup, lset labels.Labels) *dedupGroup {
	for _, g := range groups[lset.Hash()] {
		if labels.Equal(g.lset, lset) {
			return g
		}
	}
	return nil
}

func (g *dedupGroup) findReplica(lset labels.Labels) *replica {
	for _, r := range g.replicas {
		if labels.Equal(r.lset, lset) {
			return r
		}
	}
	return nil
}

// dedupSeries is one canonical series combined from its replicas and their copies
type dedupSeries struct {
	group   *dedupGroup
	penalty bool
	counter bool
}

func (s *dedupSeries) Labels() labels.Labels { return s.group.lset }

func (s *dedupSeries) Iterator(chunkenc.Iterator) chunkenc.Iterator {
	replicas := make([]chunkenc.Iterator, len(s.group.replicas))
	for i, r := range s.group.replicas {
		replicas[i] = copiesIterator(r.copies)
	}
	if len(replicas) == 1 {
		return replicas[0]
	}
	if !s.penalty {
		return newChainIterator(replicas)
	}
	it := s.adjustable(replicas[0])
	for _, next := range replicas[1:] {
		it = newPenaltyIterator(it, s.adjustable(next))
	}
	return it
}

func (s *dedupSeries) adjustable(it chunkenc.Iterator) adjustableIterator {
	if s.counter {
		return &counterAdjustIterator{Iterator: it}
	}
	return noAdjustIterator{Iterator: it}
}

func copiesIterator(copies []storage.Series) chunkenc.Iterator {
	if len(copies) == 1 {
		return copies[0].Iterator(nil)
	}
	its := make([]chunkenc.Iterator, len(copies))
	for i, c := range copies {
		its[i] = c.Iterator(nil)
	}
	return newChainIterator(its)
}

// chainIterator yields every distinct timestamp of its iterators; at a timestamp several hold, the
// earliest-listed iterator's sample wins
type chainIterator struct {
	its     []chunkenc.Iterator
	types   []chunkenc.ValueType
	cur     int
	started bool
	// cons is whether the current sample follows the one returned before it in the same source
	cons bool
}

func newChainIterator(its []chunkenc.Iterator) *chainIterator {
	return &chainIterator{its: its, types: make([]chunkenc.ValueType, len(its)), cur: -1}
}

func (c *chainIterator) Next() chunkenc.ValueType {
	prev := c.cur
	switch {
	case !c.started:
		c.started = true
		for i, it := range c.its {
			c.types[i] = it.Next()
		}
	case c.cur < 0:
		return chunkenc.ValNone
	default:
		t := c.its[c.cur].AtT()
		for i, it := range c.its {
			if c.types[i] != chunkenc.ValNone && it.AtT() == t {
				c.types[i] = it.Next()
			}
		}
	}
	vt := c.pick()
	// only the iterator whose sample was just returned moves to its next one, so staying on it keeps
	// its source's order; any other's next sample never followed the one returned
	c.cons = vt != chunkenc.ValNone && prev >= 0 && c.cur == prev && isConsecutive(c.its[c.cur])
	return vt
}

func (c *chainIterator) Seek(t int64) chunkenc.ValueType {
	if c.started && c.cur < 0 {
		return chunkenc.ValNone
	}
	if c.started && c.its[c.cur].AtT() >= t {
		return c.types[c.cur]
	}
	for i, it := range c.its {
		if !c.started || c.types[i] != chunkenc.ValNone {
			c.types[i] = it.Seek(t)
		}
	}
	c.started = true
	// samples were passed over, so the one returned may not follow the last one returned
	c.cons = false
	return c.pick()
}

// pick selects the iterator with the earliest current timestamp, the earliest-listed winning a tie
func (c *chainIterator) pick() chunkenc.ValueType {
	c.cur = -1
	var best int64
	for i, vt := range c.types {
		if vt == chunkenc.ValNone {
			continue
		}
		if t := c.its[i].AtT(); c.cur < 0 || t < best {
			c.cur, best = i, t
		}
	}
	if c.cur < 0 {
		return chunkenc.ValNone
	}
	return c.types[c.cur]
}

func (c *chainIterator) At() (int64, float64) { return c.its[c.cur].At() }

func (c *chainIterator) AtHistogram(h *histogram.Histogram) (int64, *histogram.Histogram) {
	t, out := c.its[c.cur].AtHistogram(h)
	if !c.cons {
		out = withUnknownReset(out, h)
	}
	return t, out
}

func (c *chainIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	t, out := c.its[c.cur].AtFloatHistogram(fh)
	if !c.cons {
		out = withUnknownFloatReset(out, fh)
	}
	return t, out
}

func (c *chainIterator) consecutive() bool { return c.cons }

func (c *chainIterator) AtT() int64  { return c.its[c.cur].AtT() }
func (c *chainIterator) AtST() int64 { return c.its[c.cur].AtST() }

func (c *chainIterator) Err() error {
	for _, it := range c.its {
		if err := it.Err(); err != nil {
			return err
		}
	}
	return nil
}

// adjustableIterator is a replica penalty deduplication can switch to; switching to a counter
// replica lifts its values so the switch doesn't read as a counter reset
type adjustableIterator interface {
	chunkenc.Iterator
	adjustAtValue(last float64)
}

type noAdjustIterator struct {
	chunkenc.Iterator
}

func (noAdjustIterator) adjustAtValue(float64) {}

func (n noAdjustIterator) consecutive() bool { return isConsecutive(n.Iterator) }

// counterAdjustIterator raises a counter replica's values by what earlier switches found it behind
type counterAdjustIterator struct {
	chunkenc.Iterator
	vt     chunkenc.ValueType
	adjust float64
}

func (c *counterAdjustIterator) Next() chunkenc.ValueType {
	c.vt = c.Iterator.Next()
	return c.vt
}

func (c *counterAdjustIterator) Seek(t int64) chunkenc.ValueType {
	c.vt = c.Iterator.Seek(t)
	return c.vt
}

func (c *counterAdjustIterator) At() (int64, float64) {
	t, v := c.Iterator.At()
	return t, v + c.adjust
}

func (c *counterAdjustIterator) consecutive() bool { return isConsecutive(c.Iterator) }

func (c *counterAdjustIterator) adjustAtValue(last float64) {
	if c.vt != chunkenc.ValFloat {
		return
	}
	if _, v := c.At(); last > v {
		c.adjust += last - v
	}
}

// penaltyIterator follows one of two replicas until it has a gap, seeking the other past twice the
// last sampling interval so interleaved scrapes never raise the sampling frequency
type penaltyIterator struct {
	a, b       adjustableIterator
	aval, bval chunkenc.ValueType
	vt         chunkenc.ValueType
	lastT      int64
	lastIter   adjustableIterator
	penA, penB int64
	useA       bool
	started    bool
	// lastFloat is the last float sample returned, which a replica switched to is adjusted against
	lastFloat float64
	hasFloat  bool
	// cons is whether the current sample follows the one returned before it in the same source
	cons bool
}

func newPenaltyIterator(a, b adjustableIterator) *penaltyIterator {
	return &penaltyIterator{a: a, b: b, lastT: math.MinInt64, lastIter: a}
}

func (it *penaltyIterator) Next() chunkenc.ValueType {
	returned := it.lastT != math.MinInt64
	if !it.started {
		it.started = true
		it.aval, it.bval = it.a.Next(), it.b.Next()
	} else {
		// the followed replica's sample was just returned, so it steps to its next one, which seeking
		// past it would do too, without passing over any of its samples
		if it.aval != chunkenc.ValNone {
			if it.useA {
				it.aval = it.a.Next()
			} else {
				it.aval = it.a.Seek(it.lastT + 1 + it.penA)
			}
		}
		if it.bval != chunkenc.ValNone {
			if it.useA {
				it.bval = it.b.Seek(it.lastT + 1 + it.penB)
			} else {
				it.bval = it.b.Next()
			}
		}
	}
	prevUseA := it.useA
	switch {
	case it.aval == chunkenc.ValNone && it.bval == chunkenc.ValNone:
		it.vt = chunkenc.ValNone
		return it.vt
	case it.aval == chunkenc.ValNone:
		it.follow(false, it.b.AtT())
	case it.bval == chunkenc.ValNone:
		it.follow(true, it.a.AtT())
	default:
		ta, tb := it.a.AtT(), it.b.AtT()
		useA := ta <= tb
		t := tb
		if useA {
			t = ta
		}
		pen := int64(initialPenalty)
		if it.lastT != math.MinInt64 {
			pen = 2 * (t - it.lastT)
		}
		if useA {
			it.penB = pen
		} else {
			it.penA = pen
		}
		it.follow(useA, t)
	}
	it.vt = it.bval
	if it.useA {
		it.vt = it.aval
	}
	if it.useA != prevUseA && it.hasFloat {
		it.lastIter.adjustAtValue(it.lastFloat)
	}
	if it.vt == chunkenc.ValFloat {
		_, it.lastFloat = it.lastIter.At()
		it.hasFloat = true
	}
	// the replica switched to was sought past samples, so its sample follows none returned
	it.cons = returned && it.useA == prevUseA && isConsecutive(it.lastIter)
	return it.vt
}

// follow makes a or b the replica whose sample at t is returned, clearing its own penalty
func (it *penaltyIterator) follow(useA bool, t int64) {
	it.useA, it.lastT = useA, t
	if useA {
		it.lastIter, it.penA = it.a, 0
		return
	}
	it.lastIter, it.penB = it.b, 0
}

// Seek steps through samples rather than seeking each replica, since the replica followed at t
// depends on every sample before it
func (it *penaltyIterator) Seek(t int64) chunkenc.ValueType {
	vt := it.vt
	moved := it.started && vt != chunkenc.ValNone && it.AtT() < t
	if !it.started {
		vt = it.Next()
	}
	for vt != chunkenc.ValNone && it.AtT() < t {
		vt = it.Next()
	}
	if moved {
		// the caller never saw the samples passed over, so the one returned follows none it saw
		it.cons = false
	}
	return vt
}

func (it *penaltyIterator) At() (int64, float64) { return it.lastIter.At() }

func (it *penaltyIterator) AtHistogram(h *histogram.Histogram) (int64, *histogram.Histogram) {
	t, out := it.lastIter.AtHistogram(h)
	if !it.cons {
		out = withUnknownReset(out, h)
	}
	return t, out
}

func (it *penaltyIterator) AtFloatHistogram(fh *histogram.FloatHistogram) (int64, *histogram.FloatHistogram) {
	t, out := it.lastIter.AtFloatHistogram(fh)
	if !it.cons {
		out = withUnknownFloatReset(out, fh)
	}
	return t, out
}

func (it *penaltyIterator) consecutive() bool { return it.cons }

func (it *penaltyIterator) AtT() int64  { return it.lastIter.AtT() }
func (it *penaltyIterator) AtST() int64 { return it.lastIter.AtST() }

func (it *penaltyIterator) Err() error {
	if err := it.a.Err(); err != nil {
		return err
	}
	return it.b.Err()
}

// adjustAtValue adjusts both replicas when this iterator is itself a replica of an outer one
func (it *penaltyIterator) adjustAtValue(last float64) {
	if it.aval != chunkenc.ValNone {
		it.a.adjustAtValue(last)
	}
	if it.bval != chunkenc.ValNone {
		it.b.adjustAtValue(last)
	}
}

// consecutiveIterator reports whether its current sample follows the one it returned before in the
// same source; a histogram's counter reset hint holds only across such a pair
type consecutiveIterator interface {
	consecutive() bool
}

// isConsecutive reports whether it's current sample follows its previous one in the same source, which
// a member's own iterator, stepped by Next, always does
func isConsecutive(it chunkenc.Iterator) bool {
	if c, ok := it.(consecutiveIterator); ok {
		return c.consecutive()
	}
	return true
}

// withUnknownReset returns h with an unknown counter reset hint, unless it is a gauge; h is copied,
// into buf when given, unless it is buf, so a member's own histogram never changes
func withUnknownReset(h, buf *histogram.Histogram) *histogram.Histogram {
	if h == nil || h.CounterResetHint == histogram.GaugeType || h.CounterResetHint == histogram.UnknownCounterReset {
		return h
	}
	if h != buf {
		if buf == nil {
			h = h.Copy()
		} else {
			h.CopyTo(buf)
			h = buf
		}
	}
	h.CounterResetHint = histogram.UnknownCounterReset
	return h
}

// withUnknownFloatReset is withUnknownReset for float histograms
func withUnknownFloatReset(fh, buf *histogram.FloatHistogram) *histogram.FloatHistogram {
	if fh == nil || fh.CounterResetHint == histogram.GaugeType || fh.CounterResetHint == histogram.UnknownCounterReset {
		return fh
	}
	if fh != buf {
		if buf == nil {
			fh = fh.Copy()
		} else {
			fh.CopyTo(buf)
			fh = buf
		}
	}
	fh.CounterResetHint = histogram.UnknownCounterReset
	return fh
}
