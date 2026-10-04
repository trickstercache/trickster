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
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func handshakeWith(t *testing.T, role session.Role, o func(*session.Options), m *message.Setup) (*session.Session, *raw) {
	// starts a session against a hand-driven peer whose SETUP is m
	t.Helper()
	sc, pc := memconn.Pair()
	opts := session.Options{Role: role, Codec: c22(t)}
	if o != nil {
		o(&opts)
	}
	s, err := session.New(sc, opts)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Start(context.Background()) }()
	p := newRaw(t, pc)
	p.setup(m)
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	return s, p
}

func TestPeerSetupRules(t *testing.T) {
	wt := func(o *session.Options) { o.WebTransport = true }
	tests := []struct {
		name string
		role session.Role
		opts func(*session.Options)
		m    *message.Setup
		want message.SessionCode
	}{
		{"path over webtransport", session.RoleServer, wt, &message.Setup{Path: "/x", HasPath: true}, message.CodeInvalidPath},
		{
			"authority over webtransport", session.RoleServer, wt, &message.Setup{Authority: "a", HasAuthority: true},
			message.CodeInvalidAuthority,
		},
		{"path from a server", session.RoleClient, nil, &message.Setup{Path: "/x", HasPath: true}, message.CodeInvalidPath},
		{
			"authority from a server", session.RoleClient, nil, &message.Setup{Authority: "a", HasAuthority: true},
			message.CodeInvalidAuthority,
		},
		{
			"alias use in setup", session.RoleServer, nil,
			&message.Setup{AuthTokens: []wire.Token{{AliasType: wire.TokenUseAlias, Alias: 1}}}, message.CodeProtocolViolation,
		},
		{
			"deleting an unknown alias", session.RoleClient, nil,
			&message.Setup{AuthTokens: []wire.Token{{AliasType: wire.TokenDelete, Alias: 1}}}, message.CodeUnknownAuthTokenAlias,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := handshakeWith(t, tc.role, tc.opts, tc.m)
			if code := closeCode(t, s); code != uint64(tc.want) {
				t.Errorf("closed with %#x, want %#x", code, uint64(tc.want))
			}
		})
	}
}

func TestSetupTokens(t *testing.T) {
	var got []wire.Token
	h := newRecorder()
	s, p := handshakeWith(t, session.RoleServer, func(o *session.Options) {
		o.Setup.MaxAuthTokenCacheSize = 1024
		o.Handler = h
		o.OnSetup = func(_ *session.Session, m *message.Setup) error {
			got = m.AuthTokens
			return nil
		}
	}, &message.Setup{AuthTokens: []wire.Token{{AliasType: wire.TokenRegister, Alias: 4, Type: 1, Value: []byte("secret")}}})
	p.acceptControl()
	<-s.Ready()
	if len(got) != 1 || got[0].AliasType != wire.TokenUseValue || string(got[0].Value) != "secret" {
		t.Fatalf("setup tokens resolved to %+v", got)
	}
	var sp message.Params
	sp.AuthTokens = []wire.Token{{AliasType: wire.TokenUseAlias, Alias: 4}, {AliasType: wire.TokenDelete, Alias: 4}}
	st, _ := p.openBidi(&message.Subscribe{RequestID: 0, Track: track("v", "ns"), Params: sp})
	in := recv(t, h.requests, "request")
	toks := in.Message().(*message.Subscribe).Params.AuthTokens
	if len(toks) != 1 || toks[0].AliasType != wire.TokenUseValue || string(toks[0].Value) != "secret" || toks[0].Type != 1 {
		t.Fatalf("request tokens resolved to %+v", toks)
	}
	// the alias was deleted, so a later use of it is an error that ends the session
	var up message.Params
	up.AuthTokens = []wire.Token{{AliasType: wire.TokenUseAlias, Alias: 4}}
	p.write(st, &message.RequestUpdate{RequestID: 2, Params: up})
	if code := closeCode(t, s); code != uint64(message.CodeUnknownAuthTokenAlias) {
		t.Errorf("closed with %#x", code)
	}
}

func TestControlStreamViolations(t *testing.T) {
	tests := []struct {
		name string
		act  func(p *raw)
		want message.SessionCode
	}{
		{"second control stream", func(p *raw) { p.setup(&message.Setup{}) }, message.CodeProtocolViolation},
		{"unexpected message", func(p *raw) { p.write(p.ctrl, &message.RequestOK{}) }, message.CodeProtocolViolation},
		{"control stream closed", func(p *raw) { _ = p.ctrl.Close() }, message.CodeProtocolViolation},
		{"second goaway", func(p *raw) {
			p.write(p.ctrl, &message.GoAway{})
			p.write(p.ctrl, &message.GoAway{})
		}, message.CodeProtocolViolation},
		{
			"client goaway with a uri", func(p *raw) { p.write(p.ctrl, &message.GoAway{NewSessionURI: "moqt://x"}) },
			message.CodeProtocolViolation,
		},
		{"unknown stream type", func(p *raw) { p.openUni([]byte{0x01}) }, message.CodeProtocolViolation},
		{
			"malformed datagram", func(p *raw) { _ = p.conn.SendDatagram([]byte{0x10, 0x00, 0x00}) },
			message.CodeProtocolViolation,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, p := rawPeer(t, session.RoleServer, nil)
			tc.act(p)
			if code := closeCode(t, s); code != uint64(tc.want) {
				t.Errorf("closed with %#x, want %#x", code, uint64(tc.want))
			}
		})
	}
}

func TestGoAwayReachesHandler(t *testing.T) {
	h := newRecorder()
	s, p := rawPeer(t, session.RoleClient, func(o *session.Options) { o.Handler = h })
	// padding and datagrams for unknown aliases are dropped without harm
	_ = p.conn.SendDatagram(wire.AppendVarint(nil, codec.DatagramTypePadding))
	dg, _ := p.c.AppendDatagram(nil, &message.Datagram{TrackAlias: 77, Payload: []byte("x")})
	_ = p.conn.SendDatagram(dg)
	p.write(p.ctrl, &message.GoAway{NewSessionURI: "moqt://elsewhere", Timeout: 2500})
	g := recv(t, h.goaways, "goaway")
	if g.NewSessionURI != "moqt://elsewhere" || g.Timeout != 2500 || !s.GoAwayReceived() || s.Err() != nil {
		t.Errorf("goaway %+v, err %v", g, s.Err())
	}
}

func unknownMessageType(t *testing.T) uint64 {
	t.Helper()
	c := c22(t)
	for typ := uint64(1); typ < 0x3f; typ++ {
		if _, ok := c.MessageKind(typ); !ok {
			return typ
		}
	}
	t.Fatal("no unused message type")
	return 0
}

func TestRequestStreamViolations(t *testing.T) {
	sub := func(id uint64) *message.Subscribe {
		return &message.Subscribe{RequestID: id, Track: track("v", "ns")}
	}
	tests := []struct {
		name string
		opts func(*session.Options)
		act  func(p *raw)
		want message.SessionCode
	}{
		{"not a request", nil, func(p *raw) { p.openBidi(&message.SubscribeOK{}) }, message.CodeProtocolViolation},
		{"wrong parity", nil, func(p *raw) { p.openBidi(sub(1)) }, message.CodeInvalidRequestID},
		{"duplicate id", nil, func(p *raw) {
			p.openBidi(sub(0))
			p.openBidi(sub(0))
		}, message.CodeInvalidRequestID},
		{"unknown message type", nil, func(p *raw) {
			st, _ := p.openBidi(sub(0))
			_, _ = st.Write(append(wire.AppendVarint(nil, unknownMessageType(t)), 0, 0))
		}, message.CodeProtocolViolation},
		{"too many updates", func(o *session.Options) { o.Setup.MaxRequestUpdates = 1 }, func(p *raw) {
			st, _ := p.openBidi(sub(0))
			p.write(st, &message.RequestUpdate{RequestID: 2})
			p.write(st, &message.RequestUpdate{RequestID: 4})
		}, message.CodeTooManyRequestUpdates},
		{"update reusing an id", nil, func(p *raw) {
			st, _ := p.openBidi(sub(0))
			p.write(st, &message.RequestUpdate{RequestID: 0})
		}, message.CodeInvalidRequestID},
		{"responder message from the requester", nil, func(p *raw) {
			st, _ := p.openBidi(sub(0))
			p.write(st, &message.PublishDone{})
		}, message.CodeProtocolViolation},
		{"unknown token alias", nil, func(p *raw) {
			m := sub(0)
			m.Params.AuthTokens = []wire.Token{{AliasType: wire.TokenUseAlias, Alias: 9}}
			p.openBidi(m)
		}, message.CodeUnknownAuthTokenAlias},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, p := rawPeer(t, session.RoleServer, tc.opts)
			tc.act(p)
			if code := closeCode(t, s); code != uint64(tc.want) {
				t.Errorf("closed with %#x, want %#x", code, uint64(tc.want))
			}
		})
	}
}

func TestResponderViolations(t *testing.T) {
	answers := func(ms ...message.Message) func(*testing.T, *session.Session, *raw) {
		return func(t *testing.T, s *session.Session, p *raw) {
			h := newRecorder()
			if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
				t.Fatal(err)
			}
			st, _, _ := p.acceptBidi()
			for _, m := range ms {
				p.write(st, m)
			}
		}
	}
	publish := func(id, alias uint64, name string) *message.Publish {
		return &message.Publish{RequestID: id, Track: track(name, "ns"), TrackAlias: alias}
	}
	tests := []struct {
		name string
		act  func(*testing.T, *session.Session, *raw)
		want message.SessionCode
	}{
		{"wrong answer", answers(&message.FetchOK{}), message.CodeProtocolViolation},
		{
			"answer with no update", answers(&message.SubscribeOK{TrackAlias: 1}, &message.RequestOK{}),
			message.CodeProtocolViolation,
		},
		{"message after publish done", answers(&message.SubscribeOK{TrackAlias: 1}, &message.PublishDone{},
			&message.PublishStateNotify{}), message.CodeProtocolViolation},
		{
			"second goaway", answers(&message.SubscribeOK{TrackAlias: 1}, &message.GoAway{}, &message.GoAway{}),
			message.CodeProtocolViolation,
		},
		{"namespace done before namespace", func(t *testing.T, s *session.Session, p *raw) {
			h := newRecorder()
			if _, err := s.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("ns")}, h, h); err != nil {
				t.Fatal(err)
			}
			st, _, _ := p.acceptBidi()
			p.write(st, &message.RequestOK{})
			p.write(st, &message.Namespace{Suffix: ns("a")})
			p.write(st, &message.NamespaceDone{Suffix: ns("a")})
			p.write(st, &message.NamespaceDone{Suffix: ns("a")})
		}, message.CodeProtocolViolation},
		{"duplicate track alias", func(_ *testing.T, _ *session.Session, p *raw) {
			p.openBidi(publish(1, 7, "a"))
			p.openBidi(publish(3, 7, "b"))
		}, message.CodeDuplicateTrackAlias},
		{"server request with an even id", func(_ *testing.T, _ *session.Session, p *raw) {
			p.openBidi(publish(0, 7, "a"))
		}, message.CodeInvalidRequestID},
		{"publish reusing a subscription's alias", func(t *testing.T, s *session.Session, p *raw) {
			h := newRecorder()
			if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
				t.Fatal(err)
			}
			st, _, _ := p.acceptBidi()
			p.write(st, &message.SubscribeOK{TrackAlias: 7})
			p.openBidi(publish(1, 7, "other"))
		}, message.CodeDuplicateTrackAlias},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, p := rawPeer(t, session.RoleClient, nil)
			tc.act(t, s, p)
			if code := closeCode(t, s); code != uint64(tc.want) {
				t.Errorf("closed with %#x, want %#x", code, uint64(tc.want))
			}
		})
	}
}

func TestSharedTrackAlias(t *testing.T) {
	h := newRecorder()
	s, p := rawPeer(t, session.RoleClient, func(o *session.Options) { o.Handler = h })
	for _, id := range []uint64{1, 3} {
		p.openBidi(&message.Publish{RequestID: id, Track: track("a", "ns"), TrackAlias: 7})
		if r := recv(t, h.requests, "publish"); r.RequestID() != id {
			t.Errorf("request %d", r.RequestID())
		}
	}
	if s.Err() != nil {
		t.Errorf("two publishes of one track may share an alias: %v", s.Err())
	}
}

func TestRefusedRequests(t *testing.T) {
	h := newRecorder()
	tests := []struct {
		name  string
		opts  func(*session.Options)
		track message.FullTrackName
		held  int
		code  message.RequestCode
		retry uint64
	}{
		{"session namespace", nil, track("v", ".session", "x"), 0, message.RequestDoesNotExist, 0},
		{"reserved namespace", nil, track("v", "."), 0, message.RequestDoesNotExist, 0},
		{
			"no handler", func(o *session.Options) { o.Handler = nil }, track("v", "ns"), 0,
			message.RequestNotSupported, 0,
		},
		{
			"excessive load", func(o *session.Options) { o.MaxRequests, o.Handler = 1, h }, track("v", "ns"), 1,
			message.RequestExcessiveLoad, 1000,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, p := rawPeer(t, session.RoleServer, tc.opts)
			var id uint64
			for ; id < uint64(tc.held)*2; id += 2 {
				p.openBidi(&message.Subscribe{RequestID: id, Track: tc.track})
				recv(t, h.requests, "held request")
			}
			_, br := p.openBidi(&message.Subscribe{RequestID: id, Track: tc.track})
			m := p.read(br)
			if _, err := br.ReadByte(); err != io.EOF {
				t.Errorf("a refused request's stream ends after REQUEST_ERROR: %v", err)
			}
			re, ok := m.(*message.RequestError)
			if !ok || re.Code != tc.code || re.RetryInterval != tc.retry {
				t.Fatalf("got %+v", m)
			}
			if s.Err() != nil {
				t.Errorf("a refusal leaves the session up: %v", s.Err())
			}
		})
	}
}
