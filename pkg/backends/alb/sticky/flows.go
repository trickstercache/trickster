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

package sticky

import (
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/flow"

	"github.com/prometheus/client_golang/prometheus"
)

// Flows pins an ALB's stream and native sessions in its table, by a key read from each flow, once
// the flow's member is reached. It is safe for concurrent use.
type Flows struct {
	table   *Table
	source  flowkey.KeySource
	prefix  int
	reject  bool
	results [resultCount]prometheus.Counter
}

// NewFlows returns the persistence that initialized options configure on the named ALB's stream
// and native listeners, or nil when the mode in effect there keeps no table.
func NewFlows(albName string, o *options.Options) *Flows {
	if o == nil || o.ModeFor(false) != options.ModeTable {
		return nil
	}
	p := &Flows{
		table: TableFor(albName, o), source: o.Table.KeySource, prefix: o.Table.IPv6Prefix,
		reject: o.OnUnavailable == options.OnUnavailableReject,
	}
	if p.prefix <= 0 {
		p.prefix = flowkey.DefaultIPv6Prefix
	}
	for i := range p.results {
		p.results[i] = metrics.ALBStickyResults.WithLabelValues(albName, resultNames[i])
	}
	return p
}

// Table returns the table the persistence keeps its pins in, or nil for no persistence.
func (p *Flows) Table() *Table {
	if p == nil {
		return nil
	}
	return p.table
}

// StreamKey returns the key a tcp, tls or udp flow is pinned by.
func (p *Flows) StreamKey(f flow.Flow) flowkey.Value {
	return flowkey.StreamValue(p.source, p.prefix, f)
}

// SessionKey returns the key a native session is pinned by, from the name it authenticated as
// and the address it arrived from.
func (p *Flows) SessionKey(user string, client netip.Addr) flowkey.Value {
	return flowkey.Session(p.source, p.prefix, user, client)
}

// FlowSession is one flow's or native session's passage through its ALB's table: the path its key
// is pinned to, and the path it is sent down.
type FlowSession struct {
	passage
	owner *Flows
	// the picker asked to honor each level's pin, to tell a pin to an unavailable member from one
	// to a member that has left its pool
	askedAt [lb.MaxPickDepth]lb.Picker
	settled atomic.Bool
}

// Begin resets the session for a flow with key, and reads the path stored under it as of now,
// in Unix nanoseconds.
func (p *Flows) Begin(s *FlowSession, key flowkey.Value, now int64) {
	s.passage, s.owner, s.askedAt = passage{key: key}, p, [lb.MaxPickDepth]lb.Picker{}
	s.settled.Store(false)
	if !key.OK {
		return
	}
	if path, ok := p.table.Get(key.Hash, now); ok {
		s.Pins, s.found = path, foundPins
	}
}

// Flow returns f pinned to the session's member at level when every level above followed its pin;
// picker is the level's, via the member picked above it (nil at level 0).
func (s *FlowSession) Flow(level int, picker lb.Picker, via *lb.Member, f lb.Flow) lb.Flow {
	if level < 0 || level >= lb.MaxPickDepth {
		return f
	}
	if via != nil {
		s.Record(level-1, via.Hash())
	}
	s.askedAt[level] = nil
	if pin, ok := s.Pin(level); ok {
		f.Pin, f.HasPin = pin, true
		s.askedAt[level] = picker
	}
	return f
}

// Stranded reports whether a pick that found no member had followed the session's pins into a pool
// with nothing left. Callers must ask before Picked records the failed pick.
func (s *FlowSession) Stranded() bool {
	if s.found != foundPins || s.Chosen.Depth == 0 {
		return false
	}
	for i := range s.Chosen.Depth {
		if s.Chosen.Hashes[i] != s.Pins.Hashes[i] {
			return false
		}
	}
	return true
}

// Picked records the path that a pick sends the session down.
func (s *FlowSession) Picked(pk lb.LeafPick) {
	s.Chosen = Path{}
	for i := range pk.Depth() {
		s.Record(i, pk.Level(i).Member().Hash())
	}
}

// pinnable is a picker that tells whether its pool still has the member a pin names
type pinnable interface {
	Pinnable(pin uint64) bool
}

// Unavailable reports whether the path that Picked recorded leaves a pinned level for another
// member, or for none, while the pinned member is still in its pool: it is unavailable, not gone.
func (s *FlowSession) Unavailable() bool {
	for level, picker := range s.askedAt {
		if picker == nil || (level < int(s.Chosen.Depth) && s.Chosen.Hashes[level] == s.Pins.Hashes[level]) {
			continue
		}
		if p, ok := picker.(pinnable); ok && p.Pinnable(s.Pins.Hashes[level]) {
			return true
		}
	}
	return false
}

// OnPins reports whether the session was sent down the whole path it is pinned to.
func (s *FlowSession) OnPins() bool {
	return s.found == foundPins && s.Chosen == s.Pins
}

// Rejects reports whether a session whose pinned member is unavailable is refused, not moved.
func (s *FlowSession) Rejects() bool {
	return s.owner != nil && s.owner.reject
}

// Refuse counts the session as refused because a member it is pinned to is unavailable. It counts
// a session once, whatever else settles it.
func (s *FlowSession) Refuse() {
	if s.owner == nil || !s.settled.CompareAndSwap(false, true) {
		return
	}
	s.rejected = true
	s.owner.results[resultRejected].Inc()
}

// Settle counts how the session fared once the member it was sent to is reached, and pins its key
// to the path it was sent down when that path is new. It runs once per session.
func (s *FlowSession) Settle() {
	if s.owner == nil || s.Chosen.Depth == 0 || !s.settled.CompareAndSwap(false, true) {
		return
	}
	s.owner.results[s.result()].Inc()
	if s.key.OK && !s.OnPins() {
		s.owner.table.Put(s.key.Hash, s.Chosen, time.Now().UnixNano())
	}
}
