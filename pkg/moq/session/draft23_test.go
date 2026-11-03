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
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func fetchFilter(sg, so, eg, eo uint64) message.Params {
	var p message.Params
	p.SetLocationFilter(message.LocationFilter{
		Type: message.FilterAbsoluteRange, StartGroup: sg, StartObject: so, EndGroupDelta: eg - sg, EndObject: eo,
	})
	return p
}

func TestDraft23FetchAnswer(t *testing.T) {
	// a -23 FETCH is answered with REQUEST_OK, which its requester reads as a FETCH_OK ending at LARGEST_OBJECT, or
	// at the requested end when the response does not reach Largest Object
	sh, ch := newRecorder(), newRecorder()
	sh.onRequest = func(r *session.Request) {
		f := r.Message().(*message.Fetch)
		ok := &message.FetchOK{EndLocation: wire.Location{Group: 3, Object: 4}}
		if f.Params.LocationFilter.EndGroupDelta > 3 {
			ok.Params.SetLargestObject(ok.EndLocation)
		}
		if err := r.Send(ok); err != nil {
			t.Error(err)
		}
	}
	client, _ := pair(t, sh, ch, pairOpts{server: draft(t, 23), client: draft(t, 23)})
	for _, tc := range []struct {
		p    message.Params
		want wire.Location
	}{
		{fetchFilter(1, 0, 5, 2), wire.Location{Group: 3, Object: 4}},
		{fetchFilter(1, 0, 2, 7), wire.Location{Group: 2, Object: 7}},
	} {
		req, err := client.Open(context.Background(), &message.Fetch{Track: track("v", "live"), Params: tc.p}, ch, ch)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := req.Response(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		fok, ok := resp.(*message.FetchOK)
		if !ok || fok.EndLocation != tc.want || fok.EndOfTrack {
			t.Errorf("FETCH answered with %+v, want the end %v", resp, tc.want)
		}
		recv(t, sh.requests, "the fetch")
		// an update's answer is still a REQUEST_OK
		if _, err = req.Update(message.Params{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDraft23GoAwayFromRequester(t *testing.T) {
	// from -23 only the receiver of a request sends GOAWAY on its stream
	sh := newRecorder()
	s, p := rawPeer(t, session.RoleServer, func(o *session.Options) {
		draft(t, 23)(o)
		o.Handler = sh
	})
	st, _ := p.openBidi(&message.Subscribe{RequestID: 0, Track: track("v", "live")})
	recv(t, sh.requests, "the subscription")
	p.write(st, &message.GoAway{})
	if code := closeCode(t, s); code != uint64(message.CodeProtocolViolation) {
		t.Errorf("closed with %#x", code)
	}

	sh2, ch2 := newRecorder(), newRecorder()
	client, _ := pair(t, sh2, ch2, pairOpts{server: draft(t, 23), client: draft(t, 23)})
	sub, err := client.Open(context.Background(), &message.Subscribe{Track: track("v", "live")}, ch2, ch2)
	if err != nil {
		t.Fatal(err)
	}
	in := recv(t, sh2.requests, "the subscription")
	if err = in.Send(&message.SubscribeOK{}); err != nil {
		t.Fatal(err)
	}
	if _, err = sub.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = sub.Send(&message.GoAway{}); !errors.Is(err, session.ErrNotAllowed) {
		t.Errorf("a requester's GOAWAY: %v", err)
	}
	if err = in.Send(&message.GoAway{}); err != nil {
		t.Errorf("a responder's GOAWAY: %v", err)
	}
	if m := recv(t, ch2.messages, "the GOAWAY"); m.Kind() != message.KindGoAway {
		t.Errorf("got %s", m.Kind())
	}
}

func TestDraft23RequesterGoAwayEarlierDrafts(t *testing.T) {
	// through -22 either side of a request may send GOAWAY on it
	sh := newRecorder()
	s, p := rawPeer(t, session.RoleServer, func(o *session.Options) {
		draft(t, 22)(o)
		o.Handler = sh
	})
	st, _ := p.openBidi(&message.Subscribe{RequestID: 0, Track: track("v", "live")})
	recv(t, sh.requests, "the subscription")
	p.write(st, &message.GoAway{})
	if m := recv(t, sh.messages, "the GOAWAY"); m.Kind() != message.KindGoAway || s.Err() != nil {
		t.Errorf("got %s, %v", m.Kind(), s.Err())
	}
}

func TestDraft23Codes(t *testing.T) {
	notify := func(p *raw, st session.Stream) {
		p.write(st, &message.PublishStateNotify{})
	}
	tests := []struct {
		name  string
		draft int
		act   func(*raw, session.Stream)
		want  message.SessionCode
	}{
		{"PUBLISH_STATE_NOTIFY to a publisher", 23, notify, message.CodeRoleViolation},
		{"PUBLISH_STATE_NOTIFY to a publisher in -22", 22, notify, message.CodeProtocolViolation},
		{"an answer with no update outstanding", 23, func(p *raw, st session.Stream) {
			p.write(st, &message.RequestOK{})
		}, message.CodeUnexpectedMessage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sh := newRecorder()
			sh.onRequest = func(r *session.Request) { _ = r.Send(&message.SubscribeOK{}) }
			s, p := rawPeer(t, session.RoleServer, func(o *session.Options) {
				draft(t, tc.draft)(o)
				o.Handler = sh
			})
			st, br := p.openBidi(&message.Subscribe{RequestID: 0, Track: track("v", "live")})
			recv(t, sh.requests, "the subscription")
			p.read(br)
			tc.act(p, st)
			if code := closeCode(t, s); code != uint64(tc.want) {
				t.Errorf("closed with %#x, want %#x", code, uint64(tc.want))
			}
		})
	}
}

func TestDraft23StreamCodes(t *testing.T) {
	t.Run("a request stream opening with an answer", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		p.openBidi(&message.RequestOK{})
		if code := closeCode(t, s); code != uint64(message.CodeUnexpectedMessage) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("an oversized control message", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		st, err := p.conn.OpenStreamSync(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// SUBSCRIBE with a Length of 70000
		if _, err = st.Write([]byte{0x03, 0xc1, 0x11, 0x70}); err != nil {
			t.Fatal(err)
		}
		if code := closeCode(t, s); code != uint64(message.CodeFieldLengthExceeded) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("an unknown stream type", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		p.openUni([]byte{0x3f})
		if code := closeCode(t, s); code != uint64(message.CodeInvalidStreamHeader) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("a GOAWAY URI to a server", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		p.write(p.ctrl, &message.GoAway{NewSessionURI: "moqt://elsewhere"})
		if code := closeCode(t, s); code != uint64(message.CodeRoleViolation) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("the control stream closing", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		_ = p.ctrl.Close()
		if code := closeCode(t, s); code != uint64(message.CodeClosedControlStream) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("Close translates for an earlier draft", func(t *testing.T) {
		s, _ := rawPeer(t, session.RoleServer, draft(t, 22))
		_ = s.Close(message.CodeInvalidParameter, "")
		if code := closeCode(t, s); code != uint64(message.CodeProtocolViolation) {
			t.Errorf("closed with %#x", code)
		}
	})
}

func TestDraft23OversizedMessages(t *testing.T) {
	// a Length of 70000 exceeds the default MAX_CONTROL_MESSAGE_SIZE wherever it arrives
	oversized := []byte{0xc1, 0x11, 0x70}
	t.Run("SETUP", func(t *testing.T) {
		sc, pc := memconn.Pair()
		s, err := session.New(sc, session.Options{Role: session.RoleServer, Codec: mustDraft(t, 23)})
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = s.Start(context.Background()) }()
		t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
		newRaw(t, pc).openUni(append([]byte{0xaf, 0x00}, oversized...))
		if code := closeCode(t, s); code != uint64(message.CodeFieldLengthExceeded) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("the control stream", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		if _, err := p.ctrl.Write(append([]byte{0x10}, oversized...)); err != nil {
			t.Fatal(err)
		}
		if code := closeCode(t, s); code != uint64(message.CodeFieldLengthExceeded) {
			t.Errorf("closed with %#x", code)
		}
	})
	for _, tc := range []struct {
		name string
		b    []byte
		want message.SessionCode
	}{
		{"a request stream", append([]byte{0x02}, oversized...), message.CodeFieldLengthExceeded},
		{"FETCH_OK's reserved code", []byte{0x18, 0x00}, message.CodeUnexpectedMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := newRecorder()
			s, p := rawPeer(t, session.RoleServer, func(o *session.Options) {
				draft(t, 23)(o)
				o.Handler = sh
			})
			st, _ := p.openBidi(&message.Subscribe{RequestID: 0, Track: track("v", "live")})
			recv(t, sh.requests, "the subscription")
			if _, err := st.Write(tc.b); err != nil {
				t.Fatal(err)
			}
			if code := closeCode(t, s); code != uint64(tc.want) {
				t.Errorf("closed with %#x, want %#x", code, uint64(tc.want))
			}
		})
	}
}

func mustDraft(t *testing.T, d int) *codec.Codec {
	t.Helper()
	c, ok := codec.ForDraft(d)
	if !ok {
		t.Fatalf("draft %d", d)
	}
	return c
}

func TestDraft23Redirects(t *testing.T) {
	// a server is never redirected to another URI, and a namespace never to a track
	t.Run("a URI to a server", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, draft(t, 23))
		go func() {
			_, _ = s.Open(context.Background(), &message.Publish{Track: track("v", "live")}, newRecorder(), nil)
		}()
		st, _, _ := p.acceptBidi()
		p.write(st, &message.RequestError{
			Code: message.RequestRedirect, Redirect: &message.Redirect{ConnectURI: "moqt://b", Track: track("v", "x")},
		})
		if code := closeCode(t, s); code != uint64(message.CodeRoleViolation) {
			t.Errorf("closed with %#x", code)
		}
	})
	t.Run("a track for a namespace", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, draft(t, 23))
		go func() {
			_, _ = s.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, newRecorder(), nil)
		}()
		st, _, _ := p.acceptBidi()
		p.write(st, &message.RequestError{
			Code: message.RequestRedirect, Redirect: &message.Redirect{Track: track("v", "x")},
		})
		if code := closeCode(t, s); code != uint64(message.CodeInvalidField) {
			t.Errorf("closed with %#x", code)
		}
	})
}

func TestDraft23PublishFill(t *testing.T) {
	// a -23 PUBLISH may carry FILL_PARAMETERS, whose fill fetch stream its publisher opens under the PUBLISH's ID
	sh := newRecorder()
	_, p := rawPeer(t, session.RoleClient, func(o *session.Options) {
		draft(t, 23)(o)
		o.Handler = sh
	})
	var pp message.Params
	pp.SetFill(&message.Params{})
	p.openBidi(&message.Publish{RequestID: 1, Track: track("v", "live"), TrackAlias: 3, Params: pp})
	recv(t, sh.requests, "the PUBLISH")
	w := p.c.NewFetchWriter(message.GroupOrderAscending)
	b := p.c.AppendFetchHeader(nil, 1)
	b, err := w.AppendObject(b, &message.FetchObject{Group: 2, Object: 0, Priority: 1, PayloadLen: 2})
	if err != nil {
		t.Fatal(err)
	}
	st := p.openUni(append(b, "hi"...))
	_ = st.Close()
	got := recv(t, sh.fetches, "the fill")
	if len(got) != 1 || got[0].payload != "hi" || got[0].o.Group != 2 {
		t.Errorf("fill %+v", got)
	}
}

func TestNamespacePrefixUpdateSuffixes(t *testing.T) {
	// after an update's answer, NAMESPACE and NAMESPACE_DONE name suffixes of the new prefix, and a namespace the
	// new prefix no longer covers is gone with the answer
	for _, d := range []int{23, 22} {
		t.Run(strconv.Itoa(d), func(t *testing.T) {
			ch := newRecorder()
			s, p := rawPeer(t, session.RoleClient, draft(t, d))
			opened := make(chan *session.Request, 1)
			go func() {
				if r, err := s.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, ch, nil); err == nil {
					opened <- r
				}
			}()
			st, br, _ := p.acceptBidi()
			p.write(st, &message.RequestOK{})
			p.write(st, &message.Namespace{Suffix: ns("a")})
			p.write(st, &message.Namespace{Suffix: ns("b", "c")})
			recv(t, ch.messages, "NAMESPACE a")
			recv(t, ch.messages, "NAMESPACE b/c")
			req := recv(t, opened, "the request")
			var u message.Params
			u.SetTrackNamespacePrefix(ns())
			if _, err := req.Update(u); err != nil {
				t.Fatal(err)
			}
			p.read(br)
			p.write(st, &message.RequestOK{})
			recv(t, ch.messages, "the widening answer")
			p.write(st, &message.NamespaceDone{Suffix: ns("live", "a")})
			recv(t, ch.messages, "NAMESPACE_DONE live/a")
			u.SetTrackNamespacePrefix(ns("news"))
			if _, err := req.Update(u); err != nil {
				t.Fatal(err)
			}
			p.read(br)
			p.write(st, &message.RequestOK{})
			recv(t, ch.messages, "the narrowing answer")
			// live/b/c went with the answer, so it cannot be withdrawn again
			p.write(st, &message.NamespaceDone{Suffix: ns("b", "c")})
			if code := closeCode(t, s); code != uint64(mustDraft(t, d).SessionCode(message.CodeUnexpectedMessage)) {
				t.Errorf("closed with %#x", code)
			}
		})
	}
}

func TestDraft23FillOvertakesPublish(t *testing.T) {
	// a fill stream that arrives before its PUBLISH waits for it rather than being refused
	sh := newRecorder()
	_, p := rawPeer(t, session.RoleClient, func(o *session.Options) {
		draft(t, 23)(o)
		o.Handler = sh
	})
	w := p.c.NewFetchWriter(message.GroupOrderAscending)
	b := p.c.AppendFetchHeader(nil, 1)
	b, err := w.AppendObject(b, &message.FetchObject{Group: 2, Object: 0, Priority: 1, PayloadLen: 2})
	if err != nil {
		t.Fatal(err)
	}
	fill := p.openUni(append(b, "hi"...))
	_ = fill.Close()
	time.Sleep(20 * time.Millisecond)
	var pp message.Params
	pp.SetFill(&message.Params{})
	p.openBidi(&message.Publish{RequestID: 1, Track: track("v", "live"), TrackAlias: 3, Params: pp})
	recv(t, sh.requests, "the PUBLISH")
	if got := recv(t, sh.fetches, "the fill"); len(got) != 1 || got[0].payload != "hi" {
		t.Errorf("fill %+v", got)
	}
}

func TestDraft23AdvertisedMessageLimit(t *testing.T) {
	// a session that advertises a MAX_CONTROL_MESSAGE_SIZE above the default accepts messages up to it
	sh := newRecorder()
	s, p := rawPeer(t, session.RoleServer, func(o *session.Options) {
		draft(t, 23)(o)
		o.Handler = sh
		o.Setup.MaxControlMessageSize = 1 << 20
	})
	// a SUBSCRIBE carrying two 40000-byte tokens, which no 65535-byte message could
	body := []byte{0x00}
	body = wire.AppendNamespace(body, ns("live"))
	body = wire.AppendBytes(body, []byte("v"))
	body = append(body, 0x02)
	for i, delta := range []uint64{0x03, 0x00} {
		tok, err := wire.AppendToken(nil, wire.Token{AliasType: wire.TokenUseValue, Value: make([]byte, 40000+i)})
		if err != nil {
			t.Fatal(err)
		}
		body = wire.AppendBytes(wire.AppendVarint(body, delta), tok)
	}
	msg := wire.AppendVarint(wire.AppendVarint(nil, 0x03), uint64(len(body)))
	st, err := p.conn.OpenStreamSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Write(append(msg, body...)); err != nil {
		t.Fatal(err)
	}
	in := recv(t, sh.requests, "the large SUBSCRIBE")
	if toks := in.Message().(*message.Subscribe).Params.AuthTokens; len(toks) != 2 || len(toks[1].Value) != 40001 {
		t.Errorf("tokens %d", len(toks))
	}
	if s.Err() != nil {
		t.Errorf("the session ended: %v", s.Err())
	}
	sc, _ := memconn.Pair()
	if _, err = session.New(sc, session.Options{
		Codec: mustDraft(t, 23),
		Setup: message.Setup{MaxControlMessageSize: session.MaxControlMessageSize + 1},
	}); err == nil {
		t.Error("a limit past MaxControlMessageSize is refused")
	}
}
