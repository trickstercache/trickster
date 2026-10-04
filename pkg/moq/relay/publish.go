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
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	publishOpenTimeout = time.Second

	reasonUnauthorized  = "not authorized"
	reasonNoPublishing  = "publishing is not allowed here"
	reasonNamespace     = "namespace not open to publishers"
	reasonMandatory     = "unsupported mandatory track property"
	reasonPrefixOverlap = "prefix overlaps an active request"
	reasonNotSupported  = "not supported by this relay"
)

func (d *downstream) refuse(req *session.Request, kind string, code message.RequestCode, reason string) {
	req.SetHandlers(nopRequestHandler{}, nil)
	_ = req.Reject(code, reason, 0)
	d.r.m.request(kind, resultRefused)
}

func (d *downstream) authorized(req *session.Request, op Operation, ns wire.Namespace, p *message.Params) bool {
	return d.r.access().authorized(req, op, ns, p.AuthTokens)
}

func (d *downstream) mayPublish(req *session.Request, kind string, ns wire.Namespace, p *message.Params) bool {
	// a backend that takes no publishers does not support publishing; one that does checks the namespace and tokens
	a := d.r.access()
	switch {
	case !a.Publish:
		d.refuse(req, kind, message.RequestNotSupported, reasonNoPublishing)
	case !a.mayPublish(ns):
		d.refuse(req, kind, message.RequestUnauthorized, reasonNamespace)
	case !a.authorized(req, OpPublish, ns, p.AuthTokens):
		d.refuse(req, kind, message.RequestUnauthorized, reasonUnauthorized)
	default:
		return true
	}
	return false
}

func (d *downstream) await(req *session.Request, t *track, forward bool, p *message.Params) error {
	// a SUBSCRIBE with RENDEZVOUS_TIMEOUT for a track nobody publishes yet waits for a publisher (§9.20.6)
	t.mu.Lock()
	arrived := t.arrivalLocked()
	t.mu.Unlock()
	err := t.ensureUpstream(req.Context(), forward)
	if !errors.Is(err, errNoUpstream) || p.RendezvousTimeout == 0 {
		return err
	}
	timer := time.NewTimer(min(millis(p.RendezvousTimeout), d.r.opts.MaxRendezvous))
	defer timer.Stop()
	sess := req.Session()
	t.await(sess, 1)
	defer t.await(sess, -1)
	for errors.Is(err, errNoUpstream) {
		select {
		case <-arrived:
		case <-timer.C:
			return errRendezvous
		case <-req.Context().Done():
			return req.Context().Err()
		}
		t.mu.Lock()
		arrived = t.arrivalLocked()
		t.mu.Unlock()
		err = t.ensureUpstream(req.Context(), forward)
	}
	return err
}

func (t *track) await(sess *session.Session, delta int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.awaiting == nil {
		t.awaiting = make(map[*session.Session]int)
	}
	if t.awaiting[sess] += delta; t.awaiting[sess] <= 0 {
		delete(t.awaiting, sess)
	}
}

func (t *track) awaitedBy(sess *session.Session) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.awaiting[sess] > 0
}

func (d *downstream) publish(req *session.Request, m *message.Publish) {
	// a publisher's PUBLISH becomes one of the track's sources (§7.6)
	if !d.mayPublish(req, kindPublish, m.Track.Namespace, &m.Params) {
		return
	}
	if _, mandatory := m.TrackProperties.Mandatory(); mandatory {
		// §3.7: a track with a required property the relay does not understand is neither processed nor forwarded
		d.refuse(req, kindPublish, message.RequestUnsupportedExtension, reasonMandatory)
		return
	}
	t := d.r.tracks.acquire(d.r, d.scope, m.Track)
	u := &upstreamSub{t: t, pushed: true, req: req, up: true}
	req.SetHandlers(u, u)
	if err := t.addPublisher(u, m); err != nil {
		code, reason := refusalFor(err)
		_ = req.Reject(code, reason, 0)
		d.r.m.request(kindPublish, resultRefused)
		d.r.tracks.release(t)
		return
	}
	d.r.m.publishers(1)
	if err := req.Send(&message.RequestOK{}); err != nil {
		t.upstreamEnded(u, err)
		return
	}
	d.r.m.request(kindPublish, resultOK)
	go t.syncForward()
	d.r.offerTrack(t)
}

func (d *downstream) publishNamespace(req *session.Request, m *message.PublishNamespace) {
	// the relay routes later subscriptions under the prefix to the session until it withdraws it (§4.1)
	if !d.mayPublish(req, kindPublishNamespace, m.Prefix, &m.Params) {
		return
	}
	p := &nsPublisher{scope: d.scope, prefix: m.Prefix.Clone(), key: namespaceKey(m.Prefix), sess: req.Session()}
	req.SetHandlers(&withdrawal{done: func() { d.r.removeNamespacePublisher(p) }}, nil)
	if err := req.Send(&message.RequestOK{}); err != nil {
		return
	}
	d.r.m.request(kindPublishNamespace, resultOK)
	d.r.addNamespacePublisher(p)
	if req.Context().Err() != nil {
		// the request ended before it was registered, so its withdrawal found nothing to remove
		d.r.removeNamespacePublisher(p)
	}
}

type withdrawal struct {
	done func()
}

func (w *withdrawal) HandleMessage(r *session.Request, m message.Message) {
	// a REQUEST_UPDATE refreshes the request's authorization, which the relay accepts as given
	if _, ok := m.(*message.RequestUpdate); ok {
		_ = r.Send(&message.RequestOK{})
	}
}

func (w *withdrawal) HandleClose(*session.Request, error) {
	w.done()
}

func (d *downstream) claim(prefixes *[]wire.Namespace, prefix wire.Namespace) bool {
	// two requests of one kind in a session may not overlap: neither prefix may be a prefix of the other (§4.2)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range *prefixes {
		if p.HasPrefix(prefix) || prefix.HasPrefix(p) {
			return false
		}
	}
	*prefixes = append(*prefixes, prefix)
	return true
}

func (d *downstream) unclaim(prefixes *[]wire.Namespace, prefix wire.Namespace) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := slices.IndexFunc(*prefixes, prefix.Equal); i >= 0 {
		*prefixes = slices.Delete(*prefixes, i, i+1)
	}
}

func (d *downstream) subscribeNamespace(req *session.Request, m *message.SubscribeNamespace) {
	if !d.authorized(req, OpSubscribe, m.Prefix, &m.Params) {
		d.refuse(req, kindSubscribeNamespace, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	prefix := m.Prefix.Clone()
	if !d.claim(&d.nsPrefixes, prefix) {
		d.refuse(req, kindSubscribeNamespace, message.RequestPrefixOverlap, reasonPrefixOverlap)
		return
	}
	l := &nsListener{scope: d.scope, prefix: prefix, req: req}
	req.SetHandlers(&withdrawal{done: func() {
		d.r.removeNamespaceListener(l)
		d.unclaim(&d.nsPrefixes, prefix)
	}}, nil)
	if err := req.Send(&message.RequestOK{}); err != nil {
		return
	}
	d.r.m.request(kindSubscribeNamespace, resultOK)
	d.r.addNamespaceListener(l)
	if req.Context().Err() != nil {
		d.r.removeNamespaceListener(l)
	}
}

func (d *downstream) subscribeTracks(req *session.Request, m *message.SubscribeTracks) {
	if !d.authorized(req, OpSubscribe, m.Prefix, &m.Params) {
		d.refuse(req, kindSubscribeTracks, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	prefix := m.Prefix.Clone()
	if !d.claim(&d.trPrefixes, prefix) {
		d.refuse(req, kindSubscribeTracks, message.RequestPrefixOverlap, reasonPrefixOverlap)
		return
	}
	l := &trackListener{
		r: d.r, scope: d.scope, prefix: prefix, req: req, params: m.Params,
		offered: make(map[string]struct{}),
	}
	l.params.AuthTokens = nil
	req.SetHandlers(&trackListenerHandler{l: l, done: func() {
		d.r.removeTrackListener(l)
		d.unclaim(&d.trPrefixes, prefix)
	}}, nil)
	if err := req.Send(&message.RequestOK{}); err != nil {
		return
	}
	d.r.m.request(kindSubscribeTracks, resultOK)
	d.r.addTrackListener(l)
	if req.Context().Err() != nil {
		d.r.removeTrackListener(l)
	}
}

type trackListenerHandler struct {
	l    *trackListener
	done func()
}

func (h *trackListenerHandler) HandleMessage(r *session.Request, m message.Message) {
	// an update changes the initial parameters of the subscriptions the request causes from now on (§9.20.18)
	u, ok := m.(*message.RequestUpdate)
	if !ok {
		return
	}
	h.l.mu.Lock()
	if u.Params.Has(message.ParamForward) {
		h.l.params.SetForward(u.Params.Forward)
	}
	h.l.mu.Unlock()
	_ = r.Send(&message.RequestOK{})
}

func (h *trackListenerHandler) HandleClose(*session.Request, error) {
	h.done()
}

func publishParams(from *message.Params, largest *wire.Location) message.Params {
	// a PUBLISH carries the subscription's initial delivery parameters (§9.8); filters stay at the relay
	var p message.Params
	if from.Has(message.ParamForward) {
		p.SetForward(from.Forward)
	}
	if from.Has(message.ParamGroupOrder) {
		p.SetGroupOrder(from.GroupOrder)
	}
	if from.Has(message.ParamSubscriberPriority) {
		p.SetSubscriberPriority(from.SubscriberPriority)
	}
	if from.Has(message.ParamObjectDeliveryTimeout) {
		p.SetObjectDeliveryTimeout(from.ObjectDeliveryTimeout)
	}
	if from.Has(message.ParamSubgroupDeliveryTimeout) {
		p.SetSubgroupDeliveryTimeout(from.SubgroupDeliveryTimeout)
	}
	if from.Has(message.ParamLocationFilter) {
		p.SetLocationFilter(from.LocationFilter)
	}
	if largest != nil {
		p.SetLargestObject(*largest)
	}
	return p
}

func (r *Relay) publishTo(l *trackListener, name message.FullTrackName) {
	// the relay offers a published track to a SUBSCRIBE_TRACKS subscriber with a PUBLISH of its own (§3.6)
	sess := l.req.Session()
	t := r.tracks.acquire(r, l.scope, name)
	l.mu.Lock()
	params := l.params
	l.mu.Unlock()
	s := newSubscriber(t, nil, &params)
	t.mu.Lock()
	largest, props, malformed := t.largestLocked(), t.props, t.malformed
	t.mu.Unlock()
	if malformed {
		s.releaseTrack()
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, publishOpenTimeout)
	req, err := sess.Open(ctx, &message.Publish{
		Track: name, TrackAlias: sess.NewTrackAlias(), Params: publishParams(&params, largest), TrackProperties: props,
	}, s, nil)
	cancel()
	if err != nil {
		// §3.6.3: a subscription that cannot be created is reported, and never offered again
		s.releaseTrack()
		l.skipped(name)
		r.m.request(kindPublish, resultRefused)
		return
	}
	// objects may lead the subscriber's answer (§3.1), so the subscription forwards from the PUBLISH on
	s.bind(req)
	s.activatePublished()
	resp, err := req.Response(req.Context())
	if err != nil {
		s.end(message.ResetCancelled)
		return
	}
	switch resp.(type) {
	case *message.RequestOK, *message.PublishOK:
		r.m.request(kindPublish, resultOK)
	default:
		// the subscriber declined, typically with UNINTERESTED
		s.end(message.ResetCancelled)
		r.m.request(kindPublish, resultRefused)
	}
}
