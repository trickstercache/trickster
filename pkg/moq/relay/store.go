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

package relay

import (
	"bytes"
	"container/list"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *signal) wait() <-chan struct{} {
	// a notify wakes every waiter at once; the channel is made only when someone waits
	s.mu.Lock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	ch := s.ch
	s.mu.Unlock()
	return ch
}

func (s *signal) notify() {
	s.mu.Lock()
	if s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
	s.mu.Unlock()
}

type object struct {
	group, id   uint64
	subgroup    uint64
	priority    uint8
	defaultPrio bool
	datagram    bool
	endOfGroup  bool
	status      message.ObjectStatus
	props       message.Properties
	payload     []byte
	filled      atomic.Int64
	failed      atomic.Bool
	arrived     time.Time
	run         *run
}

func (o *object) loc() wire.Location {
	return wire.Location{Group: o.group, Object: o.id}
}

func (o *object) complete() bool {
	return o.filled.Load() == int64(len(o.payload))
}

func (o *object) size() int64 {
	return int64(len(o.payload) + len(o.props))
}

func (o *object) sameAs(d *object) bool {
	// a duplicate that differs in delivery mode, subgroup, priority or payload makes the track malformed
	if o.datagram != d.datagram || o.priority != d.priority || o.defaultPrio != d.defaultPrio ||
		(!o.datagram && o.subgroup != d.subgroup) || len(o.payload) != len(d.payload) || o.status != d.status {
		return false
	}
	if o.complete() && d.complete() {
		return bytes.Equal(o.payload, d.payload)
	}
	return true
}

func (o *object) priorObjectGap() uint64 {
	v, _ := o.props.Uint(message.PropPriorObjectIDGap)
	return v
}

type runState uint32

const (
	runOpen runState = iota
	runFinished
	runReset
)

type run struct {
	g      *group
	header message.SubgroupHeader
	objs   atomic.Pointer[[]*object]
	state  atomic.Uint32
	sig    signal
}

func newRun(g *group, h message.SubgroupHeader) *run {
	// one upstream stream's reader appends, publishing each new slice header atomically so readers never lock
	rn := &run{g: g, header: h}
	empty := make([]*object, 0, 4)
	rn.objs.Store(&empty)
	return rn
}

func (rn *run) objects() []*object {
	return *rn.objs.Load()
}

func (rn *run) append(o *object) {
	s := append(*rn.objs.Load(), o)
	rn.objs.Store(&s)
	rn.sig.notify()
}

func (rn *run) end(st runState) {
	rn.state.Store(uint32(st))
	rn.sig.notify()
}

func (rn *run) ended() (runState, bool) {
	st := runState(rn.state.Load())
	return st, st != runOpen
}

type group struct {
	t            *track
	id           uint64
	runs         []*run
	datagrams    []*object
	hasEnd       bool
	end          uint64
	bytes        atomic.Int64
	created      time.Time
	elem         *list.Element
	evicted      atomic.Bool
	newest       atomic.Bool
	spillPending bool
}

func (g *group) find(id uint64) *object {
	// the caller holds the track's lock
	for _, d := range g.datagrams {
		if d.id == id {
			return d
		}
	}
	for _, rn := range g.runs {
		objs := rn.objects()
		i, ok := slices.BinarySearchFunc(objs, id, func(o *object, id uint64) int {
			switch {
			case o.id < id:
				return -1
			case o.id > id:
				return 1
			}
			return 0
		})
		if ok {
			return objs[i]
		}
	}
	return nil
}

func (g *group) sorted() []*object {
	// the caller holds the track's lock
	n := len(g.datagrams)
	for _, rn := range g.runs {
		n += len(rn.objects())
	}
	out := make([]*object, 0, n)
	out = append(out, g.datagrams...)
	for _, rn := range g.runs {
		out = append(out, rn.objects()...)
	}
	slices.SortFunc(out, func(a, b *object) int {
		switch {
		case a.id < b.id:
			return -1
		case a.id > b.id:
			return 1
		}
		return 0
	})
	return out
}

func (g *group) openRuns() bool {
	for _, rn := range g.runs {
		if _, done := rn.ended(); !done {
			return true
		}
	}
	return false
}

func covered(objs []*object, lo, hi uint64, hasEnd bool, end uint64) bool {
	// every ID in [lo, hi] must be present in objs (sorted by ID), or declared absent by a Prior Object ID Gap or
	// by the group's known end; an unknown end leaves an open range uncovered
	limit := hi
	if hasEnd && end < limit {
		limit = end
	}
	if limit == math.MaxUint64 {
		return false
	}
	expect := lo
	for _, o := range objs {
		if o.id < expect {
			continue
		}
		if gap := o.priorObjectGap(); o.id-min(gap, o.id) > expect {
			return false
		}
		if o.id >= limit {
			return true
		}
		expect = o.id + 1
	}
	return expect > limit
}
