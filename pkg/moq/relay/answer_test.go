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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
)

type offerPeer struct {
	msgs   chan message.Message
	closes chan error
}

type openStream struct{ session.Stream }

func (openStream) Close() error { return nil }

type keepOpen struct{ *memconn.Conn }

func (c keepOpen) OpenStreamSync(ctx context.Context) (session.Stream, error) {
	// the viewer's requests never end their sending half, as a peer that ignores the relay's FIN does
	st, err := c.Conn.OpenStreamSync(ctx)
	return openStream{st}, err
}

func (p *offerPeer) HandleGoAway(*session.Session, *message.GoAway) {}

func (p *offerPeer) HandleRequest(r *session.Request) {
	r.SetHandlers(p, nil)
	_ = r.Send(&message.RequestOK{})
}

func (p *offerPeer) HandleMessage(r *session.Request, m message.Message) {
	p.msgs <- m
	if _, done := m.(*message.PublishDone); done {
		_ = r.CloseSend()
	}
}

func (p *offerPeer) HandleClose(_ *session.Request, err error) { p.closes <- err }

type subscribeSink chan *session.Request

func (subscribeSink) HandleGoAway(*session.Session, *message.GoAway) {}
func (k subscribeSink) HandleRequest(r *session.Request)             { k <- r }

func offerSession(t *testing.T, relayHandler session.Handler, holdOpen bool,
) (*session.Session, *offerPeer, *session.Session) {
	t.Helper()
	c, _ := codec.ForDraft(23)
	peer := &offerPeer{msgs: make(chan message.Message, 8), closes: make(chan error, 2)}
	cc, mc := memconn.Pair()
	var sc session.Conn = mc
	if holdOpen {
		sc = keepOpen{mc}
	}
	relaySide, err := session.New(cc, session.Options{Role: session.RoleClient, Codec: c, Handler: relayHandler})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := session.New(sc, session.Options{Role: session.RoleServer, Codec: c, Handler: peer})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- relaySide.Start(context.Background()) }()
	go func() { errs <- viewer.Start(context.Background()) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = relaySide.Close(message.CodeNoError, "")
		_ = viewer.Close(message.CodeNoError, "")
	})
	return relaySide, peer, viewer
}

func TestOfferEndedWhileAnswered(t *testing.T) {
	// an offer the relay ends while its PUBLISH is sent, before or after the request is bound, gets exactly one
	// PUBLISH_DONE from the path sending the PUBLISH, and leaves no writer or track reference behind
	name := message.FullTrackName{Name: []byte("video")}
	for _, afterBind := range []bool{false, true} {
		r := New(Options{}, nil, nil)
		t.Cleanup(r.Close)
		tr := r.tracks.acquire(r, Scope{}, name)
		s := newSubscriber(tr, nil, &message.Params{})
		if _, _, code, _ := s.register(); code != 0 || !s.answer() {
			t.Fatalf("registered with %#x", code)
		}
		// a writer waits for the PUBLISH, as one for an object arriving meanwhile does
		s.startRun(newRun(&group{t: tr}, message.SubgroupHeader{}), 0)
		sess, peer, _ := offerSession(t, nil, false)
		req, err := sess.Open(context.Background(), &message.Publish{Track: name, TrackAlias: sess.NewTrackAlias()}, s, nil)
		if err != nil {
			t.Fatal(err)
		}
		// the viewer has answered, so its session expects PUBLISH_DONE before the stream's end
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		if _, err := req.Response(ctx); err != nil {
			t.Fatal(err)
		}
		if afterBind {
			s.bind(req)
		}
		s.finish(message.DoneMalformedTrack, "malformed track")
		if !afterBind {
			s.bind(req)
		}
		s.activatePublished()
		if done, ok := recv(t, peer.msgs, "PUBLISH_DONE").(*message.PublishDone); !ok ||
			done.Code != message.DoneMalformedTrack {
			t.Fatalf("after bind %v: got %+v", afterBind, done)
		}
		if err := recv(t, peer.closes, "the request's close"); err != nil {
			t.Errorf("after bind %v: closed with %v", afterBind, err)
		}
		select {
		case m := <-peer.msgs:
			t.Errorf("after bind %v: then %+v", afterBind, m)
		default:
		}
		if !waitFor(&s.wg, time.Second) {
			t.Errorf("after bind %v: a writer is left", afterBind)
		}
		// the subscriber holds the reference acquired for it
		if tr.refs != 0 {
			t.Errorf("after bind %v: %d track references", afterBind, tr.refs)
		}
	}
}

func TestSubscribeEndedWhileAnswered(t *testing.T) {
	// a SUBSCRIBE the relay ends before answering is refused, and one ended while its SUBSCRIBE_OK is sent gets
	// PUBLISH_DONE after it
	name := message.FullTrackName{Name: []byte("video")}
	for _, answered := range []bool{false, true} {
		r := New(Options{}, nil, nil)
		t.Cleanup(r.Close)
		incoming := make(subscribeSink, 1)
		// the viewer never ends its side, so only the relay's own end releases the subscription's writers
		_, peer, viewer := offerSession(t, incoming, true)
		sub, err := viewer.Open(context.Background(), &message.Subscribe{Track: name}, peer, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)
		req := recv(t, incoming, "the SUBSCRIBE")
		tr := r.tracks.acquire(r, Scope{}, name)
		s := newSubscriber(tr, req, &message.Params{})
		req.SetHandlers(s, nil)
		if _, _, code, _ := s.register(); code != 0 {
			t.Fatalf("registered with %#x", code)
		}
		s.startRun(newRun(&group{t: tr}, message.SubgroupHeader{}), 0)
		if answered {
			if !s.answer() {
				t.Fatal("ended before its answer")
			}
			if err := req.Send(&message.SubscribeOK{TrackAlias: req.Session().NewTrackAlias()}); err != nil {
				t.Fatal(err)
			}
		}
		s.finish(message.DoneMalformedTrack, "malformed track")
		if !answered {
			if s.answer() {
				t.Fatal("answered after its refusal")
			}
			if resp, _ := sub.Response(ctx); resp == nil || resp.Kind() != message.KindRequestError {
				t.Errorf("answered %+v", resp)
			}
			if !waitFor(&s.wg, time.Second) {
				t.Error("a refused subscription's writer is left")
			}
			continue
		}
		if _, ok := s.markActive(); ok {
			t.Fatal("activated after its end")
		}
		s.endAnswered()
		if resp, _ := sub.Response(ctx); resp == nil || resp.Kind() != message.KindSubscribeOK {
			t.Fatalf("answered %+v", resp)
		}
		if done, ok := recv(t, peer.msgs, "PUBLISH_DONE").(*message.PublishDone); !ok ||
			done.Code != message.DoneMalformedTrack {
			t.Errorf("got %+v", done)
		}
		if !waitFor(&s.wg, time.Second) {
			t.Error("an ended subscription's writer is left")
		}
		if sub.Err() != nil {
			t.Errorf("the request ended: %v", sub.Err())
		}
	}
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}
