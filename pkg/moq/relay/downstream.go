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
	"sync"

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

type fetchHandler struct{}

func (fetchHandler) HandleMessage(r *session.Request, m message.Message) {
	// a FETCH's updates change nothing the relay honors after it begins, so each is accepted as it comes
	if _, ok := m.(*message.RequestUpdate); ok {
		_ = r.Send(&message.RequestOK{})
	}
}

func (fetchHandler) HandleClose(*session.Request, error) {}

func (d *downstream) fetch(req *session.Request, m *message.Fetch) {
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
	order := props.GroupOrder()
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
