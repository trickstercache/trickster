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

type prefixKind uint8

const (
	prefixNamespaces prefixKind = iota
	prefixTracks
	prefixKinds
)

const handoverGrace = 2 * time.Second

type upPrefix struct {
	r      *Relay
	kind   prefixKind
	scope  Scope
	prefix wire.Namespace
	stopCh chan struct{}

	refs     int
	linger   *time.Timer
	into     *upPrefix
	handover []*upPrefix

	mu        sync.Mutex
	req       *session.Request
	sess      *session.Session
	entries   map[string]*nsPublisher
	renew     renewer
	stopped   bool
	handedOff bool
}

// OriginNamespace records a namespace the origin published on an upstream session (PUBLISH_NAMESPACE), which
// every scope's SUBSCRIBE_NAMESPACE reports until the returned withdraw is called
func (r *Relay) OriginNamespace(ns wire.Namespace) (withdraw func()) {
	prefix := ns.Clone()
	p := &nsPublisher{prefix: prefix, key: namespaceKey(prefix), anyScope: true}
	r.addNamespacePublisher(p)
	var once sync.Once
	return func() { once.Do(func() { r.removeNamespacePublisher(p) }) }
}

func (r *Relay) acquireUpstreamLocked(kind prefixKind, scope Scope, prefix wire.Namespace) *upPrefix {
	// origin reports are backend-wide, and prefixes on one session must not overlap (§9.20.20): a covered prefix
	// shares its cover, and a wider one replaces those it covers, keeping their reports until it makes its own
	n := &r.ns
	set := n.upstreams[kind]
	for _, u := range set {
		if prefix.HasPrefix(u.prefix) {
			u.refs++
			u.stopLingerLocked()
			return u
		}
	}
	w := &upPrefix{
		r: r, kind: kind, scope: scope, prefix: prefix, stopCh: make(chan struct{}),
		entries: make(map[string]*nsPublisher), refs: 1,
	}
	kept := make([]*upPrefix, 0, len(set)+1)
	for _, u := range set {
		if !u.prefix.HasPrefix(prefix) {
			kept = append(kept, u)
			continue
		}
		w.refs += u.refs
		u.into = w
		u.stopLingerLocked()
		w.handover = append(w.handover, u)
		w.handover = append(w.handover, u.handover...)
		u.handover = nil
	}
	n.upstreams[kind] = append(kept, w)
	go w.run()
	return w
}

func (u *upPrefix) stopLingerLocked() {
	if u.linger != nil {
		u.linger.Stop()
		u.linger = nil
	}
}

func (r *Relay) releaseUpstreamLocked(u *upPrefix) {
	// a subscription that was replaced passes its listeners on; the last one leaving starts the linger, so a
	// returning listener finds the subscription warm
	for u.into != nil {
		u = u.into
	}
	u.refs--
	if u.refs == 0 && u.linger == nil {
		u.linger = time.AfterFunc(r.opts.Linger, func() { r.lingerExpiredUp(u) })
	}
}

func (r *Relay) lingerExpiredUp(u *upPrefix) {
	n := &r.ns
	n.mu.Lock()
	i := slices.Index(n.upstreams[u.kind], u)
	if u.refs > 0 || i < 0 {
		n.mu.Unlock()
		return
	}
	n.upstreams[u.kind] = slices.Delete(n.upstreams[u.kind], i, i+1)
	u.linger = nil
	n.mu.Unlock()
	u.stop(false)
}

func (r *Relay) stopUpstreamPrefixes() {
	n := &r.ns
	n.mu.Lock()
	var ups []*upPrefix
	for k := range n.upstreams {
		for _, u := range n.upstreams[k] {
			u.stopLingerLocked()
			ups = append(ups, u)
		}
		n.upstreams[k] = nil
	}
	n.mu.Unlock()
	for _, u := range ups {
		u.stop(false)
	}
}

func (u *upPrefix) stop(handedOff bool) {
	// a subscription handed off to a wider one keeps its reports in the registry until the wider one removes them
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	u.stopped, u.handedOff = true, handedOff
	close(u.stopCh)
	req := u.req
	u.mu.Unlock()
	u.renew.stop()
	if req != nil {
		req.Cancel(message.ResetCancelled)
	}
}

func (u *upPrefix) finishHandover() {
	// the narrower subscriptions' reports are removed once the wider one has had the grace to report them again
	n := &u.r.ns
	n.mu.Lock()
	hs := u.handover
	u.handover = nil
	n.mu.Unlock()
	for _, h := range hs {
		h.withdrawAll()
	}
}

func (u *upPrefix) run() {
	// replaced requests are cancelled first, and a PREFIX_OVERLAP refusal racing that cancel is retried; a lost
	// subscription withdraws its reports (§4.2.2) and reopens after a backoff, and any other refusal is final
	n := &u.r.ns
	n.mu.Lock()
	hs := slices.Clone(u.handover)
	n.mu.Unlock()
	for _, h := range hs {
		h.stop(true)
	}
	defer u.finishHandover()
	retries := 0
	for {
		established, err := u.attempt()
		u.mu.Lock()
		handedOff := u.handedOff
		u.mu.Unlock()
		if !handedOff {
			u.withdrawAll()
		}
		if established {
			retries = 0
		}
		if (!retryable(err) && !overlapRefusal(err)) || retries >= reconnectAttempts {
			return
		}
		t := time.NewTimer(u.r.opts.ReconnectBackoff << retries)
		retries++
		select {
		case <-t.C:
		case <-u.stopCh:
			t.Stop()
			return
		case <-u.r.ctx.Done():
			t.Stop()
			return
		}
	}
}

func overlapRefusal(err error) bool {
	re, ok := errors.AsType[*requestError](err)
	return ok && re.code == message.RequestPrefixOverlap
}

func (u *upPrefix) request(c *codec.Codec) message.Message {
	// draft -17 asks for namespaces or PUBLISHes with an option of SUBSCRIBE_NAMESPACE; later drafts ignore the
	// field and ask for PUBLISHes with SUBSCRIBE_TRACKS
	if u.kind == prefixNamespaces {
		return &message.SubscribeNamespace{Prefix: u.prefix, Options: message.SubscribeOptionNamespace}
	}
	if c.Has(codec.CapSubscribeTracks) {
		return &message.SubscribeTracks{Prefix: u.prefix}
	}
	return &message.SubscribeNamespace{Prefix: u.prefix, Options: message.SubscribeOptionPublish}
}

func (u *upPrefix) attempt() (bool, error) {
	r := u.r
	sess, release, err := r.up.Session(r.ctx, u.scope, message.FullTrackName{Namespace: u.prefix})
	if err != nil {
		return false, errUpstream
	}
	if release == nil {
		release = noRelease
	}
	a := &upAttempt{u: u, closed: make(chan error, 1)}
	// the session is known before the request goes out, since the origin's PUBLISHes may lead its answer
	u.mu.Lock()
	u.sess = sess
	u.mu.Unlock()
	req, err := sess.Open(r.ctx, u.request(sess.Codec()), a, nil)
	if err != nil {
		u.mu.Lock()
		u.sess = nil
		u.mu.Unlock()
		release(errUpstream)
		return false, errUpstream
	}
	u.mu.Lock()
	if u.stopped {
		u.sess = nil
		u.mu.Unlock()
		req.Cancel(message.ResetCancelled)
		release(context.Canceled)
		return false, context.Canceled
	}
	u.req = req
	u.mu.Unlock()
	defer func() {
		u.renew.cancel()
		u.mu.Lock()
		u.req, u.sess = nil, nil
		u.mu.Unlock()
	}()
	resp, err := req.Response(r.ctx)
	if err != nil {
		release(settled(err))
		return false, err
	}
	switch v := resp.(type) {
	case *message.RequestError:
		err = &requestError{code: v.Code, reason: v.Reason}
		release(settled(err))
		return false, err
	case *message.RequestOK:
		u.renew.schedule(&v.Params, u.extend)
	}
	time.AfterFunc(handoverGrace, u.finishHandover)
	select {
	case err = <-a.closed:
		if err == nil {
			// the origin ended the subscription; the listeners still want it
			err = errUpstream
		}
	case <-u.stopCh:
		req.Cancel(message.ResetCancelled)
		err = context.Canceled
	case <-r.ctx.Done():
		req.Cancel(message.ResetCancelled)
		err = context.Canceled
	}
	release(settled(err))
	return true, err
}

func (u *upPrefix) extend() {
	// an empty REQUEST_UPDATE asks the origin to extend the subscription; its answer carries the next EXPIRES
	u.mu.Lock()
	req := u.req
	u.mu.Unlock()
	if req != nil {
		_, _ = req.Update(message.Params{})
	}
}

func (u *upPrefix) covers(sess *session.Session, ns wire.Namespace) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.sess == sess && ns.HasPrefix(u.prefix)
}

func settled(err error) error {
	// the origin answers only for what a retry elsewhere might cure
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case retryable(err):
		return err
	}
	return nil
}

func (u *upPrefix) announce(suffix wire.Namespace) {
	full := append(slices.Clip(u.prefix), suffix.Clone()...)
	key := namespaceKey(full)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped || u.entries[key] != nil {
		return
	}
	p := &nsPublisher{prefix: full, key: key, anyScope: true}
	u.entries[key] = p
	u.r.addNamespacePublisher(p)
}

func (u *upPrefix) withdraw(suffix wire.Namespace) {
	key := namespaceKey(append(slices.Clip(u.prefix), suffix...))
	u.mu.Lock()
	defer u.mu.Unlock()
	if p := u.entries[key]; p != nil {
		delete(u.entries, key)
		u.r.removeNamespacePublisher(p)
	}
}

func (u *upPrefix) withdrawAll() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for key, p := range u.entries {
		delete(u.entries, key)
		u.r.removeNamespacePublisher(p)
	}
}

type upAttempt struct {
	u      *upPrefix
	closed chan error
}

func (a *upAttempt) HandleMessage(_ *session.Request, m message.Message) {
	switch v := m.(type) {
	case *message.Namespace:
		a.u.announce(v.Suffix)
	case *message.NamespaceDone:
		a.u.withdraw(v.Suffix)
	case *message.RequestOK:
		// the answer to an extension
		a.u.renew.schedule(&v.Params, a.u.extend)
	}
}

func (a *upAttempt) HandleClose(_ *session.Request, err error) {
	select {
	case a.closed <- err:
	default:
	}
}
