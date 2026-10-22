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

package relay_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

func negativeHarness(t *testing.T, ttl time.Duration) *harness {
	t.Helper()
	h := newHarness(t, relay.Options{}, nil)
	h.relay.SetNegativeTTL(ttl)
	return h
}

func subscribeRefused(t *testing.T, c *session.Session, name string, code message.RequestCode) {
	t.Helper()
	req, _ := subscribe(t, c, name, message.Params{})
	refusedWith(t, req, code)
}

func statusRefused(t *testing.T, c *session.Session, name string, code message.RequestCode) {
	t.Helper()
	req, err := c.Open(context.Background(), &message.TrackStatus{Track: track(name)}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, req, code)
}

func TestNegativeSubscribe(t *testing.T) {
	h := negativeHarness(t, time.Minute)
	h.o.refuse["ghost"] = message.RequestDoesNotExist
	cl := h.client()
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	// the origin's refusal answers every later request for the track until it expires
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	statusRefused(t, cl, "ghost", message.RequestDoesNotExist)
	freq, _ := fetch(t, cl, "ghost", fetchRange(0, 0, 1, 0))
	refusedWith(t, freq, message.RequestDoesNotExist)
	if n, s := h.o.subscribes.Load(), h.o.statuses.Load(); n != 1 || s != 0 {
		t.Errorf("the origin was asked %d SUBSCRIBEs and %d TRACK_STATUSes, want 1 and 0", n, s)
	}
}

func TestNegativeTrackStatus(t *testing.T) {
	// the origin answers TRACK_STATUS for a track it never published with DOES_NOT_EXIST
	h := negativeHarness(t, time.Minute)
	h.o.refuse["phantom"] = message.RequestDoesNotExist
	cl := h.client()
	statusRefused(t, cl, "phantom", message.RequestDoesNotExist)
	statusRefused(t, cl, "phantom", message.RequestDoesNotExist)
	subscribeRefused(t, cl, "phantom", message.RequestDoesNotExist)
	if n, s := h.o.subscribes.Load(), h.o.statuses.Load(); n != 0 || s != 1 {
		t.Errorf("the origin was asked %d SUBSCRIBEs and %d TRACK_STATUSes, want 0 and 1", n, s)
	}
}

func TestNegativeExpires(t *testing.T) {
	h := negativeHarness(t, 20*time.Millisecond)
	h.o.refuse["ghost"] = message.RequestDoesNotExist
	cl := h.client()
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	eventually(t, "the refusal to expire", func() bool {
		subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
		return h.o.subscribes.Load() >= 2
	})
}

func TestNegativeOnlyNotFound(t *testing.T) {
	cases := map[string]struct {
		ttl  time.Duration
		code message.RequestCode
	}{
		// the default negative cache has no 404 entry, so nothing is kept
		"no ttl": {0, message.RequestDoesNotExist},
		// a refusal that depends on the requester is asked again
		"unauthorized": {time.Minute, message.RequestUnauthorized},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := negativeHarness(t, tc.ttl)
			h.o.refuse["ghost"] = tc.code
			cl := h.client()
			subscribeRefused(t, cl, "ghost", tc.code)
			subscribeRefused(t, cl, "ghost", tc.code)
			if n := h.o.subscribes.Load(); n != 2 {
				t.Errorf("the origin was asked %d times, want 2", n)
			}
		})
	}
}

func TestNegativeForgottenWhenPublished(t *testing.T) {
	h := negativeHarness(t, time.Minute)
	h.relay.SetAccess(relay.Access{Publish: true})
	h.o.refuse["ghost"] = message.RequestDoesNotExist
	cl := h.client()
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	// a publisher's PUBLISH of the track means it exists now, so the next subscriber is served
	pubSess := h.session(nil)
	published(t, pubSess, "ghost")
	req, _ := subscribe(t, cl, "ghost", message.Params{})
	subscribed(t, req)
	req.Cancel(message.ResetCancelled)
	// once the publisher leaves, the origin is asked again rather than the old refusal repeated
	_ = pubSess.Close(message.CodeNoError, "")
	eventually(t, "the origin to be asked again", func() bool {
		subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
		return h.o.subscribes.Load() >= 2
	})
}

func TestNegativeForgottenWhenAnnounced(t *testing.T) {
	h := negativeHarness(t, time.Minute)
	h.o.refuse["ghost"] = message.RequestDoesNotExist
	cl := h.client()
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	// a namespace arriving may hold the track, so the origin is asked again
	withdraw := h.relay.OriginNamespace(ns("live"))
	defer withdraw()
	h.o.mu.Lock()
	delete(h.o.refuse, "ghost")
	h.o.mu.Unlock()
	req, _ := subscribe(t, cl, "ghost", message.Params{})
	subscribed(t, req)
	if n := h.o.subscribes.Load(); n != 2 {
		t.Errorf("the origin was asked %d times, want 2", n)
	}
}

func TestNegativeTTLCleared(t *testing.T) {
	h := negativeHarness(t, time.Minute)
	h.o.refuse["ghost"] = message.RequestDoesNotExist
	cl := h.client()
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	// a reload that removes the 404 entry forgets what was kept
	h.relay.SetNegativeTTL(0)
	h.relay.SetNegativeTTL(time.Minute)
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	if n := h.o.subscribes.Load(); n != 2 {
		t.Errorf("the origin was asked %d times, want 2", n)
	}
}

type absentPublisher struct {
	asks atomic.Int32
}

func (p *absentPublisher) HandleGoAway(*session.Session, *message.GoAway) {}

func (p *absentPublisher) HandleRequest(r *session.Request) {
	r.SetHandlers(newRec(), nil)
	p.asks.Add(1)
	_ = r.Reject(message.RequestDoesNotExist, "not here", 0)
}

func TestNegativeOriginOnly(t *testing.T) {
	// a namespace publisher's refusal is its own, and it may publish the track at any time, so it is asked again
	h := negativeHarness(t, time.Minute)
	h.relay.SetAccess(relay.Access{Publish: true})
	p := &absentPublisher{}
	pubSess := h.session(p)
	ann, err := pubSess.Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	cl := h.client()
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	subscribeRefused(t, cl, "ghost", message.RequestDoesNotExist)
	if n := p.asks.Load(); n != 2 {
		t.Errorf("the namespace publisher was asked %d times, want 2", n)
	}
	if n := h.o.subscribes.Load(); n != 0 {
		t.Errorf("the origin was asked %d times", n)
	}
	// once the publisher leaves, the origin serves the track, its refusal never having been kept
	_ = pubSess.Close(message.CodeNoError, "")
	eventually(t, "the origin to serve the track", func() bool {
		req, _ := subscribe(t, cl, "ghost", message.Params{})
		_, ok := response(t, req).(*message.SubscribeOK)
		return ok
	})
}
