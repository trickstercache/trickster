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
	"math"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type downstream struct {
	r     *Relay
	scope Scope

	mu         sync.Mutex
	nsPrefixes []wire.Namespace
	trPrefixes []wire.Namespace
	subs       map[uint64]*subscriber
}

func (d *downstream) HandleRequest(req *session.Request) {
	switch m := req.Message().(type) {
	case *message.Subscribe:
		d.subscribe(req, m)
	case *message.Fetch:
		d.fetch(req, m)
	case *message.TrackStatus:
		d.trackStatus(req, m)
	case *message.Publish:
		d.publish(req, m)
	case *message.PublishNamespace:
		d.publishNamespace(req, m)
	case *message.SubscribeNamespace:
		d.subscribeNamespace(req, m)
	case *message.SubscribeTracks:
		d.subscribeTracks(req, m)
	default:
		d.refuse(req, kindOther, message.RequestNotSupported, reasonNotSupported)
	}
}

func (d *downstream) HandleGoAway(*session.Session, *message.GoAway) {}

func (d *downstream) subscribe(req *session.Request, m *message.Subscribe) {
	if !d.authorized(req, OpSubscribe, m.Track.Namespace, &m.Params) {
		d.refuse(req, kindSubscribe, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	t := d.r.tracks.acquire(d.r, d.scope, m.Track)
	s := newSubscriber(t, req, &m.Params)
	d.remember(req.RequestID(), s)
	req.SetHandlers(s, nil)
	if m.Params.Invalid != nil {
		s.refuse(m.Params.Invalid.Code, m.Params.Invalid.Reason)
		return
	}
	t.hold(s)
	if err := d.await(req, t, s.forward.Load(), &m.Params); err != nil {
		code, reason := refusalFor(err)
		s.refuse(code, reason)
		return
	}
	s.activate()
}

func (d *downstream) remember(id uint64, s *subscriber) {
	// a Joining FETCH names its subscription by Request ID (through draft -19)
	d.mu.Lock()
	if d.subs == nil {
		d.subs = make(map[uint64]*subscriber)
	}
	d.subs[id] = s
	d.mu.Unlock()
	s.forget = func() {
		d.mu.Lock()
		if d.subs[id] == s {
			delete(d.subs, id)
		}
		d.mu.Unlock()
	}
}

func (d *downstream) subscription(id uint64) *subscriber {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.subs[id]
}

func (d *downstream) joiningRange(req *session.Request, m *message.Fetch) (*message.Fetch, message.RequestCode,
	string,
) {
	// a Joining FETCH covers the groups before its subscription's Joining Location, through that location
	// (draft -19 §10.12.2.1); one for a pending subscription waits for it to be answered
	j := m.Joining
	s := d.subscription(j.RequestID)
	if s == nil {
		return nil, message.RequestInvalidJoiningID, "no such subscription"
	}
	t := time.NewTimer(finishWait)
	defer t.Stop()
	select {
	case <-s.ready:
	case <-t.C:
		return nil, message.RequestInvalidJoiningID, "subscription not established"
	case <-req.Context().Done():
		return nil, message.RequestInvalidJoiningID, "subscription not established"
	}
	jl, forward := s.joiningLocation()
	if !forward || jl == nil {
		return nil, message.RequestInvalidRange, "the subscription has no joining location"
	}
	start := j.Start
	if j.Relative {
		start = jl.Group - min(j.Start, jl.Group)
	}
	if start > jl.Group {
		return nil, message.RequestInvalidRange, "joining start past the joining location"
	}
	out := &message.Fetch{RequestID: m.RequestID, Track: s.t.name, Params: m.Params}
	out.Params.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: start, EndGroupDelta: jl.Group - start, EndObject: jl.Object,
	})
	return out, 0, ""
}

type fetchHandler struct{}

func (fetchHandler) HandleMessage(r *session.Request, m message.Message) {
	// a FETCH's updates change nothing the relay honors after it begins, so each is accepted as it comes
	if _, ok := m.(*message.RequestUpdate); ok {
		_ = r.Send(&message.RequestOK{})
	}
}

func (fetchHandler) HandleClose(*session.Request, error) {}

func (d *downstream) fetch(req *session.Request, m *message.Fetch) {
	if m.Joining != nil {
		joined, code, reason := d.joiningRange(req, m)
		if code != 0 {
			d.refuse(req, kindFetch, code, reason)
			return
		}
		m = joined
	}
	if !d.authorized(req, OpSubscribe, m.Track.Namespace, &m.Params) {
		d.refuse(req, kindFetch, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	req.SetHandlers(fetchHandler{}, nil)
	refuse := func(code message.RequestCode, reason string) {
		_ = req.Reject(code, reason, 0)
		d.r.m.request(kindFetch, resultRefused)
	}
	if m.Params.Invalid != nil {
		refuse(m.Params.Invalid.Code, m.Params.Invalid.Reason)
		return
	}
	t := d.r.tracks.acquire(d.r, d.scope, m.Track)
	defer d.r.tracks.release(t)
	largest, props, err := t.status(req.Context())
	if err != nil {
		refuse(refusalFor(err))
		return
	}
	if largest == nil {
		refuse(message.RequestInvalidRange, "no objects published")
		return
	}
	start, end, _, ok := m.Params.LocationFilter.Bounds(largest, true)
	if largest.Less(end) {
		end = *largest
	} else if end.Object == math.MaxUint64 {
		// a range closing at a group's end names its last object when known, which peers on drafts through -19 need
		if last, known := t.lastObject(end.Group); known {
			end.Object = last
		}
	}
	if !ok || largest.Less(start) || end.Less(start) {
		refuse(message.RequestInvalidRange, "range outside the track")
		return
	}
	fok := &message.FetchOK{EndLocation: end, EndOfTrack: t.endsBy(end)}
	if includeProperties(m) {
		fok.TrackProperties = props
	}
	if err = req.Send(fok); err != nil {
		return
	}
	d.r.m.request(kindFetch, resultOK)
	// a FETCH naming no group order is served ascending, whatever the track's publisher prefers (§9.20.9)
	order := message.GroupOrderAscending
	if m.Params.Has(message.ParamGroupOrder) {
		order = m.Params.GroupOrder
	}
	prio := m.Params.SubscriberPriorityOr(message.DefaultPublisherPriority)
	j := &fetchJob{
		t: t, owner: req, requestID: req.RequestID(), start: start, end: end, order: order,
		rng: newRanges(m.Params.RangeFilters), urgency: session.Urgency(prio, props.PublisherPriority()),
		budget: d.r.fillBudget(&m.Params),
	}
	if j.run(req.Context()) == nil {
		_ = req.CloseSend()
	}
}

func (d *downstream) trackStatus(req *session.Request, m *message.TrackStatus) {
	if !d.authorized(req, OpSubscribe, m.Track.Namespace, &m.Params) {
		d.refuse(req, kindTrackStatus, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	req.SetHandlers(nopRequestHandler{}, nil)
	t := d.r.tracks.acquire(d.r, d.scope, m.Track)
	defer d.r.tracks.release(t)
	largest, props, err := t.status(req.Context())
	if err != nil {
		code, reason := refusalFor(err)
		_ = req.Reject(code, reason, 0)
		d.r.m.request(kindTrackStatus, resultRefused)
		return
	}
	ok := &message.RequestOK{}
	if largest != nil {
		ok.Params.SetLargestObject(*largest)
	}
	if includeProperties(m) {
		ok.TrackProperties = props
	}
	if req.Send(ok) == nil {
		_ = req.CloseSend()
		d.r.m.request(kindTrackStatus, resultOK)
	}
}
