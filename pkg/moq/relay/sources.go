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
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

func (t *track) ensureUpstream(ctx context.Context, forward bool) error {
	// a source is a SUBSCRIBE the relay opened toward a namespace publisher or the origin, or a publisher's PUBLISH;
	// this returns once one is established, subscribing upstream if needed
	for attempt := 0; ; attempt++ {
		err := t.awaitUpstream(ctx, forward)
		if err != nil || !forward || t.resume(ctx) {
			return err
		}
		// the source ended while its resume was in flight, as one that refuses the update does (§9.5.1)
		if attempt > 0 {
			return errUpstream
		}
	}
}

func (t *track) awaitUpstream(ctx context.Context, forward bool) error {
	t.mu.Lock()
	if t.malformed {
		t.mu.Unlock()
		return errMalformed
	}
	switch t.upState {
	case upEstablished:
		t.mu.Unlock()
		return nil
	case upIdle:
		if err := t.startPullLocked(forward || t.wantForwardLocked()); err != nil {
			t.mu.Unlock()
			return err
		}
	}
	ready := t.upReady
	t.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	t.mu.Lock()
	state, err := t.upState, t.upErr
	t.mu.Unlock()
	if state == upEstablished {
		return nil
	}
	if err == nil {
		return errUpstream
	}
	return err
}

func (t *track) startPullLocked(forward bool) error {
	// one subscription per session that published the track's namespace (§7.6), else one to the origin
	targets := t.r.publishersFor(t.scope, t.name.Namespace)
	if len(targets) == 0 {
		if t.r.up == nil {
			return errNoUpstream
		}
		targets = []*session.Session{nil}
	}
	t.pendingLocked()
	for _, sess := range targets {
		u := &upstreamSub{t: t, target: sess}
		t.ups = append(slices.Clip(t.ups), u)
		go u.open(forward)
	}
	return nil
}

func (t *track) addPullLocked(sess *session.Session) {
	// a publisher of the track's namespace arrived while the track is wanted, so it is subscribed to as well
	for _, u := range t.ups {
		if u.target == sess || (u.pushed && u.session() == sess) {
			return
		}
	}
	u := &upstreamSub{t: t, target: sess}
	t.ups = append(slices.Clip(t.ups), u)
	if t.upState == upIdle {
		t.pendingLocked()
	}
	go u.open(t.wantForwardLocked())
}

func (t *track) pendingLocked() {
	if t.upState != upPending {
		t.upState, t.upErr = upPending, nil
		t.upReady = make(chan struct{})
	}
}

func (t *track) readyLocked(state upState, err error) {
	t.upState, t.upErr = state, err
	if t.upReady != nil {
		close(t.upReady)
		t.upReady = nil
	}
}

func (t *track) sourceLocked(u *upstreamSub) bool {
	return slices.Contains(t.ups, u)
}

func (t *track) liveSourceLocked() bool {
	for _, u := range t.ups {
		if u.live {
			return true
		}
	}
	return false
}

func (t *track) hasPullLocked() bool {
	for _, u := range t.ups {
		if !u.pushed {
			return true
		}
	}
	return false
}

func (t *track) publisherCountLocked() int {
	var n int
	for _, u := range t.ups {
		if u.pushed {
			n++
		}
	}
	return n
}

func (t *track) publishedBy(sess *session.Session) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, u := range t.ups {
		if u.pushed && u.session() == sess {
			return true
		}
	}
	return false
}

func (t *track) hasPublisher() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.publisherCountLocked() > 0
}

func (t *track) wantedLocked() bool {
	return len(t.subs) > 0 || t.held > 0
}

func (t *track) establishLocked() {
	if t.upState != upEstablished {
		t.retries, t.retrying = 0, false
		t.readyLocked(upEstablished, nil)
	}
}

func (t *track) established(u *upstreamSub, ok *message.SubscribeOK, forward bool) bool {
	t.mu.Lock()
	if !t.sourceLocked(u) || u.live {
		t.mu.Unlock()
		return false
	}
	u.live, u.fwd = true, forward
	t.establishLocked()
	if len(ok.TrackProperties) > 0 || t.props == nil {
		t.props = bytes.Clone(ok.TrackProperties)
	}
	if ok.Params.Has(message.ParamLargestObject) {
		t.raiseLargest(ok.Params.LargestObject)
	}
	late := t.newGroupEstablishedLocked(u)
	t.mu.Unlock()
	u.renew.schedule(&ok.Params, u.extend)
	t.r.m.subscriptions(directionUpstream, 1)
	go t.syncForward()
	if late != nil {
		go t.sendNewGroup(late)
	}
	return true
}

func (t *track) addPublisher(u *upstreamSub, m *message.Publish) error {
	// a PUBLISH is a source established from its first message, carrying the publisher's initial forward state
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.malformed:
		return errMalformed
	case t.closed:
		return errUpstream
	}
	if limit := t.r.access().MaxPublishersPerTrack; limit > 0 && t.publisherCountLocked() >= limit {
		return errTooManyPublishers
	}
	u.live, u.fwd = true, m.Params.ForwardOr(true)
	t.ups = append(slices.Clip(t.ups), u)
	t.establishLocked()
	if t.props == nil {
		t.props = bytes.Clone(m.TrackProperties)
		if t.props == nil {
			t.props = message.Properties{}
		}
	}
	if m.Params.Has(message.ParamLargestObject) {
		t.raiseLargest(m.Params.LargestObject)
	}
	t.signalArrivalLocked()
	u.renew.schedule(&m.Params, u.extend)
	return nil
}

func (t *track) upstreamEnded(u *upstreamSub, cause error) {
	// a source, or an attempt to open one, ended
	u.settle(cause)
	u.renew.stop()
	t.mu.Lock()
	i := slices.Index(t.ups, u)
	if i < 0 {
		t.mu.Unlock()
		return
	}
	t.ups = slices.Delete(slices.Clone(t.ups), i, i+1)
	wasLive := u.live
	u.live = false
	t.forgetUpdatesLocked(u)
	if t.malformed {
		cause = errMalformed
	}
	var end []*subscriber
	switch {
	case t.liveSourceLocked() || t.retrying:
	case len(t.ups) > 0:
		// only sources still opening remain
		t.pendingLocked()
	default:
		end = t.lostSourcesLocked(cause)
	}
	t.mu.Unlock()
	if wasLive {
		if u.pushed {
			t.r.m.publishers(-1)
		} else {
			t.r.m.subscriptions(directionUpstream, -1)
		}
	}
	if u.pushed {
		t.r.tracks.release(t)
	}
	if len(end) > 0 {
		code, reason := doneFor(cause)
		for _, s := range end {
			go s.finish(code, reason)
		}
	}
	t.r.tracks.dropIdle(t)
}

func (t *track) lostSourcesLocked(cause error) []*subscriber {
	// the track has no source left: its subscribers stay while the relay finds one again, else they end
	if len(t.subs) > 0 && !t.closed && !t.malformed && retryable(cause) && t.retries < reconnectAttempts {
		t.scheduleRetryLocked(cause)
		return nil
	}
	t.readyLocked(upIdle, cause)
	if len(t.subs) == 0 {
		return nil
	}
	return slices.Clone(t.subs)
}

func (t *track) scheduleRetryLocked(cause error) {
	backoff := t.r.opts.ReconnectBackoff << t.retries
	t.retries++
	t.retrying, t.lost = true, cause
	t.pendingLocked()
	time.AfterFunc(backoff, t.retry)
}

func (t *track) retry() {
	t.mu.Lock()
	if !t.retrying {
		t.mu.Unlock()
		return
	}
	t.retrying = false
	if len(t.ups) > 0 || t.closed {
		// a publisher came back meanwhile, or the relay is closing
		t.mu.Unlock()
		return
	}
	var end []*subscriber
	cause := t.lost
	if len(t.subs) == 0 {
		t.readyLocked(upIdle, cause)
	} else if err := t.startPullLocked(t.wantForwardLocked()); err != nil {
		// nothing to subscribe to until a publisher returns, which a later attempt may find
		if t.retries < reconnectAttempts {
			t.scheduleRetryLocked(cause)
		} else {
			t.readyLocked(upIdle, cause)
			end = slices.Clone(t.subs)
		}
	}
	t.mu.Unlock()
	if len(end) > 0 {
		code, reason := doneFor(cause)
		for _, s := range end {
			go s.finish(code, reason)
		}
	}
	t.r.tracks.dropIdle(t)
}

func (t *track) lingerExpired() {
	// the relay's own subscriptions end once nobody wants the track; a publisher's keep the track supplied
	t.mu.Lock()
	t.linger = nil
	if len(t.subs) > 0 {
		t.mu.Unlock()
		return
	}
	var pulls []*upstreamSub
	for _, u := range t.ups {
		if !u.pushed {
			pulls = append(pulls, u)
		}
	}
	t.mu.Unlock()
	for _, u := range pulls {
		u.cancel()
	}
}

func (t *track) answered(u *upstreamSub, all bool) updateKind {
	// the oldest outstanding update was answered, or with all, every one of them failed
	t.mu.Lock()
	defer t.mu.Unlock()
	if all {
		t.forgetUpdatesLocked(u)
		return updateForward
	}
	if len(u.updates) == 0 {
		return updateNone
	}
	k := u.updates[0]
	u.updates = u.updates[1:]
	if k == updateForward {
		t.ackedLocked()
	}
	return k
}

func (t *track) forgetUpdatesLocked(u *upstreamSub) {
	// a source that ended will answer none of its outstanding updates
	for _, k := range u.updates {
		if k == updateForward {
			t.ackedLocked()
		}
	}
	u.updates = nil
}

func (t *track) ackedLocked() {
	if t.fwdPending == 0 {
		return
	}
	t.fwdPending--
	if t.fwdPending == 0 && t.fwdAck != nil {
		close(t.fwdAck)
		t.fwdAck = nil
	}
}

type forwardUpdate struct {
	u       *upstreamSub
	forward bool
}

func (t *track) syncForward() {
	// each source's FORWARD state follows the downstream subscribers' (§7.2), except that the relay resumes a
	// paused publisher it wants but never pauses one, so its cache keeps what is published
	t.ctrlMu.Lock()
	defer t.ctrlMu.Unlock()
	t.mu.Lock()
	want := t.wantForwardLocked()
	var todo []forwardUpdate
	for _, u := range t.ups {
		target := want || (u.pushed && u.fwd)
		if !u.live || target == u.fwd {
			continue
		}
		if u.request() != nil {
			todo = append(todo, forwardUpdate{u: u, forward: target})
			t.fwdPending++
		}
	}
	if len(todo) > 0 && t.fwdAck == nil {
		t.fwdAck = make(chan struct{})
	}
	t.mu.Unlock()
	for _, f := range todo {
		var p message.Params
		p.SetForward(f.forward)
		if err := f.u.sendUpdate(updateForward, p); err != nil {
			t.mu.Lock()
			t.ackedLocked()
			t.mu.Unlock()
			continue
		}
		t.mu.Lock()
		f.u.fwd = f.forward
		t.mu.Unlock()
	}
}

func (t *track) shutdown() {
	t.mu.Lock()
	t.closed, t.retrying = true, false
	ups := slices.Clone(t.ups)
	if t.linger != nil {
		t.linger.Stop()
		t.linger = nil
	}
	t.mu.Unlock()
	for _, u := range ups {
		u.cancel()
	}
}

func (t *track) arrivalLocked() <-chan struct{} {
	if t.arrived == nil {
		t.arrived = make(chan struct{})
	}
	return t.arrived
}

func (t *track) signalArrivalLocked() {
	// a subscriber awaiting a publisher (RENDEZVOUS_TIMEOUT) tries again
	if t.arrived != nil {
		close(t.arrived)
		t.arrived = nil
	}
}

func (t *track) publisherArrived(sess *session.Session) {
	// a session published a namespace the track falls under: a wanted track subscribes to it too (§7.6)
	t.mu.Lock()
	if !t.closed && !t.malformed && t.wantedLocked() && t.upState != upIdle {
		t.addPullLocked(sess)
	}
	t.signalArrivalLocked()
	t.mu.Unlock()
}

func (t *track) upstreamSession(ctx context.Context) (*session.Session, func(error), error) {
	// the session an upstream FETCH or TRACK_STATUS goes to: a source's, else a namespace publisher's, else an
	// origin's (§7.6); the release is called once the request ends
	t.mu.Lock()
	for _, u := range t.ups {
		if s := u.session(); u.live && s != nil {
			t.mu.Unlock()
			return s, noRelease, nil
		}
	}
	t.mu.Unlock()
	if targets := t.r.publishersFor(t.scope, t.name.Namespace); len(targets) > 0 {
		return targets[0], noRelease, nil
	}
	if t.r.up == nil {
		return nil, nil, errNoUpstream
	}
	sess, release, err := t.r.up.Session(ctx, t.scope, t.name)
	if err != nil {
		return nil, nil, errUpstream
	}
	if release == nil {
		release = noRelease
	}
	return sess, release, nil
}

func noRelease(error) {}

func (t *track) hasUpstream() bool {
	t.mu.Lock()
	live := t.liveSourceLocked()
	t.mu.Unlock()
	return live || t.r.up != nil || len(t.r.publishersFor(t.scope, t.name.Namespace)) > 0
}
