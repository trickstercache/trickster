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
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	publishOpenTimeout = time.Second
	publishPullTimeout = 5 * time.Second

	reasonUnauthorized  = "not authorized"
	reasonNoPublishing  = "publishing is not allowed here"
	reasonNamespace     = "namespace not open to publishers"
	reasonMandatory     = "unsupported mandatory track property"
	reasonPrefixOverlap = "prefix overlaps an active request"
	reasonNotSupported  = "not supported by this relay"
	reasonRelativeFetch = "a fetch range is absolute"
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
	d.r.attachPublisher(d.scope, req, m, false)
}

func (r *Relay) attachPublisher(scope Scope, req *session.Request, m *message.Publish, origin bool) {
	if _, mandatory := m.TrackProperties.Mandatory(); mandatory {
		// §3.7: a track with a required property the relay does not understand is neither processed nor forwarded
		req.SetHandlers(nopRequestHandler{}, nil)
		reject(req, refusal(unsupported(m.TrackProperties)))
		r.m.request(kindPublish, resultRefused)
		return
	}
	if malformedProps(req.Session(), m.TrackProperties, true) {
		req.SetHandlers(nopRequestHandler{}, nil)
		reject(req, &message.RequestError{Code: message.RequestMalformedTrack, Reason: "malformed track"})
		r.m.request(kindPublish, resultRefused)
		return
	}
	t := r.tracks.acquire(r, scope, m.Track)
	u := &upstreamSub{t: t, pushed: true, req: req, up: true}
	req.SetHandlers(u, u)
	if err := t.addPublisher(u, m); err != nil {
		reject(req, refusal(err))
		r.m.request(kindPublish, resultRefused)
		r.tracks.release(t)
		return
	}
	if origin {
		t.mu.Lock()
		t.fromOrigin = true
		t.mu.Unlock()
	}
	r.negs.forget(t)
	r.m.publishers(1)
	if err := req.Send(&message.RequestOK{}); err != nil {
		t.upstreamEnded(u, err)
		return
	}
	r.m.request(kindPublish, resultOK)
	go t.syncForward()
	r.offerTrack(t)
}

// OriginPublish takes a PUBLISH the origin sent on an upstream session, in answer to the relay's SUBSCRIBE_TRACKS,
// reporting false when no subscription of the relay's on that session covers the track, or none downstream wants it
func (r *Relay) OriginPublish(req *session.Request, m *message.Publish) bool {
	sess := req.Session()
	n := &r.ns
	n.mu.Lock()
	covered := false
	for _, u := range n.upstreams[prefixTracks] {
		if u.covers(sess, m.Track.Namespace) {
			covered = true
			break
		}
	}
	var scope Scope
	wanted := false
	for _, l := range n.trackers {
		if m.Track.Namespace.HasPrefix(l.prefix) {
			scope, wanted = l.scope, true
			break
		}
	}
	n.mu.Unlock()
	if !covered || !wanted {
		return false
	}
	r.attachPublisher(scope, req, m, true)
	return true
}

func (t *track) originPublished() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fromOrigin && t.publisherCountLocked() > 0
}

func (t *track) lingerIdle() {
	// a pull nobody came to use ends after the linger, as one whose last subscriber left does
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.subs) == 0 && t.held == 0 && t.hasPullLocked() && t.linger == nil && !t.closed {
		t.linger = time.AfterFunc(t.r.opts.Linger, t.lingerExpired)
	}
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

func (d *downstream) claim(req *session.Request, prefixes *[]wire.Namespace, prefix wire.Namespace) bool {
	// through -22 two requests of one kind in a session may not overlap: neither prefix may be a prefix of the
	// other (§4.2); from -23 they may, and each is answered on its own
	if req.Session().Codec().Has(codec.CapOverlappingPrefixes) {
		return true
	}
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
	if !d.claim(req, &d.nsPrefixes, prefix) {
		d.refuse(req, kindSubscribeNamespace, message.RequestPrefixOverlap, reasonPrefixOverlap)
		return
	}
	// draft -17 asks for namespaces, PUBLISHes or both in one request, which later drafts split in two
	namespaces, tracks := true, false
	if !req.Session().Codec().Has(codec.CapSubscribeTracks) {
		namespaces = m.Options != message.SubscribeOptionPublish
		tracks = m.Options != message.SubscribeOptionNamespace
	}
	h := &prefixHandler{
		d: d, kind: kindSubscribeNamespace, slots: &d.nsPrefixes, prefix: prefix, tokens: m.Params.AuthTokens,
		active: make(chan struct{}),
	}
	if namespaces {
		h.ns = &nsListener{scope: d.scope, prefix: prefix, req: req}
	}
	if tracks {
		h.tl = &trackListener{
			r: d.r, scope: d.scope, prefix: prefix, req: req, params: m.Params,
			offered: make(map[string]struct{}),
		}
		h.tl.params.AuthTokens = nil
	}
	h.start(req)
}

func (d *downstream) subscribeTracks(req *session.Request, m *message.SubscribeTracks) {
	if !d.authorized(req, OpSubscribe, m.Prefix, &m.Params) {
		d.refuse(req, kindSubscribeTracks, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	prefix := m.Prefix.Clone()
	if !d.claim(req, &d.trPrefixes, prefix) {
		d.refuse(req, kindSubscribeTracks, message.RequestPrefixOverlap, reasonPrefixOverlap)
		return
	}
	h := &prefixHandler{
		d: d, kind: kindSubscribeTracks, slots: &d.trPrefixes, prefix: prefix, tokens: m.Params.AuthTokens,
		active: make(chan struct{}),
		tl: &trackListener{
			r: d.r, scope: d.scope, prefix: prefix, req: req, params: m.Params, offered: make(map[string]struct{}),
		},
	}
	h.tl.params.AuthTokens = nil
	h.start(req)
}

type prefixHandler struct {
	// a SUBSCRIBE_NAMESPACE or SUBSCRIBE_TRACKS: its namespaces, its PUBLISHes, or both (draft -17), and the
	// updates that change its prefix or its subscriptions' initial FORWARD
	d      *downstream
	kind   string
	slots  *[]wire.Namespace
	tokens []wire.Token
	ns     *nsListener
	tl     *trackListener
	// active closes once the listeners are registered, which updates wait for
	active chan struct{}

	mu     sync.Mutex
	prefix wire.Namespace
}

func (h *prefixHandler) start(req *session.Request) {
	d := h.d
	req.SetHandlers(h, nil)
	defer close(h.active)
	if err := req.Send(&message.RequestOK{}); err != nil {
		return
	}
	d.r.m.request(h.kind, resultOK)
	if h.ns != nil {
		d.r.addNamespaceListener(h.ns)
	}
	if h.tl != nil {
		d.r.addTrackListener(h.tl)
	}
	if req.Context().Err() != nil {
		h.remove()
	}
}

func (h *prefixHandler) remove() {
	if h.ns != nil {
		h.d.r.removeNamespaceListener(h.ns)
	}
	if h.tl != nil {
		h.d.r.removeTrackListener(h.tl)
	}
}

func (h *prefixHandler) HandleMessage(r *session.Request, m message.Message) {
	// an update changes the initial parameters of the subscriptions the request causes from now on (§9.20.18),
	// or the prefix, and is answered in order with what the request sends
	u, ok := m.(*message.RequestUpdate)
	if !ok {
		return
	}
	select {
	case <-h.active:
	case <-r.Context().Done():
		return
	}
	if h.tl != nil && u.Params.Has(message.ParamForward) {
		h.tl.mu.Lock()
		h.tl.params.SetForward(u.Params.Forward)
		h.tl.mu.Unlock()
	}
	if u.Params.Has(message.ParamTrackNamespacePrefix) {
		h.updatePrefix(r, &u.Params)
		return
	}
	h.answer(r, &message.RequestOK{}, false)
}

func (h *prefixHandler) answer(r *session.Request, m message.Message, fin bool) {
	// namespaces are reported in order with the answers, whose prefix they are relative to
	if h.ns != nil {
		h.ns.push(nsEvent{msg: m, fin: fin})
		return
	}
	if r.Send(m) == nil && fin {
		_ = r.CloseSend()
	}
}

func (h *prefixHandler) fail(r *session.Request, code message.RequestCode, reason string) {
	// a refused update closes this side of the request's stream, which ends the request
	h.answer(r, &message.RequestError{Code: code, Reason: reason}, true)
}

func (h *prefixHandler) updatePrefix(r *session.Request, p *message.Params) {
	d := h.d
	prefix := p.TrackNamespacePrefix.Clone()
	tokens := p.AuthTokens
	if len(tokens) == 0 {
		tokens = h.tokens
	}
	if !d.r.access().authorized(r, OpSubscribe, prefix, tokens) {
		h.fail(r, message.RequestUnauthorized, reasonUnauthorized)
		return
	}
	h.mu.Lock()
	old := h.prefix
	d.unclaim(h.slots, old)
	if !d.claim(r, h.slots, prefix) {
		d.claim(r, h.slots, old)
		h.mu.Unlock()
		h.fail(r, message.RequestPrefixOverlap, reasonPrefixOverlap)
		return
	}
	h.prefix = prefix
	h.mu.Unlock()
	if h.ns != nil {
		d.r.reprefixNamespaces(h.ns, prefix, &message.RequestOK{})
	} else {
		_ = r.Send(&message.RequestOK{})
	}
	if h.tl != nil {
		d.r.reprefixTracks(h.tl, prefix)
	}
}

func (h *prefixHandler) HandleClose(*session.Request, error) {
	h.remove()
	h.mu.Lock()
	prefix := h.prefix
	h.mu.Unlock()
	h.d.unclaim(h.slots, prefix)
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
	skip := func() {
		// §3.6.3: a subscription that cannot be created is reported, and never offered again
		t.lingerIdle()
		s.end(message.ResetCancelled)
		s.stop()
		l.skipped(name)
		r.m.request(kindPublish, resultRefused)
	}
	// a forwarding offer counts as forwarding until it registers, so the upstream forwards before the PUBLISH's
	// Largest Object is taken and stays so; a track offered from another scope is pulled from the origin first
	t.hold(s)
	ctx, cancel := context.WithTimeout(r.ctx, publishPullTimeout)
	err := t.ensureUpstream(ctx, s.forward.Load())
	cancel()
	if err != nil {
		skip()
		return
	}
	if _, err = t.currentLargest(r.ctx, sess.Codec().Has(codec.CapFreshLargest)); err != nil {
		skip()
		return
	}
	// the subscription registers at the Largest Object the PUBLISH advertises, so its filter starts there and what
	// arrives after it is forwarded once the PUBLISH is sent, the fill covering what came before
	largest, props, code, _ := s.register()
	if code != 0 || !s.answer() {
		skip()
		return
	}
	pp := publishParams(&params, largest)
	if sess.Codec().Has(codec.CapParams23) && l.req.Kind() == message.KindSubscribeTracks {
		// from -23 a PUBLISH names the SUBSCRIBE_TRACKS it answers, since several may match the track, and carries
		// its fill, which the relay serves under the PUBLISH's Request ID
		pp.SetSubscribeTracksID(l.req.RequestID())
		if params.Fill != nil {
			pp.SetFill(params.Fill)
		}
	}
	ctx, cancel = context.WithTimeout(r.ctx, publishOpenTimeout)
	req, err := sess.Open(ctx, &message.Publish{
		Track: name, TrackAlias: sess.NewTrackAlias(), Params: pp, TrackProperties: props,
	}, s, nil)
	cancel()
	if err != nil {
		skip()
		return
	}
	// objects may lead the subscriber's answer (§3.1), so the subscription forwards from the PUBLISH on
	s.bind(req)
	s.activatePublished()
	if pp.Fill != nil && s.forward.Load() {
		s.startFill(req.RequestID(), pp.Fill, largest)
	}
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
