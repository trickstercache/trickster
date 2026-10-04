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

package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

func draft(t *testing.T, d int) func(*session.Options) {
	t.Helper()
	c, ok := codec.ForDraft(d)
	if !ok {
		t.Fatalf("draft %d", d)
	}
	return func(o *session.Options) { o.Codec = c }
}

func TestPublishOKInDraft17(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	sh.onRequest = func(r *session.Request) {
		// the responder answers as every draft does; the session spells it PUBLISH_OK for -17
		if err := r.Send(&message.RequestOK{}); err != nil {
			t.Error(err)
		}
	}
	client, _ := pair(t, sh, ch, pairOpts{server: draft(t, 17), client: draft(t, 17)})
	pub, err := client.Open(context.Background(), &message.Publish{Track: track("cam", "live"),
		TrackAlias: client.NewTrackAlias()}, ch, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := pub.Response(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.(*message.PublishOK); !ok {
		t.Fatalf("a -17 publisher is answered with PUBLISH_OK, got %s", resp.Kind())
	}
	// later answers on the request stay REQUEST_OK
	in := recv(t, sh.requests, "the PUBLISH")
	var p message.Params
	p.SetForward(false)
	if _, err = in.Update(p); err != nil {
		t.Fatal(err)
	}
	recv(t, ch.messages, "the update")
	if err = pub.Send(&message.RequestOK{}); err != nil {
		t.Fatal(err)
	}
	if m := recv(t, sh.messages, "the update's answer"); m.Kind() != message.KindRequestOK {
		t.Errorf("an update's answer is REQUEST_OK, got %s", m.Kind())
	}
}

func TestUpdateLimitOnlyWhereDefined(t *testing.T) {
	// MAX_REQUEST_UPDATES arrived in -19, so a -18 peer that was never told a limit is held to none
	sh, ch := newRecorder(), newRecorder()
	limit := func(o *session.Options) {
		draft(t, 18)(o)
		o.Setup.MaxRequestUpdates = 1
	}
	client, server := pair(t, sh, ch, pairOpts{server: limit, client: draft(t, 18)})
	sub, err := client.Open(context.Background(), &message.Subscribe{Track: track("v", "live")}, ch, ch)
	if err != nil {
		t.Fatal(err)
	}
	in := recv(t, sh.requests, "the subscription")
	if err = in.Send(&message.SubscribeOK{TrackAlias: server.NewTrackAlias()}); err != nil {
		t.Fatal(err)
	}
	if _, err = sub.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	var p message.Params
	p.SetSubscriberPriority(1)
	for range 3 {
		if _, err = sub.Update(p); err != nil {
			t.Fatal(err)
		}
		recv(t, sh.messages, "an update")
	}
	if server.Err() != nil || client.Err() != nil {
		t.Errorf("updates past an unsent limit end no session: %v %v", server.Err(), client.Err())
	}
}

const heldWindow = 50 * time.Millisecond // how long a held request is watched for an early delivery

func TestRequiredRequestID(t *testing.T) {
	sh := newRecorder()
	s, p := rawPeer(t, session.RoleServer, func(o *session.Options) {
		draft(t, 17)(o)
		o.Handler = sh
	})
	sub := func(id, delta uint64, name string) *message.Subscribe {
		return &message.Subscribe{RequestID: id, RequiredRequestID: delta, Track: track(name, "live")}
	}
	// request 2 depends on request 0, so it is held until request 0 has been handed to the handler
	p.openBidi(sub(2, 1, "second"))
	select {
	case r := <-sh.requests:
		t.Fatalf("request %d was delivered before the request it requires arrived", r.RequestID())
	case <-time.After(heldWindow):
	}
	p.openBidi(sub(0, 0, "first"))
	delivered := map[uint64]bool{}
	for range 2 {
		delivered[recv(t, sh.requests, "a request").RequestID()] = true
	}
	if !delivered[0] || !delivered[2] {
		t.Errorf("delivered %v", delivered)
	}
	// one whose required request never arrives times out
	_, br := p.openBidi(sub(6, 1, "orphan"))
	if re, ok := p.read(br).(*message.RequestError); !ok || re.Code != message.RequestTimeout {
		t.Errorf("an orphaned request is refused with TIMEOUT, got %+v", re)
	}
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	// one naming a request before the first is a session error
	s2, p2 := rawPeer(t, session.RoleServer, draft(t, 17))
	p2.openBidi(sub(2, 2, "invalid"))
	if code := closeCode(t, s2); code != uint64(message.CodeInvalidRequiredRequestID) {
		t.Errorf("closed with %#x", code)
	}
}

func TestPublishBlockedPerDraft(t *testing.T) {
	for _, d := range []int{17, 18} {
		sh, ch := newRecorder(), newRecorder()
		sh.onRequest = func(r *session.Request) {
			if err := r.Send(&message.RequestOK{}); err != nil {
				t.Error(err)
			}
		}
		client, _ := pair(t, sh, ch, pairOpts{server: draft(t, d), client: draft(t, d)})
		req, err := client.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, ch, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = req.Response(context.Background()); err != nil {
			t.Fatal(err)
		}
		in := recv(t, sh.requests, "the namespace subscription")
		err = in.Send(&message.PublishSkipped{Suffix: ns(), TrackName: []byte("v")})
		switch d {
		case 17:
			// -17's SUBSCRIBE_NAMESPACE may ask for PUBLISHes, and PUBLISH_BLOCKED reports one not sent
			if err != nil {
				t.Fatal(err)
			}
			if m := recv(t, ch.messages, "PUBLISH_BLOCKED"); m.Kind() != message.KindPublishSkipped {
				t.Errorf("got %s", m.Kind())
			}
		default:
			if !errors.Is(err, session.ErrNotAllowed) {
				t.Errorf("draft %d: PUBLISH_SKIPPED belongs to SUBSCRIBE_TRACKS, got %v", d, err)
			}
		}
	}
}
