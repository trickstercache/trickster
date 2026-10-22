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
	"context"
	"errors"
	"io"
	"math"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func (r *Relay) fillBudget(p *message.Params) time.Duration {
	if p == nil || !p.Has(message.ParamFillTimeout) {
		return r.opts.FillTimeout
	}
	return min(millis(p.FillTimeout), maxFillTimeout)
}

type fetchJob struct {
	t         *track
	owner     *session.Request
	requestID uint64
	start     wire.Location
	end       wire.Location
	order     message.GroupOrder
	rng       ranges
	urgency   int8
	budget    time.Duration

	out      *session.OutgoingFetch
	deadline time.Time
	gaps     int
	defPrio  uint8
}

type gapRange struct {
	first, last uint64
}

func (j *fetchJob) run(ctx context.Context) error {
	out, err := j.owner.OpenFetch(ctx, j.requestID, j.order, j.urgency)
	if err != nil {
		return err
	}
	j.out, j.deadline = out, time.Now().Add(j.budget)
	j.defPrio = j.t.properties().PublisherPriority()
	if err = j.serve(ctx); err != nil {
		out.Reset(message.ResetInternalError)
		return err
	}
	return out.Close()
}

func (j *fetchJob) descending() bool {
	return j.order == message.GroupOrderDescending
}

func (j *fetchJob) lo(g uint64) uint64 {
	if g == j.start.Group {
		return j.start.Object
	}
	return 0
}

func (j *fetchJob) hi(g uint64) uint64 {
	if g == j.end.Group {
		return j.end.Object
	}
	return math.MaxUint64
}

func (j *fetchJob) serve(ctx context.Context) error {
	// groups the relay holds are served locally; the stretches between them, and held groups it cannot vouch
	// for in full, are fetched upstream as merged gaps, in the requested group order
	known := j.t.knownGroups(j.start.Group, j.end.Group)
	var pending *gapRange
	addGap := func(a, b uint64) {
		switch {
		case pending == nil:
			pending = &gapRange{first: a, last: b}
		case !j.descending() && pending.last+1 == a:
			pending.last = b
		case j.descending() && b+1 == pending.first:
			pending.first = a
		default:
			// a gap that does not adjoin the pending one is never produced by the walks below
			pending = &gapRange{first: a, last: b}
		}
	}
	flush := func() error {
		if pending == nil {
			return nil
		}
		g := *pending
		pending = nil
		return j.fetchGap(ctx, g)
	}
	visit := func(g uint64) error {
		if objs, ok := j.local(g); ok {
			if err := flush(); err != nil {
				return err
			}
			return j.writeGroup(ctx, objs, g)
		}
		addGap(g, g)
		return nil
	}
	if !j.descending() {
		cursor, done := j.start.Group, false
		for _, k := range known {
			if k > cursor {
				addGap(cursor, k-1)
			}
			if err := visit(k); err != nil {
				return err
			}
			if k == math.MaxUint64 {
				done = true
				break
			}
			cursor = k + 1
		}
		if !done && cursor <= j.end.Group {
			addGap(cursor, j.end.Group)
		}
		return flush()
	}
	cursor, done := j.end.Group, false
	for _, k := range slices.Backward(known) {
		if k < cursor {
			addGap(k+1, cursor)
		}
		if err := visit(k); err != nil {
			return err
		}
		if k == 0 {
			done = true
			break
		}
		cursor = k - 1
	}
	if !done && cursor >= j.start.Group {
		addGap(j.start.Group, cursor)
	}
	return flush()
}

func (j *fetchJob) local(g uint64) ([]*object, bool) {
	// a group's objects in [lo, hi] come from the live edge or the cache only when they are known in full
	lo, hi := j.lo(g), j.hi(g)
	if objs, ok := j.t.liveGroup(g, lo, hi); ok {
		j.t.r.m.fetched(sourceLiveEdge, len(objs))
		return objs, true
	}
	if cg := j.t.cachedGroup(g); cg != nil && cg.covers(lo, hi) {
		j.t.r.m.fetched(sourceCache, len(cg.objects))
		return cg.objects, true
	}
	return nil, false
}

func (j *fetchJob) writeGroup(ctx context.Context, objs []*object, g uint64) error {
	lo, hi := j.lo(g), j.hi(g)
	for _, o := range objs {
		if o.id < lo || o.id > hi || o.status != message.StatusNormal || !j.rng.pass(o, j.defPrio) {
			continue
		}
		if !waitComplete(ctx, o) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := j.marker(message.RangeEndUnknown, o.loc()); err != nil {
				return err
			}
			continue
		}
		if err := j.writeObject(o); err != nil {
			return err
		}
	}
	return nil
}

func waitComplete(ctx context.Context, o *object) bool {
	for !o.complete() {
		if o.failed.Load() || o.run == nil {
			return false
		}
		ch := o.run.sig.wait()
		if o.complete() || o.failed.Load() {
			continue
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

func (j *fetchJob) writeObject(o *object) error {
	prio := o.priority
	if o.defaultPrio {
		prio = j.defPrio
	}
	fo := message.FetchObject{
		Group: o.group, Subgroup: o.subgroup, Object: o.id, Datagram: o.datagram, Priority: prio,
		Properties: o.props, PayloadLen: uint64(len(o.payload)),
	}
	if err := j.out.WriteObject(&fo); err != nil {
		return err
	}
	if _, err := j.out.Write(o.payload); err != nil {
		return err
	}
	j.t.r.m.object(directionOut, deliveryFetch, o.size())
	return nil
}

func (j *fetchJob) marker(kind message.RangeEnd, at wire.Location) error {
	return j.out.WriteObject(&message.FetchObject{EndOfRange: kind, Group: at.Group, Object: at.Object})
}

func (j *fetchJob) gapEnd(g gapRange) wire.Location {
	// the last Location the gap covers in the stream's traversal order
	last := g.last
	if j.descending() {
		last = g.first
	}
	return wire.Location{Group: last, Object: j.hi(last)}
}

func (j *fetchJob) fetchGap(ctx context.Context, g gapRange) error {
	end := j.gapEnd(g)
	if j.gaps >= j.t.r.opts.MaxGapFetches || !j.t.hasUpstream() {
		return j.marker(message.RangeEndUnknown, end)
	}
	if j.budget <= 0 || !time.Now().Before(j.deadline) {
		return j.marker(message.RangeEndTimedOut, end)
	}
	j.gaps++
	gctx, cancel := context.WithDeadline(ctx, j.deadline)
	defer cancel()
	src := &gapStream{ch: make(chan *session.IncomingFetch, 1), done: make(chan struct{})}
	defer close(src.done)
	req, release, err := j.openGap(gctx, g, src)
	if err != nil {
		return j.gapFailed(ctx, end)
	}
	defer req.Cancel(message.ResetCancelled)
	resp, err := req.Response(gctx)
	release(err)
	if err != nil {
		return j.gapFailed(ctx, end)
	}
	if _, ok := resp.(*message.FetchOK); !ok {
		return j.marker(message.RangeEndUnknown, end)
	}
	var in *session.IncomingFetch
	select {
	case in = <-src.ch:
	case <-gctx.Done():
		return j.gapFailed(ctx, end)
	}
	return j.copyGap(ctx, in, g, end)
}

func (j *fetchJob) gapFailed(ctx context.Context, end wire.Location) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !time.Now().Before(j.deadline) {
		return j.marker(message.RangeEndTimedOut, end)
	}
	return j.marker(message.RangeEndUnknown, end)
}

func (j *fetchJob) openGap(ctx context.Context, g gapRange, src *gapStream) (*session.Request, func(error), error) {
	sess, release, _, err := j.t.upstreamSession(ctx)
	if err != nil {
		return nil, nil, err
	}
	f := message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: g.first, StartObject: j.lo(g.first),
		EndGroupDelta: g.last - g.first, EndObject: j.hi(g.last),
	}
	if f.EndObject == math.MaxUint64 {
		f.Type, f.EndObject = message.FilterAbsoluteStartGroupEnd, 0
	}
	var p message.Params
	p.SetLocationFilter(f)
	if j.descending() {
		p.SetGroupOrder(message.GroupOrderDescending)
	}
	req, err := sess.Open(ctx, &message.Fetch{Track: j.t.name, Params: p}, nopRequestHandler{}, src)
	if err != nil {
		release(err)
		return nil, nil, err
	}
	return req, release, nil
}

func (j *fetchJob) copyGap(ctx context.Context, in *session.IncomingFetch, g gapRange, end wire.Location) error {
	// the upstream fetch stream is forwarded as it arrives, and each group it delivers in full is cached
	b := &groupBuilder{j: j, g: g}
	var last wire.Location
	wrote := false
	for {
		fo, err := in.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				b.flush(true)
				return nil
			}
			b.flush(false)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if wrote && !last.Less(end) && !j.descending() {
				return nil
			}
			return j.gapFailed(ctx, end)
		}
		loc := wire.Location{Group: fo.Group, Object: fo.Object}
		if fo.EndOfRange != message.RangeEndNone {
			b.marker(fo)
			if err = j.out.WriteObject(&fo); err != nil {
				return err
			}
			last, wrote = loc, true
			continue
		}
		if fo.PayloadLen > j.t.r.maxObject {
			in.Cancel(message.ResetExcessiveLoad)
			return j.gapFailed(ctx, end)
		}
		o := &object{
			group: fo.Group, id: fo.Object, subgroup: fo.Subgroup, priority: fo.Priority, datagram: fo.Datagram,
			props: fo.Properties, payload: make([]byte, fo.PayloadLen), arrived: time.Now(),
		}
		if _, err = io.ReadFull(in, o.payload); err != nil {
			b.flush(false)
			return j.gapFailed(ctx, end)
		}
		o.filled.Store(int64(len(o.payload)))
		b.add(o)
		j.t.r.m.fetched(sourceUpstream, 1)
		if !j.rng.pass(o, j.defPrio) {
			continue
		}
		if err = j.writeObject(o); err != nil {
			return err
		}
		last, wrote = loc, true
	}
}

type gapStream struct {
	ch   chan *session.IncomingFetch
	done chan struct{}
}

func (s *gapStream) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	in.Cancel(message.ResetCancelled)
}

func (s *gapStream) HandleDatagram(*session.Request, *message.Datagram) {}

func (s *gapStream) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	// the stream is read by the job; returning would let the session stop it
	select {
	case s.ch <- in:
		<-s.done
	default:
		in.Cancel(message.ResetCancelled)
	}
}

type groupBuilder struct {
	j       *fetchJob
	g       gapRange
	cur     uint64
	objs    []*object
	tainted bool
	active  bool
}

func (b *groupBuilder) begin(group uint64) {
	if b.active && b.cur == group {
		return
	}
	b.flush(true)
	b.cur, b.active, b.objs, b.tainted = group, true, nil, false
}

func (b *groupBuilder) add(o *object) {
	b.begin(o.group)
	b.objs = append(b.objs, o)
}

func (b *groupBuilder) marker(fo message.FetchObject) {
	b.begin(fo.Group)
	if fo.EndOfRange != message.RangeEndNonExistent {
		b.tainted = true
	}
}

func (b *groupBuilder) flush(complete bool) {
	// a group spills once the stream has moved past it with nothing unknown inside
	if !b.active {
		return
	}
	b.active = false
	j := b.j
	if !complete || b.tainted || !j.t.r.opts.Spill || len(b.objs) == 0 {
		return
	}
	lo, hi := j.lo(b.cur), j.hi(b.cur)
	cg := &cachedGroup{key: j.t.key, group: b.cur, from: lo, to: hi, toEnd: hi == math.MaxUint64, objects: b.objs}
	if job, ok := j.t.spillJob(cg); ok {
		j.t.r.queueSpill(job)
	}
}
