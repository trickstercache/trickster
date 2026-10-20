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

package moq

import (
	"slices"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	reasonNotServed    = "requests are not served"
	reasonUninterested = "no subscriber wants the track"
)

type originSink interface {
	OriginNamespace(ns wire.Namespace) (withdraw func())
	OriginPublish(req *session.Request, m *message.Publish) bool
}

type originRequests struct {
	mu        sync.Mutex
	sinks     []originSink
	announced []*originAnnounce
}

type originAnnounce struct {
	prefix    wire.Namespace
	withdraws map[originSink]func()
	done      bool
}

func (o *originRequests) watch(s originSink) (unwatch func()) {
	// every relay using the dialer's sessions learns what the origin publishes on them, including what it
	// published before the relay joined
	o.mu.Lock()
	defer o.mu.Unlock()
	if slices.Contains(o.sinks, s) {
		return func() {}
	}
	o.sinks = append(o.sinks, s)
	for _, a := range o.announced {
		a.withdraws[s] = s.OriginNamespace(a.prefix)
	}
	var once sync.Once
	return func() { once.Do(func() { o.unwatch(s) }) }
}

func (o *originRequests) unwatch(s originSink) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i := slices.Index(o.sinks, s); i >= 0 {
		o.sinks = slices.Delete(o.sinks, i, i+1)
	}
	for _, a := range o.announced {
		if w := a.withdraws[s]; w != nil {
			delete(a.withdraws, s)
			w()
		}
	}
}

func (d *dialer) HandleRequest(r *session.Request) {
	// the origin may publish namespaces and tracks on a session the relay dialed (§7.6); it asks for nothing
	// that is served
	switch m := r.Message().(type) {
	case *message.PublishNamespace:
		d.origin.publishNamespace(r, m)
	case *message.Publish:
		d.origin.publish(r, m)
	default:
		_ = r.Reject(message.RequestNotSupported, reasonNotServed, 0)
	}
}

func (d *dialer) HandleGoAway(*session.Session, *message.GoAway) {}

func (o *originRequests) publishNamespace(r *session.Request, m *message.PublishNamespace) {
	a := &originAnnounce{prefix: m.Prefix.Clone(), withdraws: make(map[originSink]func())}
	r.SetHandlers(&originNamespace{o: o, a: a}, nil)
	if r.Send(&message.RequestOK{}) != nil {
		return
	}
	o.mu.Lock()
	if r.Context().Err() == nil {
		o.announced = append(o.announced, a)
		for _, s := range o.sinks {
			a.withdraws[s] = s.OriginNamespace(a.prefix)
		}
	}
	o.mu.Unlock()
}

func (o *originRequests) withdraw(a *originAnnounce) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	if i := slices.Index(o.announced, a); i >= 0 {
		o.announced = slices.Delete(o.announced, i, i+1)
	}
	for s, w := range a.withdraws {
		delete(a.withdraws, s)
		w()
	}
}

func (o *originRequests) publish(r *session.Request, m *message.Publish) {
	// a PUBLISH answers a relay's SUBSCRIBE_TRACKS; the first relay whose subscription covers the track takes it
	o.mu.Lock()
	sinks := slices.Clone(o.sinks)
	o.mu.Unlock()
	for _, s := range sinks {
		if s.OriginPublish(r, m) {
			return
		}
	}
	_ = r.Reject(message.RequestUninterested, reasonUninterested, 0)
}

type originNamespace struct {
	o *originRequests
	a *originAnnounce
}

func (h *originNamespace) HandleMessage(r *session.Request, m message.Message) {
	// a REQUEST_UPDATE refreshes the request's authorization, which the relay accepts as given
	if _, ok := m.(*message.RequestUpdate); ok {
		_ = r.Send(&message.RequestOK{})
	}
}

func (h *originNamespace) HandleClose(*session.Request, error) {
	h.o.withdraw(h.a)
}
