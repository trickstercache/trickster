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
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

func subgroupBytes(t *testing.T, c *codec.Codec, alias uint64, payloads ...string) []byte {
	t.Helper()
	h := message.SubgroupHeader{TrackAlias: alias, Group: 1, DefaultPriority: true}
	b := c.AppendSubgroupHeader(nil, &h)
	w := c.NewSubgroupWriter(h)
	for i, p := range payloads {
		var err error
		if b, err = w.AppendObject(b, &message.SubgroupObject{ID: uint64(i), PayloadLen: uint64(len(p))}); err != nil {
			t.Fatal(err)
		}
		b = append(b, p...)
	}
	return b
}

func TestUnknownAliasRouting(t *testing.T) {
	long := func(o *session.Options) { o.UnknownAliasWait = 10 * time.Second }

	t.Run("parked until its alias is known", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, long)
		h := newRecorder()
		if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
			t.Fatal(err)
		}
		st, _, _ := p.acceptBidi()
		_ = p.openUni(subgroupBytes(t, p.c, 5, "early")).Close()
		time.Sleep(20 * time.Millisecond)
		p.write(st, &message.SubscribeOK{TrackAlias: 5})
		if objs := recv(t, h.subgroups, "objects"); len(objs) != 1 || objs[0].payload != "early" {
			t.Errorf("objects %+v", objs)
		}
	})

	t.Run("never named", func(t *testing.T) {
		_, p := rawPeer(t, session.RoleClient, nil)
		st := p.openUni(subgroupBytes(t, p.c, 9, "x"))
		if code, ok := stopped(t, st, wait); !ok || code != uint64(message.ResetCancelled) {
			t.Errorf("stopped %v with %#x", ok, code)
		}
	})

	t.Run("parking is bounded", func(t *testing.T) {
		_, p := rawPeer(t, session.RoleClient, func(o *session.Options) {
			o.UnknownAliasWait, o.MaxParked = 10*time.Second, 1
		})
		a, b := p.openUni(subgroupBytes(t, p.c, 9, "x")), p.openUni(subgroupBytes(t, p.c, 10, "y"))
		select {
		case <-a.Context().Done():
		case <-b.Context().Done():
		case <-time.After(wait):
			t.Fatal("a stream beyond the parking limit is stopped at once")
		}
	})

	t.Run("ended subscription", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, long)
		h := newRecorder()
		if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
			t.Fatal(err)
		}
		st, _, _ := p.acceptBidi()
		p.write(st, &message.SubscribeOK{TrackAlias: 3})
		p.write(st, &message.PublishDone{Code: message.DoneTrackEnded})
		_ = st.Close()
		if err := recv(t, h.closes, "close"); err != nil {
			t.Fatalf("subscription ended with %v", err)
		}
		late := p.openUni(subgroupBytes(t, p.c, 3, "late"))
		if _, ok := stopped(t, late, time.Second); !ok {
			t.Error("objects for an ended subscription are refused without waiting")
		}
	})

	t.Run("refused fetch", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, nil)
		h := newRecorder()
		f, err := s.Open(context.Background(), &message.Fetch{Track: track("v", "ns")}, h, h)
		if err != nil {
			t.Fatal(err)
		}
		st, _, _ := p.acceptBidi()
		p.write(st, &message.RequestError{Code: message.RequestDoesNotExist})
		_ = st.Close()
		if err = recv(t, h.closes, "close"); err != nil {
			t.Fatalf("a refused fetch ends cleanly: %v", err)
		}
		late := p.openUni(p.c.AppendFetchHeader(nil, f.RequestID()))
		if _, ok := stopped(t, late, wait); !ok {
			t.Error("a refused fetch keeps no route for its stream")
		}
		select {
		case <-h.fetches:
			t.Error("a stream for a refused fetch reached the handler")
		default:
		}
	})

	t.Run("unknown fetch", func(t *testing.T) {
		_, p := rawPeer(t, session.RoleClient, nil)
		st := p.openUni(p.c.AppendFetchHeader(nil, 99))
		if _, ok := stopped(t, st, wait); !ok {
			t.Error("a fetch stream for no request is stopped")
		}
	})

	t.Run("padding stream", func(t *testing.T) {
		_, p := rawPeer(t, session.RoleClient, nil)
		st := p.openUni(wire.AppendVarint(nil, codec.StreamTypePadding))
		if _, ok := stopped(t, st, wait); !ok {
			t.Error("a padding stream is stopped")
		}
	})
}

func TestStreamPriorities(t *testing.T) {
	h := newRecorder()
	s, p := rawPeer(t, session.RoleServer, func(o *session.Options) { o.Handler = h })
	_, _ = p.openBidi(&message.Subscribe{RequestID: 0, Track: track("v", "ns")})
	in := recv(t, h.requests, "request")
	if _, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{}, 3, false); !errors.Is(err, session.ErrNoTrackAlias) {
		t.Errorf("objects need the alias SUBSCRIBE_OK assigns: %v", err)
	}
	if err := in.Send(&message.SubscribeOK{TrackAlias: s.NewTrackAlias()}); err != nil {
		t.Fatal(err)
	}
	out, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 1}, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	rs := p.acceptUni()
	if u, incr, set := rs.Priority(); !set || u != 4 || !incr || !rs.ReliableBoundary() {
		t.Errorf("subgroup priority %d %v %v, boundary %v", u, incr, set, rs.ReliableBoundary())
	}
	if _, err = in.OpenFetch(context.Background(), 0, message.GroupOrderAscending, 5); err != nil {
		t.Fatal(err)
	}
	rs = p.acceptUni()
	if u, incr, set := rs.Priority(); !set || u != 5 || incr {
		t.Errorf("fetch priority %d %v %v", u, incr, set)
	}
	if in.StreamCount() != 2 {
		t.Errorf("stream count %d", in.StreamCount())
	}
	_ = out.Close()

	// the session's own request streams carry control urgency
	c := newRecorder()
	if _, err = s.Open(context.Background(), &message.TrackStatus{Track: track("v", "ns")}, c, nil); err != nil {
		t.Fatal(err)
	}
	st, _, _ := p.acceptBidi()
	if u, _, set := st.(*memconn.Stream).Priority(); !set || u != session.UrgencyControl {
		t.Errorf("request priority %d %v", u, set)
	}
}

func TestOutgoingWriters(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, server := pair(t, sh, ch, pairOpts{})
	subscribe(t, client, ch, "video")
	in := recv(t, sh.requests, "request")
	if err := in.Send(&message.SubscribeOK{TrackAlias: server.NewTrackAlias()}); err != nil {
		t.Fatal(err)
	}
	out, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 1, DefaultPriority: true}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 3}); err != nil {
		t.Fatal(err)
	}
	if err = out.WriteObject(&message.SubgroupObject{ID: 1}); !errors.Is(err, session.ErrPayloadPending) {
		t.Errorf("a header before the payload is done: %v", err)
	}
	if err = out.Close(); !errors.Is(err, session.ErrPayloadPending) {
		t.Errorf("a close before the payload is done: %v", err)
	}
	if _, err = out.Write([]byte("four")); !errors.Is(err, session.ErrPayloadOverrun) {
		t.Errorf("an overrun: %v", err)
	}
	if _, err = out.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err = out.WriteObject(&message.SubgroupObject{ID: 0}); err == nil {
		t.Error("object IDs must increase on a subgroup")
	}
	if err = out.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Error(err)
	}
	if err = out.CloseWithTimeout(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.Context().Done():
	case <-time.After(wait):
		t.Error("a closed stream's context ends")
	}
	if objs := recv(t, ch.subgroups, "objects"); len(objs) != 1 || objs[0].payload != "abc" {
		t.Errorf("objects %+v", objs)
	}

	reset, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 2, DefaultPriority: true}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	reset.Reset(message.ResetDeliveryTimeout)
	recv(t, ch.subgroups, "reset subgroup")

	if err = in.SendDatagram(&message.Datagram{Group: 3, Payload: make([]byte, 2000)}); err == nil {
		t.Error("a datagram beyond the path's size is refused")
	}
	fetch, err := client.Open(context.Background(), &message.Fetch{Track: track("video", "live")}, ch, ch)
	if err != nil {
		t.Fatal(err)
	}
	if err = fetch.SendDatagram(&message.Datagram{}); !errors.Is(err, session.ErrNoTrackAlias) {
		t.Errorf("a fetch has no alias: %v", err)
	}
	fin := recv(t, sh.requests, "fetch")
	fo, err := fin.OpenFetch(context.Background(), fin.RequestID(), message.GroupOrderAscending, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err = fo.WriteObject(&message.FetchObject{Group: 1, PayloadLen: 2}); err != nil {
		t.Fatal(err)
	}
	if err = fo.WriteObject(&message.FetchObject{Group: 2}); !errors.Is(err, session.ErrPayloadPending) {
		t.Errorf("a fetch entry before the payload is done: %v", err)
	}
	fo.Reset(message.ResetCancelled)
	recv(t, ch.fetches, "reset fetch")

	fin.Cancel(message.ResetCancelled)
	if _, err = fin.OpenFetch(context.Background(), fin.RequestID(), message.GroupOrderAscending, 3); !errors.Is(err, session.ErrRequestEnded) {
		t.Errorf("a fetch stream for an ended request: %v", err)
	}
	in.Cancel(message.ResetCancelled)
	if _, err = in.OpenSubgroup(context.Background(), message.SubgroupHeader{}, 3, false); !errors.Is(err, session.ErrRequestEnded) {
		t.Errorf("a subgroup for an ended request: %v", err)
	}
}

func TestCancelAndClose(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{})
	r := subscribe(t, client, ch, "video")
	recv(t, sh.requests, "request")
	r.Cancel(message.ResetCancelled)
	if se, ok := errors.AsType[*session.StreamError](recv(t, ch.closes, "local close")); !ok || se.Remote ||
		se.Code != uint64(message.ResetCancelled) || se.Error() == "" {
		t.Errorf("the canceling side sees its own reset: %v", se)
	}
	if se, ok := errors.AsType[*session.StreamError](recv(t, sh.closes, "remote close")); !ok || !se.Remote {
		t.Errorf("the peer sees a remote reset: %v", se)
	}
	if _, err := r.Response(context.Background()); err == nil {
		t.Error("a canceled request has no response")
	}

	r2 := subscribe(t, client, ch, "audio")
	recv(t, sh.requests, "request")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r2.Response(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled wait: %v", err)
	}
	if err := client.Close(message.CodeGoAwayTimeout, "bye"); err != nil {
		t.Fatal(err)
	}
	if ce, ok := errors.AsType[*session.CloseError](recv(t, ch.closes, "client request")); !ok || ce.Remote ||
		ce.Code != uint64(message.CodeGoAwayTimeout) {
		t.Errorf("the closing side's requests end with its close: %v", ce)
	}
	if ce, ok := errors.AsType[*session.CloseError](recv(t, sh.closes, "server request")); !ok || !ce.Remote ||
		ce.Code != uint64(message.CodeGoAwayTimeout) {
		t.Errorf("the peer's requests end with a remote close: %v", ce)
	}
	if _, err := client.Open(context.Background(), &message.Subscribe{}, nil, nil); !errors.Is(err, session.ErrSessionClosed) {
		t.Errorf("open after close: %v", err)
	}
	if _, err := r2.Response(context.Background()); err == nil {
		t.Error("a request ended by the session's close has no response")
	}
}

func TestResponderEndsEarly(t *testing.T) {
	t.Run("no response", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, nil)
		h := newRecorder()
		r, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h)
		if err != nil {
			t.Fatal(err)
		}
		st, _, _ := p.acceptBidi()
		_ = st.Close()
		if _, err = r.Response(context.Background()); !errors.Is(err, session.ErrNoResponse) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("subscription ends without publish done", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, nil)
		h := newRecorder()
		if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
			t.Fatal(err)
		}
		st, _, _ := p.acceptBidi()
		p.write(st, &message.SubscribeOK{TrackAlias: 1})
		_ = st.Close()
		if err := recv(t, h.closes, "close"); !errors.Is(err, session.ErrNoPublishDone) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("stop sending", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleClient, nil)
		h := newRecorder()
		if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
			t.Fatal(err)
		}
		st, _, _ := p.acceptBidi()
		st.CancelRead(uint64(message.ResetExcessiveLoad))
		se, ok := errors.AsType[*session.StreamError](recv(t, h.closes, "close"))
		if !ok || !se.Remote || se.Code != uint64(message.ResetExcessiveLoad) {
			t.Errorf("got %v", se)
		}
	})
}

func TestSessionGoAway(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, server := pair(t, sh, ch, pairOpts{})
	if err := server.GoAway("moqt://next.example.com/live", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := server.GoAway("", 0); !errors.Is(err, session.ErrGoAwaySent) {
		t.Errorf("second goaway: %v", err)
	}
	if g := recv(t, ch.goaways, "goaway"); g.NewSessionURI != "moqt://next.example.com/live" || g.Timeout != 5000 {
		t.Errorf("goaway %+v", g)
	}
	// a client never names a URI, so one it is given is dropped rather than violating the protocol
	if err := client.GoAway("moqt://ignored", 0); err != nil {
		t.Fatal(err)
	}
	if g := recv(t, sh.goaways, "goaway"); g.NewSessionURI != "" {
		t.Errorf("goaway %+v", g)
	}
	if !client.GoAwayReceived() || !server.GoAwayReceived() || client.Err() != nil || server.Err() != nil {
		t.Error("both sides took the other's goaway")
	}
}

func TestRequestFlows(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{})

	t.Run("track status", func(t *testing.T) {
		r, err := client.Open(context.Background(), &message.TrackStatus{Track: track("v", "ns")}, ch, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := recv(t, sh.requests, "request")
		if err = in.Send(&message.RequestOK{}); err != nil {
			t.Fatal(err)
		}
		if err = in.Send(&message.RequestOK{}); !errors.Is(err, session.ErrNotAllowed) {
			t.Errorf("TRACK_STATUS takes one answer: %v", err)
		}
		_ = in.CloseSend()
		if resp, err := r.Response(context.Background()); err != nil || resp.Kind() != message.KindRequestOK {
			t.Fatalf("response %v %v", resp, err)
		}
		if err = recv(t, ch.closes, "client close"); err != nil {
			t.Error(err)
		}
		if err = recv(t, sh.closes, "server close"); err != nil {
			t.Error(err)
		}
	})

	t.Run("namespaces", func(t *testing.T) {
		r, err := client.Open(context.Background(), &message.SubscribeNamespace{Prefix: ns("live")}, ch, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := recv(t, sh.requests, "request")
		for _, m := range []message.Message{
			&message.RequestOK{}, &message.Namespace{Suffix: ns("a")},
			&message.NamespaceDone{Suffix: ns("a")},
		} {
			if err = in.Send(m); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = r.Response(context.Background()); err != nil {
			t.Fatal(err)
		}
		if recv(t, ch.messages, "namespace").Kind() != message.KindNamespace ||
			recv(t, ch.messages, "done").Kind() != message.KindNamespaceDone {
			t.Error("namespace messages reach the subscriber")
		}
		r.Cancel(message.ResetCancelled)
		recv(t, ch.closes, "close")
		recv(t, sh.closes, "close")
	})

	t.Run("publish namespace", func(t *testing.T) {
		r, err := client.Open(context.Background(), &message.PublishNamespace{Prefix: ns("live")}, ch, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := recv(t, sh.requests, "request")
		if err = in.Send(&message.RequestOK{}); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Response(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Update(message.Params{}); err != nil {
			t.Fatal(err)
		}
		recv(t, sh.messages, "update")
		if err = in.Send(&message.RequestOK{}); err != nil {
			t.Fatal(err)
		}
		recv(t, ch.messages, "answer")
		_ = r.CloseSend()
		_ = in.CloseSend()
		if err = recv(t, ch.closes, "close"); err != nil {
			t.Error(err)
		}
		recv(t, sh.closes, "close")
	})

	t.Run("subscribe tracks", func(t *testing.T) {
		r, err := client.Open(context.Background(), &message.SubscribeTracks{Prefix: ns("live")}, ch, nil)
		if err != nil {
			t.Fatal(err)
		}
		in := recv(t, sh.requests, "request")
		if err = in.Send(&message.RequestOK{}); err != nil {
			t.Fatal(err)
		}
		if err = in.Send(&message.PublishSkipped{Suffix: ns("a"), TrackName: []byte("v")}); err != nil {
			t.Fatal(err)
		}
		if _, err = r.Response(context.Background()); err != nil {
			t.Fatal(err)
		}
		if recv(t, ch.messages, "skipped").Kind() != message.KindPublishSkipped {
			t.Error("PUBLISH_SKIPPED reaches the subscriber")
		}
	})
}

func TestFillFollowsTrackOrder(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{})
	var sp message.Params
	sp.SetFill(&message.Params{})
	r, err := client.Open(context.Background(), &message.Subscribe{Track: track("v", "ns"), Params: sp}, ch, ch)
	if err != nil {
		t.Fatal(err)
	}
	in := recv(t, sh.requests, "request")
	props, err := wire.AppendKVs(nil, []wire.KV{{Type: message.PropDefaultGroupOrder, Uint: uint64(message.GroupOrderDescending)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = in.Send(&message.SubscribeOK{TrackAlias: 2, TrackProperties: props}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := in.OpenFetch(context.Background(), r.RequestID(), message.GroupOrderDescending, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []uint64{9, 7, 4} {
		if err = out.WriteObject(&message.FetchObject{Group: g, Priority: 1}); err != nil {
			t.Fatal(err)
		}
	}
	_ = out.Close()
	got := recv(t, ch.fetches, "fill")
	if len(got) != 3 || got[0].o.Group != 9 || got[1].o.Group != 7 || got[2].o.Group != 4 {
		t.Errorf("a fill in the track's descending order decodes as sent: %+v", got)
	}
}
