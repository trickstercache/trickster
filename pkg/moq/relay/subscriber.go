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
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type subState uint32

const (
	subPending subState = iota
	subActive
	subDone
)

type subscriber struct {
	t      *track
	req    *session.Request
	ctx    context.Context
	filter message.LocationFilter
	ready  chan struct{}
	state  atomic.Uint32

	forward atomic.Bool
	filters atomic.Pointer[subFilters]

	mu         sync.Mutex
	win        window
	rng        ranges
	prio       uint8
	order      message.GroupOrder
	objTimeout time.Duration
	sgTimeout  time.Duration
	pending    []*message.RequestUpdate
	writers    map[*writer]struct{}

	held    bool
	wg      sync.WaitGroup
	release sync.Once
	forget  func()
	joining *wire.Location
}

func newSubscriber(t *track, req *session.Request, p *message.Params) *subscriber {
	// a subscription the relay opens with PUBLISH has no request until the PUBLISH is sent; see bind
	s := &subscriber{
		t: t, req: req, filter: p.LocationFilter, ready: make(chan struct{}),
		rng: newRanges(p.RangeFilters), prio: p.SubscriberPriorityOr(message.DefaultPublisherPriority),
		objTimeout: millis(p.ObjectDeliveryTimeout),
		sgTimeout:  millis(p.SubgroupDeliveryTimeout),
		writers:    make(map[*writer]struct{}),
	}
	if req != nil {
		s.ctx = req.Context()
	}
	if p.Has(message.ParamGroupOrder) {
		s.order = p.GroupOrder
	}
	s.forward.Store(p.ForwardOr(true))
	return s
}

func (s *subscriber) bind(req *session.Request) {
	// set before the subscription activates, after which writers and updates read it without a lock
	s.req, s.ctx = req, req.Context()
}

type subFilters struct {
	win window
	rng ranges
}

func (s *subscriber) publishFilters() {
	// the caller holds s.mu; writers read an immutable snapshot, so filtering an object takes no lock
	s.filters.Store(&subFilters{win: s.win, rng: s.rng})
}

func (s *subscriber) releaseTrack() {
	s.release.Do(func() {
		s.t.r.tracks.release(s.t)
		if s.forget != nil {
			s.forget()
		}
	})
}

func (s *subscriber) joiningLocation() (*wire.Location, bool) {
	// the Largest Object last communicated as forwarding began, which a Joining FETCH ends at (draft -19 §5.1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.joining, s.forward.Load()
}

func (s *subscriber) refuse(code message.RequestCode, reason string) {
	s.t.mu.Lock()
	s.t.unholdLocked(s)
	s.t.mu.Unlock()
	go s.t.syncForward()
	s.state.Store(uint32(subDone))
	_ = s.req.Reject(code, reason, 0)
	s.t.r.m.request(kindSubscribe, resultRefused)
	s.releaseTrack()
}

func (s *subscriber) register() (*wire.Location, message.Properties, message.RequestCode, string) {
	// the track has a source; the subscriber joins it, replays the newest group within its window, and forwards
	// what arrives from now on
	t := s.t
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.malformed {
		return nil, nil, message.RequestMalformedTrack, "malformed track"
	}
	largest := t.largestLocked()
	w, ok := newWindow(s.filter, largest)
	if !ok {
		return nil, nil, message.RequestInvalidRange, "location filter overflows"
	}
	props := t.props
	s.mu.Lock()
	s.win = w
	s.publishFilters()
	if s.order == message.GroupOrderUnset {
		s.order = props.GroupOrder()
	}
	s.mu.Unlock()
	t.addSubscriber(s)
	t.startJoinRunsLocked(s, props)
	return largest, props, 0, ""
}

func (s *subscriber) markActive() []*message.RequestUpdate {
	// returns the updates that arrived while the subscription was pending, for the caller to apply
	s.mu.Lock()
	s.state.Store(uint32(subActive))
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	close(s.ready)
	s.t.r.m.subscriptions(directionDownstream, 1)
	return pending
}

func (s *subscriber) activatePublished() {
	// a subscription the relay opened with PUBLISH, which carried what a SUBSCRIBE_OK would (§9.8)
	if subState(s.state.Load()) == subDone {
		return
	}
	if _, _, code, _ := s.register(); code != 0 {
		s.end(message.ResetCancelled)
		return
	}
	go s.t.syncForward()
	for _, u := range s.markActive() {
		s.applyUpdate(u)
	}
}

func (s *subscriber) activate() {
	// the track's upstream is established; the subscriber registers and its SUBSCRIBE is answered
	t := s.t
	largest, props, code, reason := s.register()
	if code != 0 {
		s.refuse(code, reason)
		return
	}
	go t.syncForward()
	if largest != nil && s.forward.Load() {
		s.mu.Lock()
		s.joining = largest
		s.mu.Unlock()
	}

	ok2 := &message.SubscribeOK{TrackAlias: s.req.Session().NewTrackAlias()}
	if largest != nil {
		ok2.Params.SetLargestObject(*largest)
	}
	if includeProperties(s.req.Message()) {
		ok2.TrackProperties = props
	}
	if err := s.req.Send(ok2); err != nil {
		s.end(message.ResetCancelled)
		return
	}
	pending := s.markActive()
	t.r.m.request(kindSubscribe, resultOK)
	if largest != nil && s.forward.Load() {
		if m, isSub := s.req.Message().(*message.Subscribe); isSub && m.Params.Fill != nil {
			s.startFill(s.req.RequestID(), m.Params.Fill, largest)
		}
		s.sendEndedTail(*largest, props.PublisherPriority())
	}
	for _, u := range pending {
		s.applyUpdate(u)
	}
}

func (s *subscriber) sendEndedTail(largest wire.Location, defaultPrio uint8) {
	// a subscription starting just past an ended group's last object learns the group ended from an empty stream,
	// which completes the group a fill or joining fetch delivers
	s.mu.Lock()
	start := s.win.start
	s.mu.Unlock()
	if largest.Object == math.MaxUint64 || start != (wire.Location{Group: largest.Group, Object: largest.Object + 1}) {
		return
	}
	h, ok := s.t.endedTail(largest)
	if !ok {
		return
	}
	prio := h.Priority
	if h.DefaultPriority {
		prio = defaultPrio
	}
	s.mu.Lock()
	urgency, incremental := session.Urgency(s.prio, prio), session.Incremental(s.order)
	s.mu.Unlock()
	s.wg.Go(func() {
		if out, err := s.req.OpenSubgroup(s.ctx, h, urgency, incremental); err == nil {
			_ = out.Close()
		}
	})
}

func includeProperties(m message.Message) bool {
	switch v := m.(type) {
	case *message.Subscribe:
		return v.Params.IncludePropertiesOr(true)
	case *message.Fetch:
		return v.Params.IncludePropertiesOr(true)
	case *message.TrackStatus:
		return v.Params.IncludePropertiesOr(true)
	}
	return true
}

func (t *track) startJoinRunsLocked(s *subscriber, props message.Properties) {
	// a joining subscription replays the newest group the relay holds from the subscription's start, as a subscriber
	// joining at the current group expects; older groups are a fill's or a FETCH's, and other open runs start next
	for _, g := range t.groups {
		replay := g == t.newest
		for _, rn := range g.runs {
			if replay {
				s.startRun(rn, 0)
			} else if _, done := rn.ended(); !done {
				s.startRun(rn, len(rn.objects()))
			}
		}
	}
	if t.newest != nil && len(t.newest.datagrams) > 0 {
		s.replayDatagrams(slices.Clone(t.newest.datagrams), props)
	}
}

func (s *subscriber) replayDatagrams(objs []*object, props message.Properties) {
	// the datagrams wait for the subscription to be answered, as its streams do
	s.wg.Go(func() {
		select {
		case <-s.ready:
		case <-s.ctx.Done():
			return
		}
		defaultPrio := props.PublisherPriority()
		for _, o := range objs {
			s.sendDatagram(o, props, defaultPrio)
		}
	})
}

func (t *track) startOpenRunsLocked(s *subscriber) {
	// a subscription forwards what arrives after it begins, so each open run starts at its next object
	for _, g := range t.groups {
		for _, rn := range g.runs {
			if _, done := rn.ended(); !done {
				s.startRun(rn, len(rn.objects()))
			}
		}
	}
}

func (s *subscriber) startRun(rn *run, from int) {
	// the run is forwarded on a stream of its own, from its object at index from
	if subState(s.state.Load()) == subDone || !s.forward.Load() {
		return
	}
	if f := s.filters.Load(); f == nil || !f.win.admitsGroup(rn.g.id) {
		return
	}
	w := &writer{s: s, rn: rn}
	s.mu.Lock()
	s.writers[w] = struct{}{}
	s.wg.Add(1)
	s.mu.Unlock()
	go w.run(from)
}

func (s *subscriber) writerDone(w *writer) {
	s.mu.Lock()
	delete(s.writers, w)
	s.mu.Unlock()
	s.wg.Done()
}

func (s *subscriber) resetWriters(code message.ResetCode) {
	s.mu.Lock()
	ws := make([]*writer, 0, len(s.writers))
	for w := range s.writers {
		ws = append(ws, w)
	}
	s.mu.Unlock()
	for _, w := range ws {
		w.cancel(code)
	}
}

func (s *subscriber) passes(o *object, defaultPrio uint8) bool {
	f := s.filters.Load()
	return f != nil && f.win.contains(o.loc()) && f.rng.pass(o, defaultPrio)
}

func (s *subscriber) timeouts(props message.Properties, first *object) (object, subgroup time.Duration) {
	// the publisher's value is the first object's property when present, else the track's; the smaller nonzero
	// of the publisher's and the subscriber's applies, and MAX_CACHE_DURATION bounds when forwarding may start
	pubObj, _ := props.Uint(message.PropObjectDeliveryTimeout)
	pubSub, _ := props.Uint(message.PropSubgroupDeliveryTimeout)
	if first != nil {
		if v, ok := first.props.Uint(message.PropObjectDeliveryTimeout); ok {
			pubObj = v
		}
		if v, ok := first.props.Uint(message.PropSubgroupDeliveryTimeout); ok {
			pubSub = v
		}
	}
	s.mu.Lock()
	object = minNonzero(s.objTimeout, millis(pubObj))
	subgroup = minNonzero(s.sgTimeout, millis(pubSub))
	s.mu.Unlock()
	if mcd, ok := props.MaxCacheDuration(); ok {
		object = minNonzero(object, max(millis(mcd), time.Nanosecond))
	}
	return object, subgroup
}

func minNonzero(a, b time.Duration) time.Duration {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	}
	return min(a, b)
}

func (s *subscriber) sendDatagram(o *object, props message.Properties, defaultPrio uint8) {
	if subState(s.state.Load()) != subActive || !s.forward.Load() || !s.passes(o, defaultPrio) {
		return
	}
	var deadline time.Time
	if d, _ := s.timeouts(props, o); d > 0 {
		deadline = o.arrived.Add(d)
	}
	dg := message.Datagram{
		Group: o.group, Object: o.id, Priority: o.priority, DefaultPriority: o.defaultPrio, EndOfGroup: o.endOfGroup,
		Properties: o.props, HasStatus: o.status != message.StatusNormal, Status: o.status, Payload: o.payload,
	}
	if err := s.req.QueueDatagram(&dg, deadline); err != nil {
		s.t.r.m.drop(dropDatagramQueueFull)
		return
	}
	s.t.r.m.object(directionOut, deliveryDatagram, o.size())
}

func (s *subscriber) setForward(forward bool) {
	was := s.forward.Load()
	s.t.setForward(s, forward)
	if forward && !was {
		s.t.resume(s.ctx)
	}
	switch {
	case was && !forward:
		// a paused subscription omits objects, which ends its open streams with a reset
		s.resetWriters(message.ResetCancelled)
	case !was && forward:
		s.t.mu.Lock()
		s.t.startOpenRunsLocked(s)
		s.t.mu.Unlock()
	}
}

func (s *subscriber) HandleMessage(_ *session.Request, m message.Message) {
	u, ok := m.(*message.RequestUpdate)
	if !ok {
		return
	}
	s.mu.Lock()
	if subState(s.state.Load()) == subPending {
		s.pending = append(s.pending, u)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.applyUpdate(u)
}

func (s *subscriber) applyUpdate(u *message.RequestUpdate) {
	if subState(s.state.Load()) != subActive {
		return
	}
	p := &u.Params
	if p.Invalid != nil {
		_ = s.req.Send(&message.RequestError{Code: p.Invalid.Code, Reason: p.Invalid.Reason})
		go s.finish(message.DoneUpdateFailed, p.Invalid.Reason)
		return
	}
	t := s.t
	t.mu.Lock()
	largest := t.largestLocked()
	t.mu.Unlock()
	s.mu.Lock()
	if p.Has(message.ParamLocationFilter) {
		s.filter = p.LocationFilter
		if w, ok := newWindow(p.LocationFilter, largest); ok {
			s.win = w
		}
	}
	if len(p.RangeFilters) > 0 {
		s.rng = s.rng.update(p.RangeFilters)
	}
	s.publishFilters()
	if p.Has(message.ParamSubscriberPriority) {
		s.prio = p.SubscriberPriority
	}
	if p.Has(message.ParamObjectDeliveryTimeout) {
		s.objTimeout = millis(p.ObjectDeliveryTimeout)
	}
	if p.Has(message.ParamSubgroupDeliveryTimeout) {
		s.sgTimeout = millis(p.SubgroupDeliveryTimeout)
	}
	s.mu.Unlock()
	resumed := p.Has(message.ParamForward) && p.Forward && !s.forward.Load()
	if p.Has(message.ParamForward) {
		s.setForward(p.Forward)
	}
	ok := &message.RequestOK{}
	if largest != nil {
		ok.Params.SetLargestObject(*largest)
		if resumed {
			s.mu.Lock()
			s.joining = largest
			s.mu.Unlock()
		}
	}
	if err := s.req.Send(ok); err != nil {
		return
	}
	if p.Has(message.ParamNewGroupRequest) {
		go t.newGroupRequest(p.NewGroupRequest)
	}
	if p.Fill != nil && s.forward.Load() {
		s.startFill(u.RequestID, p.Fill, largest)
	}
}

func (s *subscriber) startFill(requestID uint64, fill *message.Params, largest *wire.Location) {
	if largest == nil {
		return
	}
	s.mu.Lock()
	filter := s.filter
	if fill.Has(message.ParamLocationFilter) {
		filter = fill.LocationFilter
	}
	order, prio, rng := s.order, s.prio, s.rng
	s.mu.Unlock()
	start, end, _, ok := filter.Bounds(largest, true)
	if !ok || largest.Less(start) || end.Less(start) {
		return
	}
	if largest.Less(end) {
		end = *largest
	}
	if fill.Has(message.ParamGroupOrder) {
		order = fill.GroupOrder
	}
	if fill.Has(message.ParamSubscriberPriority) {
		prio = fill.SubscriberPriority
	}
	if len(fill.RangeFilters) > 0 {
		rng = newRanges(fill.RangeFilters)
	}
	j := &fetchJob{
		t: s.t, owner: s.req, requestID: requestID, start: start, end: end, order: order, rng: rng,
		urgency: session.Urgency(prio, s.t.properties().PublisherPriority()), budget: s.t.r.fillBudget(fill),
	}
	s.wg.Go(func() {
		_ = j.run(s.ctx)
	})
}

func (s *subscriber) HandleClose(_ *session.Request, _ error) {
	s.end(message.ResetCancelled)
}

func (s *subscriber) end(code message.ResetCode) {
	// the subscriber or its session ended the subscription
	prev := subState(s.state.Swap(uint32(subDone)))
	if prev != subDone {
		s.t.removeSubscriber(s)
		s.resetWriters(code)
		if prev == subActive {
			s.t.r.m.subscriptions(directionDownstream, -1)
		}
	}
	s.releaseTrack()
}

func (s *subscriber) finish(code message.DoneCode, reason string) {
	// the relay ends the subscription: once its streams are closed, PUBLISH_DONE ends the request
	prev := subState(s.state.Swap(uint32(subDone)))
	if prev == subDone {
		return
	}
	s.t.removeSubscriber(s)
	if prev == subPending {
		_ = s.req.Reject(message.RequestInternalError, reason, 0)
		s.releaseTrack()
		return
	}
	if code == message.DoneMalformedTrack {
		s.resetWriters(message.ResetMalformedTrack)
	}
	if !waitFor(&s.wg, finishWait) {
		s.resetWriters(message.ResetCancelled)
		s.wg.Wait()
	}
	_ = s.req.Send(&message.PublishDone{Code: code, StreamCount: s.req.StreamCount(), Reason: reason})
	_ = s.req.CloseSend()
	s.t.r.m.subscriptions(directionDownstream, -1)
	s.releaseTrack()
}

func waitFor(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

type writer struct {
	s        *subscriber
	rn       *run
	mu       sync.Mutex
	out      *session.OutgoingSubgroup
	canceled atomic.Bool
	code     message.ResetCode
}

func (w *writer) cancel(code message.ResetCode) {
	w.mu.Lock()
	w.code = code
	w.canceled.Store(true)
	out := w.out
	w.mu.Unlock()
	if out != nil {
		out.Reset(code)
	}
	w.rn.sig.notify()
}

func (w *writer) stopped() bool {
	return w.canceled.Load()
}

func (w *writer) reset(code message.ResetCode, delivered bool) {
	w.mu.Lock()
	out := w.out
	w.mu.Unlock()
	if out == nil {
		return
	}
	if delivered {
		out.ResetDelivered(code)
		return
	}
	out.Reset(code)
}

func (w *writer) run(from int) {
	s := w.s
	defer s.writerDone(w)
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	}
	rn := w.rn
	props := s.t.properties()
	defaultPrio := props.PublisherPriority()
	objTimeout, sgTimeout := s.timeouts(props, firstObject(rn))
	for i := from; ; {
		st, done := rn.ended()
		objs := rn.objects()
		for ; i < len(objs); i++ {
			o := objs[i]
			if w.stopped() {
				return
			}
			if !s.passes(o, defaultPrio) {
				continue
			}
			if objTimeout > 0 && time.Since(o.arrived) > objTimeout {
				s.t.r.m.drop(dropDeliveryTimeout)
				w.reset(message.ResetDeliveryTimeout, false)
				return
			}
			if !w.write(o, i == 0, defaultPrio) {
				return
			}
		}
		if done && i >= len(rn.objects()) {
			w.finish(st, sgTimeout)
			return
		}
		ch := rn.sig.wait()
		if _, d := rn.ended(); d || len(rn.objects()) > i || w.stopped() {
			continue
		}
		select {
		case <-ch:
		case <-s.ctx.Done():
			w.reset(message.ResetCancelled, false)
			return
		}
	}
}

func firstObject(rn *run) *object {
	if objs := rn.objects(); len(objs) > 0 {
		return objs[0]
	}
	return nil
}

func (w *writer) finish(st runState, sgTimeout time.Duration) {
	w.mu.Lock()
	out := w.out
	w.mu.Unlock()
	if out == nil {
		return
	}
	if st == runFinished {
		_ = out.CloseWithTimeout(sgTimeout)
		return
	}
	// the upstream stream was reset, so the objects already sent are delivered and the rest may exist
	out.ResetDelivered(message.ResetCancelled)
}

func (w *writer) open(firstID uint64, firstOfRun bool, defaultPrio uint8) bool {
	s := w.s
	h := w.rn.header
	switch {
	case !firstOfRun:
		h.FirstObject = false
		if h.Mode == message.SubgroupIDFirstObject {
			h.Mode = message.SubgroupIDExplicit
		}
	case firstID == 0:
		// no object precedes Object 0 in its subgroup, a fact a publisher on a draft without the bit cannot state
		h.FirstObject = true
	}
	prio := h.Priority
	if h.DefaultPriority {
		prio = defaultPrio
	}
	s.mu.Lock()
	urgency, incremental := session.Urgency(s.prio, prio), session.Incremental(s.order)
	s.mu.Unlock()
	out, err := s.req.OpenSubgroup(s.ctx, h, urgency, incremental)
	if err != nil {
		return false
	}
	w.mu.Lock()
	w.out = out
	canceled, code := w.canceled.Load(), w.code
	w.mu.Unlock()
	if canceled {
		out.Reset(code)
		return false
	}
	return true
}

func (w *writer) write(o *object, firstOfRun bool, defaultPrio uint8) bool {
	if w.out == nil && !w.open(o.id, firstOfRun, defaultPrio) {
		return false
	}
	so := message.SubgroupObject{ID: o.id, Properties: o.props, Status: o.status, PayloadLen: uint64(len(o.payload))}
	if err := w.out.WriteObject(&so); err != nil {
		return false
	}
	sent := 0
	for sent < len(o.payload) {
		if f := int(o.filled.Load()); f > sent {
			n, err := w.out.Write(o.payload[sent:f])
			sent += n
			if err != nil {
				return false
			}
			continue
		}
		if o.failed.Load() {
			w.reset(message.ResetCancelled, true)
			return false
		}
		ch := o.run.sig.wait()
		if int(o.filled.Load()) > sent || o.failed.Load() || w.stopped() {
			if w.stopped() {
				return false
			}
			continue
		}
		select {
		case <-ch:
		case <-w.s.ctx.Done():
			w.reset(message.ResetCancelled, false)
			return false
		}
	}
	w.s.t.r.m.object(directionOut, deliveryStream, o.size())
	return true
}
