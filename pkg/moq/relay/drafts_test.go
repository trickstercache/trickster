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
	"fmt"
	"slices"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
)

func (h *harness) clientDraft(draft int) *session.Session {
	h.t.Helper()
	// both sides of the session speak draft
	return h.clientDraftWith(draft, nil)
}

func (h *harness) clientDraftWith(draft int, handler session.Handler) *session.Session {
	h.t.Helper()
	c, ok := codec.ForDraft(draft)
	if !ok {
		h.t.Fatalf("draft %d", draft)
	}
	cc, sc := memconn.Pair()
	server, err := session.New(sc, session.Options{Role: session.RoleServer, Codec: c, Handler: h.relay.Handler(h.scope)})
	if err != nil {
		h.t.Fatal(err)
	}
	client, err := session.New(cc, session.Options{
		Role: session.RoleClient, Codec: c, Handler: handler,
		Setup: message.Setup{Path: h.scope.Path, HasPath: true, Authority: h.scope.Authority, HasAuthority: true},
	})
	if err != nil {
		h.t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err := recv(h.t, errs, "session start"); err != nil {
			h.t.Fatal(err)
		}
	}
	h.t.Cleanup(func() {
		_ = client.Close(message.CodeNoError, "")
		_ = server.Close(message.CodeNoError, "")
	})
	return client
}

func draftHarness(t *testing.T, upstreamDraft int) *harness {
	t.Helper()
	o := newOrigin()
	h := &harness{t: t, o: o, up: &upstream{o: o, draft: upstreamDraft}, scope: relay.Scope{Authority: "relay.test", Path: "/"}}
	c, _ := newMemoryCache()
	h.relay = relay.New(relay.Options{}, h.up, cacheProvider{c})
	t.Cleanup(func() {
		h.relay.Close()
		h.up.close()
	})
	return h
}

func TestCrossDraftMatrix(t *testing.T) {
	// the relay bridges every downstream draft to every upstream draft: subscriptions, FETCH from the live edge and
	// from upstream, Joining FETCH for the drafts that have it, and the track's end
	for _, down := range codec.Drafts() {
		for _, up := range codec.Drafts() {
			t.Run(fmt.Sprintf("down%d-up%d", down, up), func(t *testing.T) {
				h := draftHarness(t, up)
				h.o.store("video", 5, objs("old-a", "old-b")...)
				cl := h.clientDraft(down)
				req, r := subscribe(t, cl, "video", message.Params{})
				subscribed(t, req)
				h.o.publish(t, "video", 6, objs("key", "delta")...)
				eventually(t, "the live objects", func() bool { return r.count() == 2 })
				if got := payloads(r.received()); !slices.Equal(got, []string{"key", "delta"}) {
					t.Fatalf("objects %v", got)
				}
				// the run starts at Object 0, so FIRST_OBJECT reaches every draft with the bit, whether or not the
				// upstream's draft could state it
				dc, _ := codec.ForDraft(down)
				r.mu.Lock()
				first := r.headers[0].FirstObject
				r.mu.Unlock()
				if first != dc.Has(codec.CapFirstObjectBit) {
					t.Errorf("FIRST_OBJECT %t downstream", first)
				}
				// group 6 is on the live edge; group 5 the relay fetches upstream in the upstream's draft
				freq, fr := fetch(t, cl, "video", fetchRange(5, 0, 6, 1))
				if _, got := fetched(t, freq, fr); !slices.Equal(got, []string{"old-a", "old-b", "key", "delta"}) {
					t.Errorf("fetched %v", got)
				}
				if dc.Has(codec.CapJoiningFetch) {
					// SUBSCRIBE_OK carried Largest Object {5, 1}, so a relative join of one group covers group 4 to it
					jr := newRec()
					jreq, err := cl.Open(context.Background(), &message.Fetch{
						Joining: &message.JoiningFetch{RequestID: req.RequestID(), Start: 0, Relative: true},
					}, jr, jr)
					if err != nil {
						t.Fatal(err)
					}
					if _, got := fetched(t, jreq, jr); !slices.Equal(got, []string{"old-a", "old-b"}) {
						t.Errorf("joining fetch %v", got)
					}
				}
				h.o.finish("video", message.DoneTrackEnded)
				for {
					m := recv(t, r.messages, "PUBLISH_DONE")
					if pd, ok := m.(*message.PublishDone); ok {
						if pd.Code != message.DoneTrackEnded {
							t.Errorf("PUBLISH_DONE %v", pd.Code)
						}
						break
					}
				}
			})
		}
	}
}

func TestJoiningFetchRefusals(t *testing.T) {
	h := draftHarness(t, 19)
	cl := h.clientDraft(19)
	join := func(id uint64) *session.Request {
		t.Helper()
		r, err := cl.Open(context.Background(), &message.Fetch{
			Joining: &message.JoiningFetch{RequestID: id, Start: 1, Relative: true},
		}, newRec(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	refusedWith(t, join(98), message.RequestInvalidJoiningID)
	// a subscription to a track with nothing published has no joining location
	req, _ := subscribe(t, cl, "empty", message.Params{})
	subscribed(t, req)
	refusedWith(t, join(req.RequestID()), message.RequestInvalidRange)
	// nor does a paused one
	h.o.store("video", 0, objs("x")...)
	var paused message.Params
	paused.SetForward(false)
	preq, _ := subscribe(t, cl, "video", paused)
	subscribed(t, preq)
	refusedWith(t, join(preq.RequestID()), message.RequestInvalidRange)
	var absolute message.Params
	r, err := cl.Open(context.Background(), &message.Fetch{
		Joining: &message.JoiningFetch{RequestID: preq.RequestID(), Start: 9},
		Params:  absolute,
	}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	refusedWith(t, r, message.RequestInvalidRange)
}

func TestSubscribeNamespaceOptions17(t *testing.T) {
	h := ingestHarness(t, relay.Access{Publish: true}, relay.Options{})
	published(t, h.client(), "video")
	open := func(cl *session.Session, options uint64) *rec {
		t.Helper()
		r := newRec()
		req, err := cl.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live"), Options: options}, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := response(t, req).(*message.RequestOK); !ok {
			t.Fatal("SUBSCRIBE_NAMESPACE refused")
		}
		return r
	}
	// the PUBLISH form behaves as SUBSCRIBE_TRACKS: a published track is offered with a PUBLISH, answered PUBLISH_OK
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	open(h.clientDraftWith(17, sink), message.SubscribeOptionPublish)
	if pm, ok := recv(t, sink.pubs, "the relay's PUBLISH").Message().(*message.Publish); !ok ||
		string(pm.Track.Name) != "video" {
		t.Fatalf("offered %+v", pm)
	}
	// the NAMESPACE form behaves as SUBSCRIBE_NAMESPACE from -18
	watched := open(h.clientDraft(17), message.SubscribeOptionNamespace)
	ann, err := h.client().Open(context.Background(), &message.PublishNamespace{Prefix: ns("live", "a")}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, ann).(*message.RequestOK); !ok {
		t.Fatal("PUBLISH_NAMESPACE refused")
	}
	if n, ok := recv(t, watched.messages, "NAMESPACE").(*message.Namespace); !ok || !n.Suffix.Equal(ns("a")) {
		t.Fatalf("got %+v", n)
	}
	// and both forms together do both
	bothSink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	both := open(h.clientDraftWith(17, bothSink), message.SubscribeOptionBoth)
	recv(t, bothSink.pubs, "the relay's PUBLISH")
	if n, ok := recv(t, both.messages, "NAMESPACE").(*message.Namespace); !ok || !n.Suffix.Equal(ns("a")) {
		t.Fatalf("got %+v", n)
	}
}
