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

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func TestHandshake(t *testing.T) {
	var seen *message.Setup
	client, server := pair(t, newRecorder(), newRecorder(), pairOpts{
		client: func(o *session.Options) {
			o.Setup.Path, o.Setup.HasPath = "/live", true
			o.Setup.Authority, o.Setup.HasAuthority = "relay.example.com", true
			o.Setup.MaxRequestUpdates = 4
		},
		server: func(o *session.Options) {
			o.OnSetup = func(_ *session.Session, peer *message.Setup) error {
				seen = peer
				return nil
			}
			// a server never sends PATH or AUTHORITY even when configured with them
			o.Setup.Path, o.Setup.HasPath = "/x", true
		},
	})
	if seen == nil || seen.Path != "/live" || seen.Authority != "relay.example.com" || seen.Implementation != "client" {
		t.Fatalf("server saw %+v", seen)
	}
	if p := client.PeerSetup(); p == nil || p.Implementation != "server" || p.HasPath {
		t.Errorf("client saw %+v", p)
	}
	if server.PeerSetup().MaxRequestUpdates != 4 {
		t.Error("setup options reach the peer")
	}
	select {
	case <-client.Ready():
	default:
		t.Error("a started session is ready")
	}
	if client.Role() != session.RoleClient || server.Role() != session.RoleServer || client.WebTransport() ||
		client.Codec().Draft() != 22 || client.Err() != nil || client.Context().Err() != nil {
		t.Error("accessors")
	}
}

func TestHandshakeTimeout(t *testing.T) {
	_, sc := memconn.Pair()
	s, err := session.New(sc, session.Options{Role: session.RoleServer, Codec: c22(t), HandshakeTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, session.ErrHandshakeTimeout) {
		t.Fatalf("got %v", err)
	}
	if code := closeCode(t, s); code != uint64(message.CodeControlMessageTimeout) {
		t.Errorf("close code %#x", code)
	}
	if _, err := session.New(nil, session.Options{}); err == nil {
		t.Error("a session needs a connection and a codec")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, sc2 := memconn.Pair()
	s2, _ := session.New(sc2, session.Options{Role: session.RoleServer, Codec: c22(t)})
	if err := s2.Start(ctx); err == nil {
		t.Error("a canceled handshake fails")
	}
}

func TestOnSetupRejects(t *testing.T) {
	cc, sc := memconn.Pair()
	server, _ := session.New(sc, session.Options{
		Role: session.RoleServer, Codec: c22(t),
		OnSetup: func(*session.Session, *message.Setup) error {
			return &message.SessionError{Code: message.CodeInvalidPath, Reason: "no such path"}
		},
	})
	client, _ := session.New(cc, session.Options{Role: session.RoleClient, Codec: c22(t)})
	go func() { _ = server.Start(context.Background()) }()
	_ = client.Start(context.Background())
	if code := closeCode(t, server); code != uint64(message.CodeInvalidPath) {
		t.Errorf("server closed with %#x", code)
	}
	if ce, ok := errors.AsType[*session.CloseError](func() error { <-client.Done(); return client.Err() }()); !ok ||
		!ce.Remote || ce.Code != uint64(message.CodeInvalidPath) || ce.Error() == "" {
		t.Errorf("client saw %v", client.Err())
	}
}

func subscribe(t *testing.T, client *session.Session, h *recorder, name string) *session.Request {
	t.Helper()
	var p message.Params
	p.SetForward(true)
	r, err := client.Open(context.Background(), &message.Subscribe{Track: track(name, "live"), Params: p}, h, h)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSubscribeLifecycle(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, server := pair(t, sh, ch, pairOpts{})
	r := subscribe(t, client, ch, "video")
	if !r.Local() || r.Kind() != message.KindSubscribe || r.Session() != client || r.RequestID() != 0 {
		t.Errorf("request accessors %v %v %d", r.Local(), r.Kind(), r.RequestID())
	}
	in := recv(t, sh.requests, "server request")
	sub := in.Message().(*message.Subscribe)
	if in.Local() || string(sub.Track.Name) != "video" || !sub.Params.Forward {
		t.Fatalf("server got %+v", sub)
	}
	alias := server.NewTrackAlias()
	if err := in.Send(&message.SubscribeOK{TrackAlias: alias}); err != nil {
		t.Fatal(err)
	}
	resp, err := r.Response(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ok, isOK := resp.(*message.SubscribeOK); !isOK || ok.TrackAlias != alias {
		t.Fatalf("response %+v", resp)
	}
	if again, _ := r.Response(context.Background()); again != resp {
		t.Error("the response can be read again")
	}

	pub, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 3, Priority: 9}, session.Urgency(128, 9), false)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range []string{"key", "delta"} {
		if err = pub.WriteObject(&message.SubgroupObject{ID: uint64(i), PayloadLen: uint64(len(p))}); err != nil {
			t.Fatal(err)
		}
		if _, err = pub.Write([]byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err = pub.WriteObject(&message.SubgroupObject{ID: 2, Status: message.StatusEndOfGroup}); err != nil {
		t.Fatal(err)
	}
	if err = pub.Close(); err != nil {
		t.Fatal(err)
	}
	objs := recv(t, ch.subgroups, "objects")
	if len(objs) != 3 || objs[0].payload != "key" || objs[1].payload != "delta" || objs[2].status != message.StatusEndOfGroup {
		t.Fatalf("objects %+v", objs)
	}
	if in.StreamCount() != 1 {
		t.Errorf("stream count %d", in.StreamCount())
	}

	if err = in.Send(&message.PublishDone{Code: message.DoneTrackEnded, StreamCount: in.StreamCount()}); err != nil {
		t.Fatal(err)
	}
	if err = in.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if done := recv(t, ch.messages, "PUBLISH_DONE"); done.Kind() != message.KindPublishDone {
		t.Fatalf("got %s", done.Kind())
	}
	if err := recv(t, ch.closes, "client close"); err != nil {
		t.Errorf("a finished subscription ends cleanly: %v", err)
	}
	if err := recv(t, sh.closes, "server close"); err != nil {
		t.Errorf("the publisher's side ends cleanly too: %v", err)
	}
	if r.Context().Err() == nil || r.Err() != nil {
		t.Error("an ended request's context is done")
	}
	if err := in.Send(&message.PublishDone{}); !errors.Is(err, session.ErrRequestEnded) {
		t.Errorf("sending on an ended request: %v", err)
	}
}

func TestRejectAndSendRules(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	sh.onRequest = func(r *session.Request) {
		if err := r.Send(&message.FetchOK{}); !errors.Is(err, session.ErrNotAllowed) {
			t.Errorf("FETCH_OK cannot answer SUBSCRIBE: %v", err)
		}
		if err := r.Send(&message.RequestOK{}); !errors.Is(err, session.ErrNotAllowed) {
			t.Errorf("REQUEST_OK cannot answer SUBSCRIBE: %v", err)
		}
		if err := r.Reject(message.RequestDoesNotExist, "nope", 7); err != nil {
			t.Errorf("reject: %v", err)
		}
		if err := r.Send(&message.PublishDone{}); !errors.Is(err, session.ErrNotAllowed) {
			t.Errorf("nothing follows a rejection: %v", err)
		}
	}
	client, _ := pair(t, sh, ch, pairOpts{})
	r := subscribe(t, client, ch, "missing")
	resp, err := r.Response(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	re, ok := resp.(*message.RequestError)
	if !ok || re.Code != message.RequestDoesNotExist || re.RetryInterval != 7 || re.Reason != "nope" {
		t.Fatalf("response %+v", resp)
	}
	if err := recv(t, ch.closes, "close"); err != nil {
		t.Errorf("a rejected request ends cleanly: %v", err)
	}
	if _, err := client.Open(context.Background(), &message.Namespace{}, nil, nil); !errors.Is(err, session.ErrNotRequest) {
		t.Error("only request messages open requests")
	}
	if err := r.Send(&message.RequestUpdate{}); !errors.Is(err, session.ErrNotAllowed) {
		t.Error("REQUEST_UPDATE goes through Update")
	}
}

func TestRequestUpdates(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{
		server: func(o *session.Options) { o.Setup.MaxRequestUpdates = 2 },
	})
	r := subscribe(t, client, ch, "video")
	in := recv(t, sh.requests, "server request")
	if err := in.Send(&message.SubscribeOK{TrackAlias: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	var p message.Params
	p.SetSubscriberPriority(7)
	id1, err := r.Update(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Update(p); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Update(p); !errors.Is(err, session.ErrTooManyUpdates) {
		t.Errorf("the server allows two outstanding updates: %v", err)
	}
	if id1 != 2 {
		t.Errorf("a client's second request ID is 2, got %d", id1)
	}
	for range 2 {
		upd := recv(t, sh.messages, "update").(*message.RequestUpdate)
		if upd.Params.SubscriberPriority != 7 {
			t.Errorf("update %+v", upd)
		}
	}
	if err = in.Send(&message.RequestOK{}); err != nil {
		t.Fatal(err)
	}
	if err = in.Send(&message.RequestError{Code: message.RequestInvalidRange}); err != nil {
		t.Fatal(err)
	}
	if err = in.Send(&message.RequestOK{}); !errors.Is(err, session.ErrNotAllowed) {
		t.Errorf("an error answers every outstanding update: %v", err)
	}
	if recv(t, ch.messages, "ok").Kind() != message.KindRequestOK || recv(t, ch.messages, "err").Kind() != message.KindRequestError {
		t.Error("answers reach the subscriber in order")
	}
	if err = in.Send(&message.GoAway{}); err != nil {
		t.Fatal(err)
	}
	if err = in.Send(&message.GoAway{}); !errors.Is(err, session.ErrNotAllowed) {
		t.Error("one GOAWAY per request stream")
	}
	if recv(t, ch.messages, "goaway").Kind() != message.KindGoAway {
		t.Error("request GOAWAY reaches the subscriber")
	}
}

func TestFetchAndFills(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{})
	var fp message.Params
	fp.SetGroupOrder(message.GroupOrderDescending)
	f, err := client.Open(context.Background(), &message.Fetch{Track: track("video", "live"), Params: fp}, ch, ch)
	if err != nil {
		t.Fatal(err)
	}
	in := recv(t, sh.requests, "fetch")
	if err = in.Send(&message.FetchOK{EndLocation: wire.Location{Group: 1}}); err != nil {
		t.Fatal(err)
	}
	out, err := in.OpenFetch(context.Background(), in.RequestID(), message.GroupOrderDescending, session.Urgency(128, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []message.FetchObject{
		{Group: 5, Object: 0, Priority: 1, PayloadLen: 2},
		{EndOfRange: message.RangeEndNonExistent, Group: 3, Object: 0},
		{Group: 2, Object: 0, Priority: 1, PayloadLen: 1},
	} {
		if err = out.WriteObject(&o); err != nil {
			t.Fatal(err)
		}
		if o.EndOfRange == message.RangeEndNone {
			if _, err = out.Write(make([]byte, o.PayloadLen)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	entries := recv(t, ch.fetches, "fetch entries")
	if len(entries) != 3 || entries[0].o.Group != 5 || entries[1].o.EndOfRange != message.RangeEndNonExistent ||
		entries[2].o.Group != 2 || len(entries[0].payload) != 2 {
		t.Fatalf("entries %+v", entries)
	}
	if _, err = f.Response(context.Background()); err != nil {
		t.Fatal(err)
	}

	// a SUBSCRIBE with FILL_PARAMETERS gets a fill stream under its own Request ID, and an update's fill
	// under the update's ID
	fill := &message.Params{}
	fill.SetLocationFilter(message.LocationFilter{Type: message.FilterRelativeStart, StartGroup: 1})
	var sp message.Params
	sp.SetFill(fill)
	sub, err := client.Open(context.Background(), &message.Subscribe{Track: track("audio", "live"), Params: sp}, ch, ch)
	if err != nil {
		t.Fatal(err)
	}
	sin := recv(t, sh.requests, "subscribe")
	if err = sin.Send(&message.SubscribeOK{TrackAlias: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err = sub.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	sendFill := func(id uint64) {
		fs, err := sin.OpenFetch(context.Background(), id, message.GroupOrderAscending, 3)
		if err != nil {
			t.Fatal(err)
		}
		if err = fs.WriteObject(&message.FetchObject{Group: 9, Priority: 2}); err != nil {
			t.Fatal(err)
		}
		_ = fs.Close()
		if got := recv(t, ch.fetches, "fill"); len(got) != 1 || got[0].o.Group != 9 {
			t.Fatalf("fill %+v", got)
		}
	}
	sendFill(sub.RequestID())
	var up message.Params
	up.SetFill(&message.Params{})
	uid, err := sub.Update(up)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, sh.messages, "update")
	sendFill(uid)
	if sin.StreamCount() != 2 {
		t.Errorf("fill streams count toward the subscription's streams: %d", sin.StreamCount())
	}
}

func TestPublishFromServer(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	accepted := make(chan struct{})
	ch.onRequest = func(r *session.Request) {
		<-accepted
		if err := r.Send(&message.RequestOK{}); err != nil {
			t.Error(err)
		}
	}
	_, server := pair(t, sh, ch, pairOpts{})
	alias := server.NewTrackAlias()
	pub, err := server.Open(context.Background(), &message.Publish{Track: track("cam", "live"), TrackAlias: alias}, sh, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pub.RequestID() != 1 {
		t.Errorf("a server's first request ID is 1, got %d", pub.RequestID())
	}
	// objects may lead the subscriber's answer; they are held until it accepts
	out, err := pub.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 1, DefaultPriority: true}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = out.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	if err = pub.SendDatagram(&message.Datagram{Group: 1, Object: 1, Payload: []byte("dg")}); err != nil {
		t.Fatal(err)
	}
	close(accepted)
	if resp, err := pub.Response(context.Background()); err != nil || resp.Kind() != message.KindRequestOK {
		t.Fatalf("response %v %v", resp, err)
	}
	if objs := recv(t, ch.subgroups, "objects"); len(objs) != 1 || objs[0].payload != "x" {
		t.Fatalf("objects %+v", objs)
	}
	in := recv(t, ch.requests, "publish")
	var p message.Params
	p.SetForward(false)
	if _, err = in.Update(p); err != nil {
		t.Fatal(err)
	}
	recv(t, sh.messages, "update at the publisher")
	if err = pub.Send(&message.RequestOK{}); err != nil {
		t.Fatal(err)
	}
	if recv(t, ch.messages, "answer").Kind() != message.KindRequestOK {
		t.Error("the publisher answers the subscriber's update")
	}
	if err = pub.SendDatagram(&message.Datagram{Group: 2, Object: 0, Payload: []byte("late")}); err != nil {
		t.Fatal(err)
	}
	// a datagram that overtakes the PUBLISH naming its alias is dropped, so the early one may be missing
	d := recv(t, ch.datagrams, "datagram")
	if string(d.Payload) == "dg" {
		d = recv(t, ch.datagrams, "datagram")
	}
	if d.Group != 2 || string(d.Payload) != "late" {
		t.Errorf("datagram %+v", d)
	}
}
