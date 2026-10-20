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
	"slices"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type namespaces struct {
	mu        sync.Mutex
	pubs      []*nsPublisher
	listeners []*nsListener
	trackers  []*trackListener
	upstreams [prefixKinds][]*upPrefix
}

type nsPublisher struct {
	// a registration by the origin has no session, and covers every scope
	scope    Scope
	prefix   wire.Namespace
	key      string
	sess     *session.Session
	anyScope bool
}

func (p *nsPublisher) inScope(scope Scope) bool {
	return p.anyScope || p.scope == scope
}

type nsEvent struct {
	suffix wire.Namespace
	done   bool
}

type nsListener struct {
	scope  Scope
	prefix wire.Namespace
	req    *session.Request
	up     *upPrefix

	mu      sync.Mutex
	events  []nsEvent
	running bool
	closed  bool
}

func namespaceKey(ns wire.Namespace) string {
	return string(wire.AppendNamespace(nil, ns))
}

func trackNameKey(name message.FullTrackName) string {
	return string(wire.AppendBytes(wire.AppendNamespace(nil, name.Namespace), name.Name))
}

func (r *Relay) publishersFor(scope Scope, ns wire.Namespace) []*session.Session {
	// the sessions that published a prefix of ns, each once, newest first
	r.ns.mu.Lock()
	defer r.ns.mu.Unlock()
	var out []*session.Session
	for _, p := range slices.Backward(r.ns.pubs) {
		if p.sess != nil && p.scope == scope && ns.HasPrefix(p.prefix) && !slices.Contains(out, p.sess) {
			out = append(out, p.sess)
		}
	}
	return out
}

func (n *namespaces) knownLocked(scope Scope, key string) bool {
	for _, p := range n.pubs {
		if p.inScope(scope) && p.key == key {
			return true
		}
	}
	return false
}

func (r *Relay) addNamespacePublisher(p *nsPublisher) {
	// a namespace new to the scope is reported to every matching SUBSCRIBE_NAMESPACE (§4.2), and every wanted
	// track under it subscribes to its publisher (§7.6)
	n := &r.ns
	n.mu.Lock()
	for _, l := range n.listeners {
		if p.inScope(l.scope) && p.prefix.HasPrefix(l.prefix) && !n.knownLocked(l.scope, p.key) {
			l.push(nsEvent{suffix: p.prefix[len(l.prefix):]})
		}
	}
	n.pubs = append(n.pubs, p)
	n.mu.Unlock()
	if p.sess == nil {
		return
	}
	r.tracks.each(func(t *track) {
		if t.scope == p.scope && t.name.Namespace.HasPrefix(p.prefix) {
			t.publisherArrived(p.sess)
		}
	})
}

func (r *Relay) removeNamespacePublisher(p *nsPublisher) {
	// a namespace no other registration still covers is withdrawn from the listeners it was reported to
	n := &r.ns
	n.mu.Lock()
	defer n.mu.Unlock()
	i := slices.Index(n.pubs, p)
	if i < 0 {
		return
	}
	n.pubs = slices.Delete(n.pubs, i, i+1)
	for _, l := range n.listeners {
		if p.inScope(l.scope) && p.prefix.HasPrefix(l.prefix) && !n.knownLocked(l.scope, p.key) {
			l.push(nsEvent{suffix: p.prefix[len(l.prefix):], done: true})
		}
	}
}

func (r *Relay) addNamespaceListener(l *nsListener) {
	// the namespaces known now are reported first; the listener is registered in the same step, so it sees
	// every later change after them
	n := &r.ns
	n.mu.Lock()
	seen := make(map[string]struct{})
	for _, p := range n.pubs {
		if !p.inScope(l.scope) || !p.prefix.HasPrefix(l.prefix) {
			continue
		}
		if _, dup := seen[p.key]; dup {
			continue
		}
		seen[p.key] = struct{}{}
		l.push(nsEvent{suffix: p.prefix[len(l.prefix):]})
	}
	n.listeners = append(n.listeners, l)
	if r.up != nil {
		l.up = r.acquireUpstreamLocked(prefixNamespaces, l.scope, l.prefix)
	}
	n.mu.Unlock()
}

func (r *Relay) removeNamespaceListener(l *nsListener) {
	n := &r.ns
	n.mu.Lock()
	if i := slices.Index(n.listeners, l); i >= 0 {
		n.listeners = slices.Delete(n.listeners, i, i+1)
		if l.up != nil {
			r.releaseUpstreamLocked(l.up)
			l.up = nil
		}
	}
	n.mu.Unlock()
	l.mu.Lock()
	l.closed, l.events = true, nil
	l.mu.Unlock()
}

func (l *nsListener) push(e nsEvent) {
	// events are sent in the order the registry changed, by one goroutine per listener, so a NAMESPACE_DONE
	// never precedes its NAMESPACE and a slow stream never holds the registry's lock
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.events = append(l.events, e)
	if !l.running {
		l.running = true
		go l.run()
	}
}

func (l *nsListener) run() {
	for {
		l.mu.Lock()
		if len(l.events) == 0 || l.closed {
			l.running = false
			l.mu.Unlock()
			return
		}
		e := l.events[0]
		l.events = l.events[1:]
		l.mu.Unlock()
		var m message.Message = &message.Namespace{Suffix: e.suffix}
		if e.done {
			m = &message.NamespaceDone{Suffix: e.suffix}
		}
		if l.req.Send(m) != nil {
			// a stream that cannot take an update is reset, which its subscriber treats as every namespace
			// withdrawn (§4.2.2)
			l.req.Cancel(message.ResetCancelled)
			return
		}
	}
}

type trackListener struct {
	r      *Relay
	scope  Scope
	prefix wire.Namespace
	req    *session.Request
	params message.Params
	up     *upPrefix

	mu      sync.Mutex
	offered map[string]struct{}
	closed  bool
}

func (r *Relay) addTrackListener(l *trackListener) {
	n := &r.ns
	n.mu.Lock()
	n.trackers = append(n.trackers, l)
	if r.up != nil {
		l.up = r.acquireUpstreamLocked(prefixTracks, l.scope, l.prefix)
	}
	n.mu.Unlock()
	r.tracks.each(func(t *track) {
		if !t.name.Namespace.HasPrefix(l.prefix) {
			return
		}
		if (t.scope == l.scope && t.hasPublisher()) || t.originPublished() {
			l.offer(t)
		}
	})
}

func (r *Relay) removeTrackListener(l *trackListener) {
	n := &r.ns
	n.mu.Lock()
	if i := slices.Index(n.trackers, l); i >= 0 {
		n.trackers = slices.Delete(n.trackers, i, i+1)
		if l.up != nil {
			r.releaseUpstreamLocked(l.up)
			l.up = nil
		}
	}
	n.mu.Unlock()
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
}

func (r *Relay) offerTrack(t *track) {
	// a track a publisher just started is offered to every SUBSCRIBE_TRACKS whose prefix it falls under (§7.6)
	n := &r.ns
	n.mu.Lock()
	var ls []*trackListener
	// a track the origin published is backend-wide, offered to every scope; another scope's listener is served
	// a track pulled from the origin
	origin := t.originPublished()
	for _, l := range n.trackers {
		if (origin || l.scope == t.scope) && t.name.Namespace.HasPrefix(l.prefix) {
			ls = append(ls, l)
		}
	}
	n.mu.Unlock()
	for _, l := range ls {
		l.offer(t)
	}
}

func (l *trackListener) offer(t *track) {
	// one PUBLISH per track per SUBSCRIBE_TRACKS, never for a track the subscriber publishes itself, nor for one
	// it already awaits a publisher of with a SUBSCRIBE (§3.6, §7.6)
	sess := l.req.Session()
	if t.publishedBy(sess) || t.awaitedBy(sess) {
		return
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	key := trackNameKey(t.name)
	if _, done := l.offered[key]; done {
		l.mu.Unlock()
		return
	}
	l.offered[key] = struct{}{}
	l.mu.Unlock()
	go l.r.publishTo(l, t.name)
}

func (l *trackListener) skipped(name message.FullTrackName) {
	_ = l.req.Send(&message.PublishSkipped{Suffix: name.Namespace[len(l.prefix):], TrackName: name.Name})
}
