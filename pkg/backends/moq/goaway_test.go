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
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	mo "github.com/trickstercache/trickster/v2/pkg/backends/moq/options"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
	"github.com/trickstercache/trickster/v2/pkg/proxy/quicdial"

	"github.com/quic-go/quic-go"
)

const goAwayURI = "moqt://alt.example.com:4443/live"

type goAwayWatch struct {
	got chan *message.GoAway
}

func (w goAwayWatch) HandleRequest(r *session.Request) {
	_ = r.Reject(message.RequestNotSupported, "", 0)
}

func (w goAwayWatch) HandleGoAway(_ *session.Session, m *message.GoAway) {
	w.got <- m
}

func dialDraft(t *testing.T, addr string, draft int, h session.Handler) *session.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	conn, err := quicdial.DialAddr(ctx, addr, &tls.Config{InsecureSkipVerify: true, // #nosec G402 -- test certificate
		NextProtos: []string{codec.ALPNFor(draft)}, MinVersion: tls.VersionTLS13},
		&quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	s, err := session.New(session.FromQUIC(conn), session.Options{
		Role: session.RoleClient, Codec: c, Handler: h,
		Setup: message.Setup{Path: "/", HasPath: true, Authority: "localhost", HasAuthority: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRedirectFor(t *testing.T) {
	// each request is redirected to the same track, or a namespace request to its namespace; a joining fetch cannot be
	ft := message.FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte("video")}
	nsOnly := message.FullTrackName{Namespace: ft.Namespace}
	for m, want := range map[message.Message]*message.Redirect{
		&message.Subscribe{Track: ft}:                     {ConnectURI: goAwayURI, Track: ft},
		&message.TrackStatus{Track: ft}:                   {ConnectURI: goAwayURI, Track: ft},
		&message.Publish{Track: ft}:                       {ConnectURI: goAwayURI, Track: ft},
		&message.Fetch{Track: ft}:                         {ConnectURI: goAwayURI, Track: ft},
		&message.PublishNamespace{Prefix: ft.Namespace}:   {ConnectURI: goAwayURI, Track: nsOnly},
		&message.SubscribeNamespace{Prefix: ft.Namespace}: {ConnectURI: goAwayURI, Track: nsOnly},
		&message.SubscribeTracks{Prefix: ft.Namespace}:    {ConnectURI: goAwayURI, Track: nsOnly},
		&message.Fetch{Joining: &message.JoiningFetch{}}:  nil,
		&message.RequestOK{}:                              nil,
	} {
		got := redirectFor(m, goAwayURI)
		if (got == nil) != (want == nil) || (got != nil && (got.ConnectURI != want.ConnectURI ||
			!got.Track.Equal(want.Track))) {
			t.Errorf("%T redirected to %+v, want %+v", m, got, want)
		}
	}
}

func refusal(t *testing.T, s *session.Session, m message.Message) *message.RequestError {
	t.Helper()
	req, err := s.Open(context.Background(), m, nopHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	resp, err := req.Response(ctx)
	if err != nil {
		t.Fatal(err)
	}
	re, ok := resp.(*message.RequestError)
	if !ok {
		t.Fatalf("a departing session served %s", resp.Kind())
	}
	return re
}

func TestShutdownSendsGoAway(t *testing.T) {
	o := startOrigin(t)
	lo := mo.NewListener()
	lo.GoAwayURI = goAwayURI
	st := startListener(t, lo, newBackend(t, "live", "moqt://"+o.addr+"/live"))
	modern := goAwayWatch{got: make(chan *message.GoAway, 1)}
	joining := goAwayWatch{got: make(chan *message.GoAway, 1)}
	legacy := goAwayWatch{got: make(chan *message.GoAway, 1)}
	s21, s19, s17 := dialDraft(t, st.addr, 21, modern), dialDraft(t, st.addr, 19, joining),
		dialDraft(t, st.addr, 17, legacy)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- st.srv.Shutdown(ctx) }()
	for _, w := range []goAwayWatch{modern, joining, legacy} {
		g := recv(t, w.got, "GOAWAY")
		if g.NewSessionURI != goAwayURI || g.Timeout == 0 {
			t.Errorf("GOAWAY %+v", g)
		}
	}

	// a request on a departing session is sent after the session, where the draft has REDIRECT
	track := message.FullTrackName{Namespace: wire.Namespace{[]byte("live")}, Name: []byte("video")}
	re := refusal(t, s21, &message.Subscribe{Track: track})
	if re.Code != message.RequestRedirect || re.Redirect == nil || re.Redirect.ConnectURI != goAwayURI ||
		!re.Redirect.Track.Equal(track) {
		t.Errorf("-21 refusal %+v %+v", re, re.Redirect)
	}
	ns := refusal(t, s21, &message.SubscribeNamespace{Prefix: track.Namespace})
	if ns.Code != message.RequestRedirect || len(ns.Redirect.Track.Name) != 0 {
		t.Errorf("a namespace request is redirected with its namespace and no track: %+v", ns.Redirect)
	}
	if join := refusal(t, s19, &message.Fetch{Track: track, Joining: &message.JoiningFetch{RequestID: 0}}); join.Code !=
		message.RequestGoingAway {
		t.Errorf("a joining fetch has no subscription to join elsewhere: %+v", join)
	}
	if old := refusal(t, s17, &message.Subscribe{Track: track}); old.Code != message.RequestGoingAway ||
		old.RetryInterval != retryNewSession {
		t.Errorf("-17 refusal %+v", old)
	}

	// once every client has moved on, the drain ends early
	for _, s := range []*session.Session{s21, s19, s17} {
		_ = s.Close(message.CodeNoError, "")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("a drain the clients finished: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("the drain did not end once the clients left")
	}
}

func TestShutdownTimesOutLingeringClients(t *testing.T) {
	o := startOrigin(t)
	st := startListener(t, mo.NewListener(), newBackend(t, "live", "moqt://"+o.addr+"/live"))
	w := goAwayWatch{got: make(chan *message.GoAway, 1)}
	s := dialDraft(t, st.addr, 21, w)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := st.srv.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a drain cut short by its deadline: %v", err)
	}
	if g := recv(t, w.got, "GOAWAY"); g.NewSessionURI != "" {
		t.Errorf("without goaway_uri the client reconnects where it is: %+v", g)
	}
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("a client that stays past the drain is closed")
	}
	var ce *session.CloseError
	if !errors.As(s.Err(), &ce) || ce.Code != uint64(message.CodeGoAwayTimeout) {
		t.Errorf("closed with %v, not GOAWAY_TIMEOUT", s.Err())
	}
}
