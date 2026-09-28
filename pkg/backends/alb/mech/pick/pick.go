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
// Package pick is the HTTP adapter for selection strategies: one handler that dispatches each
// request to the pool member a strategy picks, whichever strategy that is.
package pick

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	cfgtypes "github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
)

// Options are what the adapter needs beyond the strategy itself.
type Options struct {
	// Key is where a request's affinity key is read from, for a strategy that needs one.
	Key flowkey.KeySource
	// IPv6Prefix is how many leading bits of an IPv6 address form a client_ip key.
	IPv6Prefix int
	// GoodCodes are the response codes that count as a good answer, for a strategy that
	// needs latency; nil counts every response as good.
	GoodCodes *cfgtypes.StatusTable
	// Balancer carries what the balancer itself is configured with, such as passive ejection.
	Balancer lb.BalancerOptions
	// ALBName is the name of the ALB the mechanism serves, which an outer ALB's session names.
	ALBName string
	// Sticky keeps each client on the member its session is pinned to; nil keeps no sessions.
	Sticky *sticky.HTTP
}

type handler struct {
	mech.PoolHolder
	name     types.Name
	albName  string
	balancer *lb.Balancer
	// resolved once: a strategy pays on dispatch only for what it needs
	tracked   bool
	timed     bool
	key       func(*http.Request) flowkey.Value
	goodCodes *cfgtypes.StatusTable
	sticky    *sticky.HTTP
	// follows is set once an ALB that keeps sessions has this one in its pool
	follows atomic.Bool
	// sessions is set when the mechanism keeps sessions or follows them, so one load gates both
	sessions atomic.Bool
}

// New returns the pool mechanism that serves HTTP with the provided strategy, which the
// mechanism then owns. name is the mechanism's short name.
func New(name types.Name, selector lb.Selector, opts ...Options) types.PickerMechanism {
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	b := lb.NewBalancer(selector, o.Balancer)
	h := &handler{
		name: name, albName: o.ALBName, balancer: b, sticky: o.Sticky,
		tracked: b.Needs() != 0, timed: b.Needs().Has(lb.NeedLatency),
	}
	h.goodCodes = o.GoodCodes
	h.sessions.Store(o.Sticky != nil)
	if b.Needs().Has(lb.NeedKey) {
		h.key = flowkey.HTTP(o.Key, o.IPv6Prefix)
	}
	return h
}

func (h *handler) Name() types.Name {
	return h.name
}

// Picker returns the balancer behind the mechanism, for planes that do not dispatch over HTTP.
func (h *handler) Picker() lb.Picker {
	return h.balancer
}

// SetPool installs the pool that requests are dispatched to; nil leaves the mechanism with none.
func (h *handler) SetPool(p pool.Pool) {
	h.PoolHolder.SetPool(p)
	if p == nil {
		h.balancer.SetPool(nil)
		return
	}
	h.balancer.SetPool(p.Core())
}

func (h *handler) StopPool() {
	if p := h.Pool(); p != nil {
		p.Stop()
	}
}

// Balancer returns the mechanism's balancer, whose members carry its runtime stats.
func (h *handler) Balancer() *lb.Balancer {
	return h.balancer
}

// FollowPins makes the mechanism pick the next level of the sessions of an ALB whose pool it is
// in, when that ALB sends it one.
func (h *handler) FollowPins() {
	h.follows.Store(true)
	h.sessions.Store(true)
}

// StickyTable returns the table the mechanism keeps its sessions in, or nil when it keeps none.
func (h *handler) StickyTable() *sticky.Table {
	return h.sticky.Table()
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var flow lb.Flow
	if h.key != nil && r != nil {
		v := h.key(r)
		flow = lb.Flow{Key: v.Hash, HasKey: v.OK}
	}
	if h.sessions.Load() && r != nil && h.serveSession(w, r, flow) {
		return
	}
	pk, ok := h.balancer.Pick(flow)
	if !ok {
		failures.HandleBadGateway(w, r)
		return
	}
	t, ok := pk.Member().Value.(*pool.Target)
	if !ok || t.Handler() == nil {
		pk.Done(lb.OutcomeFailed)
		failures.HandleBadGateway(w, r)
		return
	}
	r = mech.Align(r, pool.ModeOf(pk))
	// dispatch, written out: this is every request's path, and the call would not be inlined
	switch {
	case !h.tracked:
		t.Handler().ServeHTTP(w, r)
	case h.timed:
		h.serveTimed(pk, t.Handler(), w, r)
	default:
		h.serveTracked(pk, t.Handler(), w, r)
	}
}

// serveSession serves a request as part of a session: the next level of an outer ALB's, or one
// of its own; it reports false for a request it leaves to an ordinary pick
func (h *handler) serveSession(w http.ResponseWriter, r *http.Request, flow lb.Flow) bool {
	if h.follows.Load() {
		if s := sticky.SessionFrom(r.Context()); s != nil && s.Claim(h.albName) {
			h.serveFollowing(w, r, flow, s)
			return true
		}
	}
	if h.sticky == nil {
		return false
	}
	h.serveSticky(w, r, flow)
	return true
}

// target returns the pool target of a pick, or answers the request itself when there is none
func target(pk lb.Pick, ok bool, w http.ResponseWriter, r *http.Request) (*pool.Target, bool) {
	if !ok {
		failures.HandleBadGateway(w, r)
		return nil, false
	}
	t, ok := pk.Member().Value.(*pool.Target)
	if !ok || t.Handler() == nil {
		pk.Done(lb.OutcomeFailed)
		failures.HandleBadGateway(w, r)
		return nil, false
	}
	return t, true
}

func (h *handler) dispatch(pk lb.Pick, member http.Handler, w http.ResponseWriter, r *http.Request) {
	switch {
	case !h.tracked:
		member.ServeHTTP(w, r)
	case h.timed:
		h.serveTimed(pk, member, w, r)
	default:
		h.serveTracked(pk, member, w, r)
	}
}

// serveSticky keeps the request's session as the outermost ALB on its way that keeps sessions:
// it reads the session's pins, and issues its token or stores its pins as the response begins
func (h *handler) serveSticky(w http.ResponseWriter, r *http.Request, flow lb.Flow) {
	fw := getWriter(w, lb.Pick{})
	defer putWriter(fw)
	s := &fw.local
	h.sticky.Begin(r, s, time.Now())
	fw.persist, fw.session, fw.req = h.sticky, s, r
	pk, t, ok := h.pickPinned(fw, r, flow, s, 0)
	if !ok {
		return
	}
	inner := t.Picker()
	if inner != nil && pk.Pinned() && !canPick(inner, s) {
		if pk, t, ok = h.unstrand(fw, r, flow, s, pk); !ok {
			return
		}
		inner = t.Picker()
	}
	r = mech.Align(r, pool.ModeOf(pk))
	if inner != nil {
		// the member is an ALB, which picks the session's next level
		var ctx context.Context
		ctx, fw.session = sticky.Nest(r.Context(), s, pk.Member().Name())
		r = r.WithContext(ctx)
	}
	fw.pick = pk
	h.serveWritten(pk, t.Handler(), fw, r)
	// a member that wrote nothing is answered with the headers as they stand when it returns
	fw.finish()
}

// serveFollowing picks the next level of an outer ALB's session, whose pool this ALB is in
func (h *handler) serveFollowing(w http.ResponseWriter, r *http.Request, flow lb.Flow, s *sticky.Session) {
	pk, t, ok := h.pickPinned(w, r, flow, s, 1)
	if !ok {
		return
	}
	h.dispatch(pk, t.Handler(), w, mech.Align(r, pool.ModeOf(pk)))
}

// pickPinned picks for the session's level, honoring an eligible pin; when it cannot, it answers
// the request, refusing it if the pin is unavailable and must not move.
func (h *handler) pickPinned(w http.ResponseWriter, r *http.Request, flow lb.Flow, s *sticky.Session,
	level int,
) (lb.Pick, *pool.Target, bool) {
	pin, pinned := s.Pin(level)
	if pinned {
		flow.Pin, flow.HasPin = pin, true
	}
	pk, ok := h.balancer.Pick(flow)
	// a pin to a member that has left the pool starts a new session, as there is none to keep
	if pinned && !pk.Pinned() && s.Rejects() && h.balancer.Pinnable(pin) {
		if ok {
			pk.Done(lb.OutcomeCanceled)
		}
		s.Reject()
		failures.HandleServiceUnavailable(w, r)
		return lb.Pick{}, nil, false
	}
	t, ok := target(pk, ok, w, r)
	if !ok {
		return lb.Pick{}, nil, false
	}
	s.Record(level, pk.Member().Hash())
	return pk, t, true
}

// nestedPicker is the balancer of a pool member that is itself an ALB
type nestedPicker interface {
	CanPick(lb.Flow) bool
}

var _ nestedPicker = (*lb.Balancer)(nil)

// canPick reports whether an ALB member can pick the session's next level, following the pin it
// has there; one that cannot tell is assumed able to
func canPick(inner lb.Picker, s *sticky.Session) bool {
	np, ok := inner.(nestedPicker)
	if !ok {
		return true
	}
	var f lb.Flow
	f.Pin, f.HasPin = s.Pin(1)
	return np.CanPick(f)
}

// unstrand answers for a session pinned to an ALB member with nothing left to take it: reject
// refuses it and keeps its pin, while repick moves it to the first other member that can take it
func (h *handler) unstrand(w http.ResponseWriter, r *http.Request, flow lb.Flow, s *sticky.Session,
	stranded lb.Pick,
) (lb.Pick, *pool.Target, bool) {
	// the stranded member was never reached
	stranded.Done(lb.OutcomeCanceled)
	if s.Rejects() {
		s.Reject()
		failures.HandleServiceUnavailable(w, r)
		return lb.Pick{}, nil, false
	}
	// a moved session starts afresh below the first level, so an ALB member needs only a member;
	// the stranded one has none, so the walk passes it over like any other
	s.Chosen = sticky.Path{}
	for _, m := range h.balancer.Alternatives(flow, nil) {
		t, ok := m.Value.(*pool.Target)
		if !ok || t.Handler() == nil {
			continue
		}
		if inner := t.Picker(); inner != nil && !canPick(inner, s) {
			continue
		}
		pk, ok := h.balancer.Commit(m)
		if !ok {
			// the member left the pool after the alternatives were listed
			continue
		}
		s.Record(0, m.Hash())
		return pk, t, true
	}
	failures.HandleBadGateway(w, r)
	return lb.Pick{}, nil, false
}

// serveTracked reports the pick as done even when the member's handler panics, so the
// member's in-flight count cannot leak
func (h *handler) serveTracked(pk lb.Pick, member http.Handler, w http.ResponseWriter, r *http.Request) {
	outcome := lb.OutcomeFailed
	defer func() { pk.Done(outcome) }()
	member.ServeHTTP(w, r)
	outcome = lb.OutcomeOK
}

// serveTimed also times the member's first write and judges its answer: a response code
// outside the good set is a failure, and a client that went away is nobody's
func (h *handler) serveTimed(pk lb.Pick, member http.Handler, w http.ResponseWriter, r *http.Request) {
	fw := getWriter(w, pk)
	defer putWriter(fw)
	h.serveWritten(pk, member, fw, r)
}

// serveWritten dispatches through a writer that watches the member's first write, and reports
// the pick as done as serveTracked and serveTimed do
func (h *handler) serveWritten(pk lb.Pick, member http.Handler, fw *firstWriteWriter, r *http.Request) {
	outcome := lb.OutcomeFailed
	defer func() { pk.Done(outcome) }()
	member.ServeHTTP(fw, r)
	switch {
	case !h.timed:
		outcome = lb.OutcomeOK
	case r != nil && r.Context().Err() != nil:
		outcome = lb.OutcomeCanceled
	case h.goodCodes == nil || h.goodCodes.Contains(fw.code):
		outcome = lb.OutcomeOK
	}
}
