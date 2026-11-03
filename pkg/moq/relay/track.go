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
	"math"
	"slices"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
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
	errMalformed         = errors.New("moq: track is malformed")
	errNoUpstream        = &requestError{code: message.RequestDoesNotExist, reason: "no publisher for the track"}
	errUpstream          = &requestError{code: message.RequestInternalError, reason: "upstream unavailable"}
	errTooManyPublishers = &requestError{code: message.RequestExcessiveLoad, reason: "too many publishers"}
	errRendezvous        = &requestError{code: message.RequestTimeout, reason: "no publisher arrived"}
	errStaleLargest      = &requestError{code: message.RequestTimeout, reason: "the track's largest object is unknown"}
)

type requestError struct {
	code   message.RequestCode
	reason string
	// props are the Mandatory Track Properties an UNSUPPORTED_EXTENSION refusal names
	props []uint64
}

func unsupported(p message.Properties) *requestError {
	return &requestError{code: message.RequestUnsupportedExtension, reason: reasonMandatory, props: p.MandatoryTypes()}
}

func upstreamRefusal(v *message.RequestError) *requestError {
	return &requestError{code: v.Code, reason: v.Reason, props: v.UnsupportedProperties}
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

	ups        []*upstreamSub
	upState    upState
	upReady    chan struct{}
	upErr      error
	fwdAck     chan struct{}
	fwdPending int
	linger     *time.Timer
	retries    int
	retrying   bool
	lost       error
	arrived    chan struct{}
	awaiting   map[*session.Session]int
	newGroup   newGroupState
	fromOrigin bool
	refresh    *statusRefresh
	probes     int

	ctrlMu sync.Mutex
}

func newTrack(r *Relay, scope Scope, name message.FullTrackName, key string, hash uint64) *track {
	return &track{r: r, scope: scope, name: name, key: key, hash: hash, groups: make(map[uint64]*group)}
}

func (t *track) idle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.subs) == 0 && len(t.ups) == 0 && t.upState == upIdle && len(t.groups) == 0 && t.linger == nil
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
	return t.probes > 0 || t.held > 0 || t.keepForwardLocked()
}

func (t *track) keepForwardLocked() bool {
	// wanted by a registered subscription, not only by a probe or a request still pending
	return t.forwarders > 0 || t.r.opts.Prewarm
}

func (t *track) hold(s *subscriber) {
	// a subscriber awaiting the upstream subscription counts as forwarding, so the upstream never pauses for
	// the moment between its establishment and the subscriber's registration
	t.mu.Lock()
	if s.forward.Load() {
		t.holdLocked(s)
	}
	t.mu.Unlock()
}

func (t *track) holdLocked(s *subscriber) {
	if !s.held {
		s.held = true
		t.held++
	}
}

func (t *track) unholdLocked(s *subscriber) {
	if s.held {
		s.held = false
		t.held--
	}
}

func retryable(cause error) bool {
	if re, ok := errors.AsType[*requestError](cause); ok {
		return re.code == message.RequestInternalError || re.code == message.RequestTimeout ||
			re.code == message.RequestGoingAway || re.code == message.RequestExcessiveLoad
	}
	if pd, ok := errors.AsType[*publishDone](cause); ok {
		switch pd.code {
		case message.DoneGoingAway, message.DoneTooFarBehind, message.DoneExpired, message.DoneInternalError,
			message.DoneExcessiveLoad, message.DoneUpdateFailed:
			// the relay answers its subscribers' updates itself, so a failed update upstream was its own, which a
			// fresh subscription recovers from
			return true
		}
		return false
	}
	return cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, errMalformed)
}

func doneFor(cause error) (message.DoneCode, string) {
	if pd, ok := errors.AsType[*publishDone](cause); ok {
		if pd.code == message.DoneUpdateFailed {
			// the update that failed was the relay's own, not the subscriber's
			return message.DoneInternalError, "upstream subscription lost"
		}
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

func malformedProps(sess *session.Session, p message.Properties, track bool) bool {
	// a Mandatory Track Property on an object is malformed in every draft; -23 adds the scope and repetition rules,
	// whose single walk also covers the mandatory check
	if len(p) == 0 {
		return false
	}
	if sess != nil && sess.Codec().Has(codec.CapPropertyRules) {
		return p.CheckScope(track) != nil
	}
	return !track && p.CheckObject() != nil
}

func refusal(err error) *message.RequestError {
	// the REQUEST_ERROR answering a request err fails, naming the mandatory properties behind UNSUPPORTED_EXTENSION
	code, reason := refusalFor(err)
	out := &message.RequestError{Code: code, Reason: reason}
	if re, ok := errors.AsType[*requestError](err); ok && code == message.RequestUnsupportedExtension {
		out.UnsupportedProperties = re.props
	}
	return out
}

func reject(req *session.Request, re *message.RequestError) {
	if req.Send(re) == nil {
		_ = req.CloseSend()
	}
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
	if len(t.subs) == 0 && t.hasPullLocked() && t.linger == nil && !t.closed {
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

func (t *track) startForwardingLocked(s *subscriber) {
	// a resumed subscription forwards from each open run's next object, its hold now its forwarding
	if !s.forward.Load() && slices.Contains(t.subs, s) {
		t.forwarders++
	}
	s.forward.Store(true)
	t.unholdLocked(s)
	t.startOpenRunsLocked(s)
}

func (t *track) resume(ctx context.Context) bool {
	// a subscriber answered while the upstream is still paused, or about to pause, would miss what is published
	// before the upstream forwards again, so every FORWARD update in flight is answered first; false if none is left
	t.syncForward()
	t.mu.Lock()
	ack := t.fwdAck
	t.mu.Unlock()
	if ack == nil {
		return true
	}
	select {
	case <-ack:
	case <-ctx.Done():
	case <-time.After(finishWait):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.upState == upEstablished
}

func (t *track) markMalformed() {
	t.mu.Lock()
	if t.malformed {
		t.mu.Unlock()
		return
	}
	t.malformed = true
	ups := slices.Clone(t.ups)
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
	for _, u := range ups {
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
	if !t.sourceLocked(u) || t.malformed {
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

func (t *track) ingestSubgroup(u *upstreamSub, sess *session.Session, in *session.IncomingSubgroup) {
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
		if malformedProps(sess, o.Properties, false) {
			in.Cancel(message.ResetMalformedTrack)
			t.markMalformed()
			t.runEnded(rn, runReset)
			return
		}
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

func (t *track) ingestDatagram(u *upstreamSub, sess *session.Session, d *message.Datagram) {
	if malformedProps(sess, d.Properties, false) {
		t.markMalformed()
		return
	}
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
	if !t.sourceLocked(u) {
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

func (t *track) status(ctx context.Context, strict bool) (*wire.Location, message.Properties, error) {
	// the track's Largest Object and properties, from upstream with TRACK_STATUS when the relay has not seen it, or
	// when no source is current; with strict a value that could not be refreshed is refused rather than used
	t.mu.Lock()
	if t.malformed {
		t.mu.Unlock()
		return nil, nil, errMalformed
	}
	switch {
	case t.upState == upEstablished && !t.forwardingLocked():
		t.mu.Unlock()
		l, err := t.currentLargest(ctx, strict)
		if err != nil {
			return nil, nil, err
		}
		t.mu.Lock()
		p := t.props
		t.mu.Unlock()
		return l, p, nil
	case t.upState == upEstablished || t.hasLargest:
		l, p := t.largestLocked(), t.props
		t.mu.Unlock()
		return l, p, nil
	}
	t.mu.Unlock()
	if err := t.r.negs.refusal(t); err != nil && len(t.r.publishersFor(t.scope, t.name.Namespace)) == 0 {
		return nil, nil, err
	}
	return t.statusUpstream(ctx)
}

func (t *track) statusUpstream(ctx context.Context) (*wire.Location, message.Properties, error) {
	sess, release, origin, err := t.upstreamSession(ctx)
	if err != nil {
		return nil, nil, err
	}
	req, err := sess.Open(ctx, &message.TrackStatus{Track: t.name}, nopRequestHandler{}, nil)
	if err != nil {
		release(err)
		return nil, nil, errUpstream
	}
	resp, err := req.Response(ctx)
	release(err)
	if err != nil {
		return nil, nil, errUpstream
	}
	switch v := resp.(type) {
	case *message.RequestOK:
		if _, mandatory := v.TrackProperties.Mandatory(); mandatory {
			// the track is neither processed nor forwarded, as for a SUBSCRIBE_OK naming such a property
			return nil, nil, unsupported(v.TrackProperties)
		}
		if malformedProps(sess, v.TrackProperties, true) {
			return nil, nil, errMalformed
		}
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
		err = upstreamRefusal(v)
		if origin {
			t.r.negs.record(t, err)
		}
		return nil, nil, err
	}
	return nil, nil, errUpstream
}

func (t *track) forwardingLocked() bool {
	// a source is current once its upstream acknowledged that it forwards, while its filter delivers everything
	// past the relay's Largest Object: one starting further on misses what lies between, and one with an end stops
	for _, u := range t.ups {
		if u.live && u.fwd && u.fwdKnown && !u.bounded && t.coversLocked(u.from) &&
			!slices.Contains(u.updates, updateForward) {
			return true
		}
	}
	return false
}

func (t *track) coversLocked(from wire.Location) bool {
	if from == (wire.Location{}) {
		return true
	}
	if !t.hasLargest {
		return false
	}
	next := wire.Location{Group: t.largest.Group, Object: t.largest.Object + 1}
	if t.largest.Object == math.MaxUint64 {
		next = wire.Location{Group: t.largest.Group + 1}
	}
	return !next.Less(from)
}

func sourceFilter(p *message.Params, largest *wire.Location) (wire.Location, bool) {
	// where a source's Location Filter starts, relative filters resolved against the publisher's Largest Object
	// (else the relay's), and whether it ends
	if !p.Has(message.ParamLocationFilter) {
		return wire.Location{}, false
	}
	if p.Has(message.ParamLargestObject) {
		largest = &p.LargestObject
	}
	from, _, unbounded, ok := p.LocationFilter.Bounds(largest, false)
	return from, !ok || !unbounded
}

type statusRefresh struct {
	// done closes when a non-current track's Largest Object is refreshed, err holding why it could not be, for
	// every caller waiting on it
	done chan struct{}
	err  error
}

func fatalRefresh(err error) bool {
	// a track the relay may not process, whose refusal every caller passes on
	re, ok := errors.AsType[*requestError](err)
	return errors.Is(err, errMalformed) || (ok && re.code == message.RequestUnsupportedExtension)
}

func (t *track) freshLargest(ctx context.Context) (*wire.Location, error) {
	// with no current source the relay's Largest Object may be stale, so it is refreshed once for every caller;
	// on failure the caller gets the relay's own value and why it could not be refreshed
	t.mu.Lock()
	if t.upState != upEstablished || t.malformed || t.forwardingLocked() {
		defer t.mu.Unlock()
		return t.largestLocked(), nil
	}
	c := t.refresh
	if c == nil {
		c = &statusRefresh{done: make(chan struct{})}
		t.refresh = c
		go t.refreshStatus(c)
	}
	t.mu.Unlock()
	var err error
	select {
	case <-c.done:
		err = c.err
	case <-ctx.Done():
		err = ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.largestLocked(), err
}

func (t *track) refreshStatus(c *statusRefresh) {
	ctx, cancel := context.WithTimeout(t.r.ctx, statusRefreshWait)
	_, _, err := t.statusUpstream(ctx)
	cancel()
	t.mu.Lock()
	t.refresh, c.err = nil, err
	t.mu.Unlock()
	close(c.done)
}

func (t *track) currentLargest(ctx context.Context, strict bool) (*wire.Location, error) {
	// a refresh that fails fatally always fails the caller; otherwise with strict an upstream that cannot answer
	// TRACK_STATUS is unpaused for its answer's Largest Object instead, and without strict the relay's own value stands
	l, err := t.freshLargest(ctx)
	switch {
	case err == nil || fatalRefresh(err):
		return l, err
	case strict:
		return t.probe(ctx)
	}
	return l, nil
}

func (t *track) probe(ctx context.Context) (*wire.Location, error) {
	// the sources forward until their answer to that arrives, then pause again unless a subscriber wants them
	ctx, cancel := context.WithTimeout(ctx, statusRefreshWait)
	defer cancel()
	t.mu.Lock()
	t.probes++
	t.mu.Unlock()
	t.resume(ctx)
	t.mu.Lock()
	t.probes--
	current, l := t.forwardingLocked(), t.largestLocked()
	t.mu.Unlock()
	go t.syncForward()
	if !current {
		return nil, errStaleLargest
	}
	return l, nil
}

type nopRequestHandler struct{}

func (nopRequestHandler) HandleMessage(*session.Request, message.Message) {}
func (nopRequestHandler) HandleClose(*session.Request, error)             {}

type upstreamSub struct {
	t      *track
	target *session.Session
	pushed bool

	live        bool
	fwd         bool
	fwdKnown    bool
	provisional bool
	bounded     bool
	from        wire.Location
	updates     []updateKind
	sentNG      uint64

	updMu sync.Mutex
	renew renewer

	mu       sync.Mutex
	req      *session.Request
	done     *publishDone
	up       bool
	closed   bool
	closeErr error

	release   func(error)
	released  sync.Once
	canceling bool
}

func (u *upstreamSub) request() *session.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.req
}

func (u *upstreamSub) session() *session.Session {
	// a pushed source is a publisher's PUBLISH; a pulled one subscribes to target, or to the origin when it is nil.
	// the fields from live through updates are guarded by the track's lock
	if u.pushed {
		if req := u.request(); req != nil {
			return req.Session()
		}
		return nil
	}
	return u.target
}

func (u *upstreamSub) open(forward bool) {
	t := u.t
	ctx := t.r.ctx
	sess := u.target
	if sess == nil {
		if t.r.up == nil {
			t.upstreamEnded(u, errNoUpstream)
			return
		}
		var err error
		var release func(error)
		if sess, release, err = t.r.up.Session(ctx, t.scope, t.name); err != nil {
			t.upstreamEnded(u, errUpstream)
			return
		}
		u.mu.Lock()
		u.release = release
		u.mu.Unlock()
	}
	var p message.Params
	p.SetForward(forward)
	t.mu.Lock()
	if ng := &t.newGroup; ng.wanted {
		// §9.20.19: a NEW_GROUP_REQUEST that arrived without an Established subscription goes upstream with it
		p.SetNewGroupRequest(ng.want)
	}
	u.sentNG = t.newGroup.seq
	t.mu.Unlock()
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
		if _, mandatory := v.TrackProperties.Mandatory(); mandatory {
			// §3.7: the track is neither processed nor forwarded, and its subscribers are refused
			req.Cancel(message.ResetCancelled)
			t.upstreamEnded(u, unsupported(v.TrackProperties))
			return
		}
		if malformedProps(sess, v.TrackProperties, true) {
			req.Cancel(message.ResetMalformedTrack)
			t.upstreamEnded(u, errMalformed)
			return
		}
		if !t.established(u, v, forward) {
			req.Cancel(message.ResetCancelled)
			u.settle(context.Canceled)
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
		err := upstreamRefusal(v)
		if u.target == nil {
			t.r.negs.record(t, err)
		}
		t.upstreamEnded(u, err)
	}
}

func (u *upstreamSub) settle(cause error) {
	// the origin answers only for what a retry elsewhere might cure, never for a track that ended or was refused
	u.released.Do(func() {
		u.mu.Lock()
		release, canceling := u.release, u.canceling
		u.mu.Unlock()
		if release == nil {
			return
		}
		switch {
		case canceling || errors.Is(cause, context.Canceled):
			release(context.Canceled)
		case retryable(cause):
			release(cause)
		default:
			release(nil)
		}
	})
}

func (u *upstreamSub) cancel() {
	u.mu.Lock()
	u.canceling = true
	u.mu.Unlock()
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
	case *message.PublishStateNotify:
		// informative: the publisher's largest object and whether it has paused (§9.10)
		t := u.t
		t.mu.Lock()
		if v.Params.Has(message.ParamLargestObject) {
			t.raiseLargest(v.Params.LargestObject)
		}
		if v.Params.Has(message.ParamForward) && u.live {
			u.fwd, u.fwdKnown, u.provisional = v.Params.Forward, true, false
		}
		if v.Params.Has(message.ParamLocationFilter) {
			u.from, u.bounded = sourceFilter(&v.Params, t.largestLocked())
		}
		t.mu.Unlock()
	case *message.RequestOK:
		// answers come in the order the updates were sent; one carrying EXPIRES schedules the next renewal, and
		// LARGEST_OBJECT is in place before a resume waiting on the answer goes on
		if v.Params.Has(message.ParamLargestObject) {
			u.t.mu.Lock()
			u.t.raiseLargest(v.Params.LargestObject)
			u.t.mu.Unlock()
		}
		if u.t.answered(u, false) == updateForward {
			go u.t.syncForward()
		}
		u.renew.schedule(&v.Params, u.extend)
	case *message.RequestError:
		// a refused update fails the ones outstanding with it
		u.t.answered(u, true)
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

func (u *upstreamSub) HandleSubgroup(r *session.Request, in *session.IncomingSubgroup) {
	u.t.ingestSubgroup(u, r.Session(), in)
}

func (u *upstreamSub) HandleDatagram(r *session.Request, d *message.Datagram) {
	u.t.ingestDatagram(u, r.Session(), d)
}

func (u *upstreamSub) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	in.Cancel(message.ResetCancelled)
}

func (t *track) endsBy(l wire.Location) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.endOfTrack != nil && !l.Less(*t.endOfTrack)
}
