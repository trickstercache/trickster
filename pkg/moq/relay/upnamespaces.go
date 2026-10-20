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

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// nsUpstream is the relay's SUBSCRIBE_NAMESPACE toward the origin for one scope and prefix, shared by the
// downstream listeners of that prefix; the namespaces the origin reports join the registry beside publishers'
type nsUpstream struct {
	r      *Relay
	scope  Scope
	prefix wire.Namespace
	key    string
	stopCh chan struct{}

	// refs and linger are guarded by the registry's lock
	refs   int
	linger *time.Timer

	mu      sync.Mutex
	req     *session.Request
	entries map[string]*nsPublisher
	stopped bool
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

func upstreamKey(scope Scope, prefix wire.Namespace) string {
	return scope.Authority + "\x00" + scope.Path + "\x00" + namespaceKey(prefix)
}

func (r *Relay) acquireUpstreamLocked(scope Scope, prefix wire.Namespace) *nsUpstream {
	// a listener of a relay with an origin shares, or opens, the origin subscription for its prefix (§7.6)
	n := &r.ns
	key := upstreamKey(scope, prefix)
	u := n.upstreams[key]
	if u == nil {
		u = &nsUpstream{
			r: r, scope: scope, prefix: prefix, key: key, stopCh: make(chan struct{}),
			entries: make(map[string]*nsPublisher),
		}
		if n.upstreams == nil {
			n.upstreams = make(map[string]*nsUpstream)
		}
		n.upstreams[key] = u
		go u.run()
	}
	u.refs++
	if u.linger != nil {
		u.linger.Stop()
		u.linger = nil
	}
	return u
}

func (r *Relay) releaseUpstreamLocked(u *nsUpstream) {
	// the origin subscription lingers after its last listener leaves, so a returning one finds it warm
	u.refs--
	if u.refs == 0 && u.linger == nil {
		u.linger = time.AfterFunc(r.opts.Linger, func() { r.lingerExpiredNS(u) })
	}
}

func (r *Relay) lingerExpiredNS(u *nsUpstream) {
	n := &r.ns
	n.mu.Lock()
	if u.refs > 0 || n.upstreams[u.key] != u {
		n.mu.Unlock()
		return
	}
	delete(n.upstreams, u.key)
	u.linger = nil
	n.mu.Unlock()
	u.stop()
}

func (r *Relay) stopUpstreamNamespaces() {
	n := &r.ns
	n.mu.Lock()
	ups := make([]*nsUpstream, 0, len(n.upstreams))
	for _, u := range n.upstreams {
		if u.linger != nil {
			u.linger.Stop()
			u.linger = nil
		}
		ups = append(ups, u)
	}
	clear(n.upstreams)
	n.mu.Unlock()
	for _, u := range ups {
		u.stop()
	}
}

func (u *nsUpstream) stop() {
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		return
	}
	u.stopped = true
	close(u.stopCh)
	req := u.req
	u.mu.Unlock()
	if req != nil {
		req.Cancel(message.ResetCancelled)
	}
}

func (u *nsUpstream) run() {
	// a lost subscription withdraws what it reported (§4.2.2) and is opened again after a backoff; a refusal is
	// final, leaving the listeners with publishers' namespaces
	retries := 0
	for {
		established, err := u.attempt()
		u.withdrawAll()
		if established {
			retries = 0
		}
		if !retryable(err) || retries >= reconnectAttempts {
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

func (u *nsUpstream) attempt() (bool, error) {
	r := u.r
	sess, release, err := r.up.Session(r.ctx, u.scope, message.FullTrackName{Namespace: u.prefix})
	if err != nil {
		return false, errUpstream
	}
	if release == nil {
		release = noRelease
	}
	a := &nsAttempt{u: u, closed: make(chan error, 1)}
	// draft -17 carries the namespace-only form as an option; later drafts ignore the field
	m := &message.SubscribeNamespace{Prefix: u.prefix, Options: message.SubscribeOptionNamespace}
	req, err := sess.Open(r.ctx, m, a, nil)
	if err != nil {
		release(errUpstream)
		return false, errUpstream
	}
	u.mu.Lock()
	if u.stopped {
		u.mu.Unlock()
		req.Cancel(message.ResetCancelled)
		release(context.Canceled)
		return false, context.Canceled
	}
	u.req = req
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.req = nil
		u.mu.Unlock()
	}()
	resp, err := req.Response(r.ctx)
	if err != nil {
		release(settled(err))
		return false, err
	}
	if re, ok := resp.(*message.RequestError); ok {
		err = &requestError{code: re.Code, reason: re.Reason}
		release(settled(err))
		return false, err
	}
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

func (u *nsUpstream) announce(suffix wire.Namespace) {
	full := append(slices.Clip(u.prefix), suffix.Clone()...)
	key := namespaceKey(full)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped || u.entries[key] != nil {
		return
	}
	p := &nsPublisher{scope: u.scope, prefix: full, key: key}
	u.entries[key] = p
	u.r.addNamespacePublisher(p)
}

func (u *nsUpstream) withdraw(suffix wire.Namespace) {
	key := namespaceKey(append(slices.Clip(u.prefix), suffix...))
	u.mu.Lock()
	defer u.mu.Unlock()
	if p := u.entries[key]; p != nil {
		delete(u.entries, key)
		u.r.removeNamespacePublisher(p)
	}
}

func (u *nsUpstream) withdrawAll() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for key, p := range u.entries {
		delete(u.entries, key)
		u.r.removeNamespacePublisher(p)
	}
}

type nsAttempt struct {
	u      *nsUpstream
	closed chan error
}

func (a *nsAttempt) HandleMessage(_ *session.Request, m message.Message) {
	switch v := m.(type) {
	case *message.Namespace:
		a.u.announce(v.Suffix)
	case *message.NamespaceDone:
		a.u.withdraw(v.Suffix)
	}
}

func (a *nsAttempt) HandleClose(_ *session.Request, err error) {
	select {
	case a.closed <- err:
	default:
	}
}
