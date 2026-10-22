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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func originTracks(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, relay.Options{}, nil)
	h.o.trackAsks = make(chan *session.Request, 4)
	return h
}

func watchTracks(t *testing.T, h *harness, prefix wire.Namespace) *tracksSink {
	t.Helper()
	sink := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	st, err := h.session(sink).Open(context.Background(), &message.SubscribeTracks{Prefix: prefix}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	return sink
}

func originPublish(t *testing.T, up *session.Request, name string) (*session.Request, message.Message) {
	t.Helper()
	sess := up.Session()
	req, err := sess.Open(context.Background(), &message.Publish{Track: track(name), TrackAlias: sess.NewTrackAlias()},
		newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return req, response(t, req)
}

func TestTracksFromOrigin(t *testing.T) {
	h := originTracks(t)
	sink := watchTracks(t, h, ns("live"))
	up := recv(t, h.o.trackAsks, "the relay's SUBSCRIBE_TRACKS")
	if m, ok := up.Message().(*message.SubscribeTracks); !ok || !m.Prefix.Equal(ns("live")) {
		t.Fatalf("upstream asked for %+v", up.Message())
	}
	// the origin's PUBLISH becomes the track's source, offered downstream with a relay PUBLISH (§7.6)
	pub, answer := originPublish(t, up, "video")
	if _, ok := answer.(*message.RequestOK); !ok {
		t.Fatalf("the origin's PUBLISH was answered %+v", answer)
	}
	offered := recv(t, sink.pubs, "the relay's PUBLISH")
	if m := offered.Message().(*message.Publish); string(m.Track.Name) != "video" {
		t.Fatalf("offered %+v", m.Track)
	}
	sendGroup(t, pub, 0, objs("from", "origin")...)
	eventually(t, "the origin's objects downstream", func() bool { return sink.rec.count() == 2 })

	// a track nobody downstream covers is refused
	_, answer = originPublish(t, up, "other")
	if _, ok := answer.(*message.RequestOK); !ok {
		t.Fatalf("a covered track was refused: %+v", answer)
	}
	elsewhere := track("x")
	elsewhere.Namespace = ns("vod")
	req, err := up.Session().Open(context.Background(), &message.Publish{Track: elsewhere,
		TrackAlias: up.Session().NewTrackAlias()}, newRec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := response(t, req).(*message.RequestError); !ok || e.Code != message.RequestUninterested {
		t.Errorf("an uncovered track was answered %+v", e)
	}
}

func TestOriginTracksInAnotherScope(t *testing.T) {
	h := originTracks(t)
	sink := watchTracks(t, h, ns("live"))
	up := recv(t, h.o.trackAsks, "the relay's SUBSCRIBE_TRACKS")
	pub, _ := originPublish(t, up, "video")
	recv(t, sink.pubs, "the relay's PUBLISH")
	// a listener in another scope shares the upstream request and is offered the track, pulled from the origin
	h.scope = relay.Scope{Authority: "other.test", Path: "/"}
	before := h.o.subscribes.Load()
	other := watchTracks(t, h, ns("live"))
	recv(t, other.pubs, "the relay's PUBLISH in the other scope")
	eventually(t, "a SUBSCRIBE for the other scope", func() bool { return h.o.subscribes.Load() == before+1 })
	h.o.publish(t, "video", 3, objs("pulled")...)
	eventually(t, "the pulled object", func() bool { return other.rec.count() == 1 })
	sendGroup(t, pub, 0, objs("pushed")...)
	eventually(t, "the pushed object", func() bool { return sink.rec.count() == 1 })
}

func TestOriginTracksDraft17(t *testing.T) {
	h := originTracks(t)
	h.up.draft = 17
	watchTracks(t, h, ns("live"))
	up := recv(t, h.o.trackAsks, "the relay's request for PUBLISHes")
	if m, ok := up.Message().(*message.SubscribeNamespace); !ok || m.Options != message.SubscribeOptionPublish {
		t.Fatalf("draft -17 asked with %+v", up.Message())
	}
}

func TestOriginTrackPullRefused(t *testing.T) {
	h := originTracks(t)
	sink := watchTracks(t, h, ns("live"))
	up := recv(t, h.o.trackAsks, "the relay's SUBSCRIBE_TRACKS")
	originPublish(t, up, "video")
	recv(t, sink.pubs, "the relay's PUBLISH")
	// another scope's offer needs a pull, which the origin refuses: the track is reported skipped (§3.6.3)
	h.o.mu.Lock()
	h.o.refuse["video"] = message.RequestDoesNotExist
	h.o.mu.Unlock()
	h.scope = relay.Scope{Authority: "other.test", Path: "/"}
	other := &tracksSink{pubs: make(chan *session.Request, 4), rec: newRec()}
	skipped := newRec()
	st, err := h.session(other).Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, skipped, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := response(t, st).(*message.RequestOK); !ok {
		t.Fatal("SUBSCRIBE_TRACKS refused")
	}
	if m, ok := recv(t, skipped.messages, "PUBLISH_SKIPPED").(*message.PublishSkipped); !ok ||
		string(m.TrackName) != "video" {
		t.Fatalf("got %+v", m)
	}
}
