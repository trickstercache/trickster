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
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type upState uint8

const (
	upIdle upState = iota
	upPending
	upEstablished
)

var (
	errMalformed  = errors.New("moq: track is malformed")
	errNoUpstream = &requestError{code: message.RequestDoesNotExist, reason: "no publisher for the track"}
	errUpstream   = &requestError{code: message.RequestInternalError, reason: "upstream unavailable"}
)

type requestError struct {
	code   message.RequestCode
	reason string
}

func (e *requestError) Error() string {
	return "moq: request refused: " + e.reason
}

type publishDone struct {
	code   message.DoneCode
	reason string
}

func (e *publishDone) Error() string {
	return "moq: upstream subscription done: " + e.reason
}

type track struct {
	r     *Relay
	scope Scope
	name  message.FullTrackName
	key   string
	hash  uint64
	refs  int

	mu         sync.Mutex
	props      message.Properties
	hasLargest bool
	largest    wire.Location
	groups     map[uint64]*group
	newest     *group
	endOfTrack *wire.Location
	malformed  bool
	subs       []*subscriber
	forwarders int
	held       int
	spilled    spillIndex
	closed     bool

	up         *upstreamSub
	upState    upState
	upReady    chan struct{}
	upErr      error
	upForward  bool
	fwdAck     chan struct{}
	fwdPending int
	linger     *time.Timer
	retries    int

	ctrlMu sync.Mutex
}

func newTrack(r *Relay, scope Scope, name message.FullTrackName, key string, hash uint64) *track {
	return &track{r: r, scope: scope, name: name, key: key, hash: hash, groups: make(map[uint64]*group)}
}

func (t *track) idle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.subs) == 0 && t.upState == upIdle && len(t.groups) == 0 && t.linger == nil
}

func (t *track) largestLocked() *wire.Location {
	if !t.hasLargest {
		return nil
	}
	l := t.largest
	return &l
}

func (t *track) raiseLargest(l wire.Location) {
	if !t.hasLargest || t.largest.Less(l) {
		t.largest, t.hasLargest = l, true
	}
}

func (t *track) wantForwardLocked() bool {
	return t.forwarders > 0 || t.held > 0 || t.r.opts.Prewarm
}

func (t *track) hold(s *subscriber) {
	// a subscriber awaiting the upstream subscription counts as forwarding, so the upstream never pauses for
	// the moment between its establishment and the subscriber's registration
	t.mu.Lock()
	if s.forward.Load() && !s.held {
		s.held = true
		t.held++
	}
	t.mu.Unlock()
}

func (t *track) unholdLocked(s *subscriber) {
	if s.held {
		s.held = false
		t.held--
	}
}

func (t *track) ensureUpstream(ctx context.Context, forward bool) error {
	// returns once the track has an established upstream subscription, opening one if needed
	t.mu.Lock()
	if t.malformed {
		t.mu.Unlock()
		return errMalformed
	}
	switch t.upState {
	case upEstablished:
		t.mu.Unlock()
		if forward {
			t.resume(ctx)
		}
		return nil
	case upIdle:
		t.startUpstreamLocked(forward || t.wantForwardLocked())
	}
	ready := t.upReady
	t.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.upState == upEstablished {
		return nil
	}
	if t.upErr == nil {
		return errUpstream
	}
	return t.upErr
}

func (t *track) startUpstreamLocked(forward bool) {
	t.upState = upPending
	t.upReady = make(chan struct{})
	u := &upstreamSub{t: t}
	t.up = u
	go u.open(forward)
}

func (t *track) established(u *upstreamSub, ok *message.SubscribeOK, forward bool) bool {
	t.mu.Lock()
	if t.up != u || t.upState != upPending {
		t.mu.Unlock()
		return false
	}
	t.upState, t.upForward, t.retries = upEstablished, forward, 0
	if len(ok.TrackProperties) > 0 || t.props == nil {
		t.props = bytes.Clone(ok.TrackProperties)
	}
	if ok.Params.Has(message.ParamLargestObject) {
		t.raiseLargest(ok.Params.LargestObject)
	}
	close(t.upReady)
	t.mu.Unlock()
	t.r.m.subscriptions(directionUpstream, 1)
	go t.syncForward()
	return true
}

func (t *track) upstreamEnded(u *upstreamSub, cause error) {
	// an upstream subscription, or an attempt to open one, ended
	t.mu.Lock()
	if t.up != u {
		t.mu.Unlock()
		return
	}
	wasUp := t.upState == upEstablished
	if t.malformed {
		cause = errMalformed
	}
	t.ackedLocked(true)
	if t.upState == upPending {
		t.upErr = cause
		close(t.upReady)
	}
	t.up, t.upState = nil, upIdle
	resubscribe := len(t.subs) > 0 && !t.closed && !t.malformed && retryable(cause) &&
		t.retries < reconnectAttempts
	var end []*subscriber
	if resubscribe {
		backoff := t.r.opts.ReconnectBackoff << t.retries
		t.retries++
		t.upState = upPending
		t.upReady = make(chan struct{})
		nu := &upstreamSub{t: t}
		t.up = nu
		forward := t.wantForwardLocked()
		time.AfterFunc(backoff, func() { nu.open(forward) })
	} else if len(t.subs) > 0 {
		end = slices.Clone(t.subs)
	}
	t.mu.Unlock()
	if wasUp {
		t.r.m.subscriptions(directionUpstream, -1)
	}
	if len(end) > 0 {
		code, reason := doneFor(cause)
		for _, s := range end {
			go s.finish(code, reason)
		}
	}
	t.r.tracks.dropIdle(t)
}

func retryable(cause error) bool {
	if re, ok := errors.AsType[*requestError](cause); ok {
		return re.code == message.RequestInternalError || re.code == message.RequestTimeout ||
			re.code == message.RequestGoingAway || re.code == message.RequestExcessiveLoad
	}
	if pd, ok := errors.AsType[*publishDone](cause); ok {
		switch pd.code {
		case message.DoneGoingAway, message.DoneTooFarBehind, message.DoneExpired, message.DoneInternalError,
			message.DoneExcessiveLoad:
			return true
		}
		return false
	}
	return cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, errMalformed)
}

func doneFor(cause error) (message.DoneCode, string) {
	if pd, ok := errors.AsType[*publishDone](cause); ok {
		return pd.code, pd.reason
	}
	var re *requestError
	if errors.As(cause, &re) && re.code == message.RequestUnauthorized {
		return message.DoneUnauthorized, re.reason
	}
	if errors.Is(cause, errMalformed) {
		return message.DoneMalformedTrack, "malformed track"
	}
	return message.DoneInternalError, "upstream subscription lost"
}

func refusalFor(err error) (message.RequestCode, string) {
	if re, ok := errors.AsType[*requestError](err); ok {
		return re.code, re.reason
	}
	if errors.Is(err, errMalformed) {
		return message.RequestMalformedTrack, "malformed track"
	}
	return message.RequestInternalError, "upstream unavailable"
}

func (t *track) addSubscriber(s *subscriber) {
	// the caller holds t.mu; the list is copied on write, so a snapshot taken under the lock stays valid after it
	t.unholdLocked(s)
	t.subs = append(slices.Clip(t.subs), s)
	if s.forward.Load() {
		t.forwarders++
	}
	if t.linger != nil {
		t.linger.Stop()
		t.linger = nil
	}
}

func (t *track) removeSubscriber(s *subscriber) {
	t.mu.Lock()
	t.unholdLocked(s)
	i := slices.Index(t.subs, s)
	if i < 0 {
		t.mu.Unlock()
		go t.syncForward()
		return
	}
	t.subs = slices.Delete(slices.Clone(t.subs), i, i+1)
	if s.forward.Load() {
		t.forwarders--
	}
	if len(t.subs) == 0 && t.up != nil && t.linger == nil && !t.closed {
		t.linger = time.AfterFunc(t.r.opts.Linger, t.lingerExpired)
	}
	t.mu.Unlock()
	go t.syncForward()
}

func (t *track) setForward(s *subscriber, forward bool) {
	t.mu.Lock()
	if s.forward.Load() != forward && slices.Contains(t.subs, s) {
		if forward {
			t.forwarders++
		} else {
			t.forwarders--
		}
	}
	s.forward.Store(forward)
	t.mu.Unlock()
	go t.syncForward()
}

func (t *track) lingerExpired() {
	t.mu.Lock()
	t.linger = nil
	if len(t.subs) > 0 {
		t.mu.Unlock()
		return
	}
	u := t.up
	t.mu.Unlock()
	if u != nil {
		u.cancel()
	}
}

func (t *track) resume(ctx context.Context) {
	// a subscriber answered while the upstream is still paused, or about to pause, would miss what is published
	// before the upstream forwards again, so every FORWARD update in flight is answered first
	t.syncForward()
	t.mu.Lock()
	ack := t.fwdAck
	t.mu.Unlock()
	if ack == nil {
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	case <-time.After(finishWait):
	}
}

func (t *track) acked() {
	t.mu.Lock()
	t.ackedLocked(false)
	t.mu.Unlock()
}

func (t *track) ackedLocked(all bool) {
	if t.fwdPending == 0 {
		return
	}
	t.fwdPending--
	if all {
		t.fwdPending = 0
	}
	if t.fwdPending == 0 && t.fwdAck != nil {
		close(t.fwdAck)
		t.fwdAck = nil
	}
}

func (t *track) syncForward() {
	// the upstream subscription's FORWARD state follows the downstream subscribers'
	t.ctrlMu.Lock()
	defer t.ctrlMu.Unlock()
	t.mu.Lock()
	want, sent := t.wantForwardLocked(), t.upForward
	var req *session.Request
	if t.upState == upEstablished && t.up != nil {
		req = t.up.request()
	}
	t.mu.Unlock()
	if req == nil || want == sent {
		return
	}
	var p message.Params
	p.SetForward(want)
	t.mu.Lock()
	if t.fwdAck == nil {
		t.fwdAck = make(chan struct{})
	}
	t.fwdPending++
	t.mu.Unlock()
	if _, err := req.Update(p); err != nil {
		t.acked()
		return
	}
	t.mu.Lock()
	t.upForward = want
	t.mu.Unlock()
}

func (t *track) shutdown() {
	t.mu.Lock()
	t.closed = true
	u := t.up
	if t.linger != nil {
		t.linger.Stop()
		t.linger = nil
	}
	t.mu.Unlock()
	if u != nil {
		u.cancel()
	}
}

func (t *track) markMalformed() {
	t.mu.Lock()
	if t.malformed {
		t.mu.Unlock()
		return
	}
	t.malformed = true
	u := t.up
	subs := slices.Clone(t.subs)
	groups := make([]*group, 0, len(t.groups))
	for _, g := range t.groups {
		groups = append(groups, g)
	}
	t.mu.Unlock()
	t.r.m.fail(errClassMalformed)
	// nothing of a malformed track is cached or forwarded further
	for _, g := range groups {
		t.evict(g, false)
	}
	for _, s := range subs {
		go s.finish(message.DoneMalformedTrack, "malformed track")
	}
	if u != nil {
		u.cancel()
	}
}

func (t *track) isNewest(g *group) bool {
	return g.newest.Load()
}

func (t *track) groupLocked(id uint64) *group {
	// the caller holds t.mu; a group past the live edge's count evicts the oldest other than the newest and
	// this one, which is about to receive objects, so a late group can push the count over by one
	if g, ok := t.groups[id]; ok {
		return g
	}
	g := &group{t: t, id: id, created: time.Now()}
	t.groups[id] = g
	if t.newest == nil || t.newest.id < id {
		if t.newest != nil {
			t.newest.newest.Store(false)
		}
		t.newest = g
		g.newest.Store(true)
	}
	t.r.lruAdd(g)
	for len(t.groups) > t.r.opts.LiveEdgeGroups {
		var oldest *group
		for _, c := range t.groups {
			if c != g && c != t.newest && (oldest == nil || c.id < oldest.id) {
				oldest = c
			}
		}
		if oldest == nil {
			break
		}
		t.evictLocked(oldest, true)
	}
	return g
}

func (t *track) evict(g *group, spill bool) {
	t.mu.Lock()
	t.evictLocked(g, spill)
	t.mu.Unlock()
	t.r.tracks.dropIdle(t)
}

func (t *track) evictLocked(g *group, spill bool) {
	if t.groups[g.id] != g {
		return
	}
	delete(t.groups, g.id)
	if t.newest == g {
		t.newest = nil
		for _, c := range t.groups {
			if t.newest == nil || c.id > t.newest.id {
				t.newest = c
			}
		}
		if t.newest != nil {
			t.newest.newest.Store(true)
		}
	}
	g.evicted.Store(true)
	t.r.lruRemove(g)
	t.r.addBytes(-g.bytes.Load())
	if spill && t.r.opts.Spill && !t.malformed {
		if g.openRuns() {
			g.spillPending = true
		} else if j, ok := t.spillJobLocked(g); ok {
			t.r.queueSpill(j)
		}
	}
}

type admission uint8

const (
	admitted admission = iota
	duplicate
	refused
)

func (t *track) admitLocked(g *group, o *object) admission {
	// the caller holds t.mu
	if t.malformed {
		return refused
	}
	if dup := g.find(o.id); dup != nil {
		if !dup.sameAs(o) {
			return refused
		}
		return duplicate
	}
	if (g.hasEnd && o.id > g.end) || (t.endOfTrack != nil && t.endOfTrack.Less(o.loc())) {
		return refused
	}
	switch o.status {
	case message.StatusEndOfGroup:
		if g.hasEnd && g.end != o.id {
			return refused
		}
		g.hasEnd, g.end = true, o.id
	case message.StatusEndOfTrack:
		l := o.loc()
		t.endOfTrack = &l
		g.hasEnd, g.end = true, o.id
	}
	if o.endOfGroup {
		g.hasEnd, g.end = true, o.id
	}
	t.raiseLargest(o.loc())
	if !g.evicted.Load() {
		n := o.size()
		g.bytes.Add(n)
		t.r.addBytes(n)
	}
	return admitted
}

func (t *track) openRun(u *upstreamSub, h message.SubgroupHeader) *run {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.up != u || t.malformed {
		return nil
	}
	g := t.groupLocked(h.Group)
	rn := newRun(g, h)
	g.runs = append(g.runs, rn)
	for _, s := range t.subs {
		s.startRun(rn, 0)
	}
	return rn
}

func (t *track) runEnded(rn *run, st runState) {
	t.mu.Lock()
	g := rn.g
	if st == runFinished && rn.header.EndOfGroup {
		if objs := rn.objects(); len(objs) > 0 && !g.hasEnd {
			g.hasEnd, g.end = true, objs[len(objs)-1].id
		}
	}
	rn.end(st)
	var job *spillJob
	if g.evicted.Load() && g.spillPending && !g.openRuns() {
		g.spillPending = false
		if j, ok := t.spillJobLocked(g); ok {
			job = &j
		}
	}
	t.mu.Unlock()
	if job != nil {
		t.r.queueSpill(*job)
	}
}

func (t *track) ingestSubgroup(u *upstreamSub, in *session.IncomingSubgroup) {
	// one upstream subgroup stream is read into the live edge, forwarded as it arrives
	first, err := in.Next()
	if err != nil {
		return
	}
	rn := t.openRun(u, *in.Header())
	if rn == nil {
		in.Cancel(message.ResetCancelled)
		return
	}
	h := *in.Header()
	for o := first; ; {
		if !t.ingestObject(rn, h, o, in) {
			t.runEnded(rn, runReset)
			return
		}
		if o, err = in.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				t.runEnded(rn, runFinished)
			} else {
				t.runEnded(rn, runReset)
			}
			return
		}
	}
}

func (t *track) ingestObject(rn *run, h message.SubgroupHeader, so message.SubgroupObject,
	in *session.IncomingSubgroup,
) bool {
	if so.PayloadLen > t.r.maxObject {
		t.r.m.drop(dropOversize)
		in.Cancel(message.ResetExcessiveLoad)
		return false
	}
	o := &object{
		group: h.Group, id: so.ID, subgroup: h.Subgroup, priority: h.Priority, defaultPrio: h.DefaultPriority,
		status: so.Status, props: so.Properties, payload: make([]byte, so.PayloadLen), arrived: time.Now(), run: rn,
	}
	t.mu.Lock()
	adm := t.admitLocked(rn.g, o)
	var dup *object
	if adm == duplicate {
		dup = rn.g.find(o.id)
	}
	t.mu.Unlock()
	switch adm {
	case refused:
		in.Cancel(message.ResetMalformedTrack)
		t.markMalformed()
		return false
	case duplicate:
		// a duplicate is read only to confirm its payload matches the copy already held
		if _, err := io.ReadFull(in, o.payload); err != nil {
			return false
		}
		o.filled.Store(int64(len(o.payload)))
		if !dup.sameAs(o) {
			in.Cancel(message.ResetMalformedTrack)
			t.markMalformed()
			return false
		}
		return true
	}
	rn.append(o)
	t.r.m.object(directionIn, deliveryStream, o.size())
	var filled int
	for filled < len(o.payload) {
		n, err := in.Read(o.payload[filled:])
		filled += n
		o.filled.Store(int64(filled))
		rn.sig.notify()
		if err != nil {
			o.failed.Store(true)
			rn.sig.notify()
			return false
		}
	}
	return true
}

func (t *track) ingestDatagram(u *upstreamSub, d *message.Datagram) {
	o := &object{
		group: d.Group, id: d.Object, priority: d.Priority, defaultPrio: d.DefaultPriority, datagram: true,
		endOfGroup: d.EndOfGroup, props: bytes.Clone(d.Properties), payload: bytes.Clone(d.Payload),
		arrived: time.Now(),
	}
	if d.HasStatus {
		o.status = d.Status
	}
	o.filled.Store(int64(len(o.payload)))
	t.mu.Lock()
	if t.up != u {
		t.mu.Unlock()
		return
	}
	g := t.groupLocked(d.Group)
	adm := t.admitLocked(g, o)
	var subs []*subscriber
	props := t.props
	if adm == admitted {
		g.datagrams = append(g.datagrams, o)
		subs = t.subs
	}
	t.mu.Unlock()
	switch adm {
	case refused:
		t.markMalformed()
		return
	case duplicate:
		return
	}
	t.r.m.object(directionIn, deliveryDatagram, o.size())
	defaultPrio := props.PublisherPriority()
	for _, s := range subs {
		s.sendDatagram(o, props, defaultPrio)
	}
}

func (t *track) status(ctx context.Context) (*wire.Location, message.Properties, error) {
	// the track's Largest Object and properties, from upstream with TRACK_STATUS when the relay has not seen it
	t.mu.Lock()
	if t.malformed {
		t.mu.Unlock()
		return nil, nil, errMalformed
	}
	if t.upState == upEstablished || t.hasLargest {
		l, p := t.largestLocked(), t.props
		t.mu.Unlock()
		return l, p, nil
	}
	t.mu.Unlock()
	if t.r.up == nil {
		return nil, nil, errNoUpstream
	}
	sess, err := t.r.up.Session(ctx, t.scope)
	if err != nil {
		return nil, nil, errUpstream
	}
	req, err := sess.Open(ctx, &message.TrackStatus{Track: t.name}, nopRequestHandler{}, nil)
	if err != nil {
		return nil, nil, errUpstream
	}
	resp, err := req.Response(ctx)
	if err != nil {
		return nil, nil, errUpstream
	}
	switch v := resp.(type) {
	case *message.RequestOK:
		props := bytes.Clone(v.TrackProperties)
		t.mu.Lock()
		if v.Params.Has(message.ParamLargestObject) {
			t.raiseLargest(v.Params.LargestObject)
		}
		if t.props == nil {
			t.props = props
		}
		l := t.largestLocked()
		t.mu.Unlock()
		return l, props, nil
	case *message.RequestError:
		return nil, nil, &requestError{code: v.Code, reason: v.Reason}
	}
	return nil, nil, errUpstream
}

type nopRequestHandler struct{}

func (nopRequestHandler) HandleMessage(*session.Request, message.Message) {}
func (nopRequestHandler) HandleClose(*session.Request, error)             {}

type upstreamSub struct {
	t        *track
	mu       sync.Mutex
	req      *session.Request
	done     *publishDone
	up       bool
	closed   bool
	closeErr error
}

func (u *upstreamSub) request() *session.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.req
}

func (u *upstreamSub) open(forward bool) {
	t := u.t
	if t.r.up == nil {
		t.upstreamEnded(u, errNoUpstream)
		return
	}
	ctx := t.r.ctx
	sess, err := t.r.up.Session(ctx, t.scope)
	if err != nil {
		t.upstreamEnded(u, errUpstream)
		return
	}
	var p message.Params
	p.SetForward(forward)
	req, err := sess.Open(ctx, &message.Subscribe{Track: t.name, Params: p}, u, u)
	if err != nil {
		t.upstreamEnded(u, errUpstream)
		return
	}
	u.mu.Lock()
	u.req = req
	u.mu.Unlock()
	resp, err := req.Response(ctx)
	if err != nil {
		t.upstreamEnded(u, err)
		return
	}
	switch v := resp.(type) {
	case *message.SubscribeOK:
		if !t.established(u, v, forward) {
			req.Cancel(message.ResetCancelled)
			return
		}
		u.mu.Lock()
		u.up = true
		closed, cause := u.closed, u.causeLocked()
		u.mu.Unlock()
		if closed {
			t.upstreamEnded(u, cause)
		}
	case *message.RequestError:
		t.upstreamEnded(u, &requestError{code: v.Code, reason: v.Reason})
	}
}

func (u *upstreamSub) cancel() {
	if req := u.request(); req != nil {
		req.Cancel(message.ResetCancelled)
		return
	}
	u.t.upstreamEnded(u, context.Canceled)
}

func (u *upstreamSub) HandleMessage(_ *session.Request, m message.Message) {
	switch v := m.(type) {
	case *message.PublishDone:
		u.mu.Lock()
		u.done = &publishDone{code: v.Code, reason: v.Reason}
		u.mu.Unlock()
	case *message.RequestOK, *message.RequestError:
		// an answer to a FORWARD update frees room for the next one
		u.t.acked()
		go u.t.syncForward()
	}
}

func (u *upstreamSub) HandleClose(_ *session.Request, err error) {
	u.mu.Lock()
	u.closed, u.closeErr = true, err
	up, cause := u.up, u.causeLocked()
	u.mu.Unlock()
	// until the subscription is established, the opening path owns the outcome
	if up {
		u.t.upstreamEnded(u, cause)
	}
}

func (u *upstreamSub) causeLocked() error {
	switch {
	case u.done != nil:
		return u.done
	case u.closeErr != nil:
		return u.closeErr
	}
	return &publishDone{code: message.DoneInternalError, reason: "upstream subscription closed"}
}

func (u *upstreamSub) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	u.t.ingestSubgroup(u, in)
}

func (u *upstreamSub) HandleDatagram(_ *session.Request, d *message.Datagram) {
	u.t.ingestDatagram(u, d)
}

func (u *upstreamSub) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	in.Cancel(message.ResetCancelled)
}

func (t *track) endsBy(l wire.Location) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.endOfTrack != nil && !l.Less(*t.endOfTrack)
}
