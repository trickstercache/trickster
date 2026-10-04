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
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

type canceler struct {
	*recorder
	headers chan uint64
	ids     chan uint64
}

func (c *canceler) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	c.headers <- in.Header().Group
	if _, err := in.Next(); err == nil {
		in.Cancel(message.ResetCancelled)
	}
}

func (c *canceler) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	c.ids <- in.RequestID()
	if _, err := in.Next(); err == nil {
		in.Cancel(message.ResetCancelled)
	}
}

func TestSubscriberStopsStreams(t *testing.T) {
	sh := newRecorder()
	c := &canceler{recorder: newRecorder(), headers: make(chan uint64, 1), ids: make(chan uint64, 1)}
	client, server := pair(t, sh, c.recorder, pairOpts{})
	r, err := client.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, c, c)
	if err != nil {
		t.Fatal(err)
	}
	in := recv(t, sh.requests, "request")
	if err = in.Send(&message.SubscribeOK{TrackAlias: server.NewTrackAlias()}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 8, DefaultPriority: true}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err = out.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if g := recv(t, c.headers, "header"); g != 8 {
		t.Errorf("group %d", g)
	}
	<-out.Context().Done()
	if _, err = out.Write(nil); err == nil {
		if err = out.WriteObject(&message.SubgroupObject{ID: 1}); !isRemoteReset(err, message.ResetCancelled) {
			t.Errorf("writing to a stopped stream: %v", err)
		}
	}

	f, err := client.Open(context.Background(), &message.Fetch{Track: track("v", "ns")}, c, c)
	if err != nil {
		t.Fatal(err)
	}
	fin := recv(t, sh.requests, "fetch")
	fo, err := fin.OpenFetch(context.Background(), fin.RequestID(), message.GroupOrderAscending, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err = fo.WriteObject(&message.FetchObject{Group: 1}); err != nil {
		t.Fatal(err)
	}
	if id := recv(t, c.ids, "fetch id"); id != f.RequestID() {
		t.Errorf("fetch stream answers %d, want %d", id, f.RequestID())
	}
	<-fo.Context().Done()
}

func TestTruncatedObject(t *testing.T) {
	h := newRecorder()
	s, p := rawPeer(t, session.RoleClient, func(o *session.Options) { o.Handler = h })
	if _, err := s.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, h, h); err != nil {
		t.Fatal(err)
	}
	st, _, _ := p.acceptBidi()
	p.write(st, &message.SubscribeOK{TrackAlias: 2})
	b := subgroupBytes(t, p.c, 2, "complete")
	hdr := message.SubgroupHeader{TrackAlias: 2, Group: 1, DefaultPriority: true}
	w := p.c.NewSubgroupWriter(hdr)
	_, _ = w.AppendObject(nil, &message.SubgroupObject{ID: 0, PayloadLen: 8})
	b, _ = w.AppendObject(b, &message.SubgroupObject{ID: 1, PayloadLen: 100})
	b = append(b, "short"...)
	_ = p.openUni(b).Close()
	objs := recv(t, h.subgroups, "objects")
	if len(objs) != 2 || objs[0].payload != "complete" || objs[1].payload != "short" {
		t.Errorf("a stream that ends within an object yields what arrived: %+v", objs)
	}
	if s.Err() != nil {
		t.Errorf("a truncated stream leaves the session up: %v", s.Err())
	}
}

type holder struct {
	requests chan *session.Request
}

func (h *holder) HandleRequest(r *session.Request)               { h.requests <- r }
func (h *holder) HandleGoAway(*session.Session, *message.GoAway) {}

func TestStreamForEndedRequest(t *testing.T) {
	h := &holder{requests: make(chan *session.Request, 1)}
	_, p := rawPeer(t, session.RoleClient, func(o *session.Options) { o.Handler = h })
	st, _ := p.openBidi(&message.Publish{RequestID: 1, Track: track("v", "ns"), TrackAlias: 4})
	recv(t, h.requests, "publish")
	data := p.openUni(subgroupBytes(t, p.c, 4, "x"))
	st.CancelWrite(uint64(message.ResetCancelled))
	if _, ok := stopped(t, data, wait); !ok {
		t.Error("objects of a request that ended before taking them are refused")
	}
}

func TestMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		act  func(p *raw)
		want message.SessionCode
	}{
		{"setup body", func(p *raw) {
			p.openUni(append(wire.AppendVarint(nil, codec.StreamTypeControl), 0x00, 0x02, 0xff, 0xff))
		}, message.CodeProtocolViolation},
		{"request body", func(p *raw) {
			typ, _ := p.c.MessageType(message.KindSubscribe)
			st, err := p.conn.OpenStreamSync(context.Background())
			if err != nil {
				p.t.Fatal(err)
			}
			_, _ = st.Write(append(wire.AppendVarint(nil, typ), 0x00, 0x02, 0xff, 0xff))
		}, message.CodeProtocolViolation},
		{"goaway body", func(p *raw) {
			typ, _ := p.c.MessageType(message.KindGoAway)
			_, _ = p.ctrl.Write(append(wire.AppendVarint(nil, typ), 0x00, 0x02, 0xff, 0xff))
		}, message.CodeProtocolViolation},
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
	t.Run("abandoned request stream", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, nil)
		st, err := p.conn.OpenStreamSync(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		st.CancelWrite(uint64(message.ResetCancelled))
		if _, err := st.Read(make([]byte, 1)); !isRemoteReset(err, message.ResetCancelled) {
			t.Errorf("a request stream reset before its request is answered in kind: %v", err)
		}
		if s.Err() != nil {
			t.Errorf("the session stays up: %v", s.Err())
		}
	})
	t.Run("subgroup header", func(t *testing.T) {
		s, p := rawPeer(t, session.RoleServer, nil)
		_ = p.openUni([]byte{0x10, 0x05}).Close()
		data := p.openUni(subgroupBytes(t, p.c, 6, "after"))
		data.CancelWrite(0)
		if s.Err() != nil {
			t.Errorf("a truncated header ends only its stream: %v", s.Err())
		}
	})
}

func TestOpenEdges(t *testing.T) {
	cc, _ := memconn.Pair()
	s, err := session.New(cc, session.Options{Role: session.RoleClient, Codec: c22(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.Open(ctx, &message.Subscribe{}, nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("open before the handshake with a canceled context: %v", err)
	}
	if err = s.GoAway("", 0); !errors.Is(err, session.ErrSessionClosed) {
		t.Errorf("goaway before SETUP was sent: %v", err)
	}

	ch := newRecorder()
	client, _ := pair(t, newRecorder(), ch, pairOpts{})
	tooDeep := make([]string, 40)
	for i := range tooDeep {
		tooDeep[i] = "x"
	}
	if _, err = client.Open(context.Background(), &message.Subscribe{Track: track("v", tooDeep...)}, ch, ch); err == nil {
		t.Error("a request that cannot be encoded is refused")
	}
	if err = recv(t, ch.closes, "close"); err == nil {
		t.Error("the refused request ends with an error")
	}
	_ = client.Close(message.CodeNoError, "")
	if _, err = client.Open(context.Background(), &message.Subscribe{}, nil, nil); !errors.Is(err, session.ErrSessionClosed) {
		t.Errorf("open after close: %v", err)
	}
}

func TestFillOrders(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{})
	run := func(sub, fill message.GroupOrder, groups ...uint64) {
		t.Helper()
		var sp message.Params
		fp := &message.Params{}
		if fill != message.GroupOrderUnset {
			fp.SetGroupOrder(fill)
		}
		if sub != message.GroupOrderUnset {
			sp.SetGroupOrder(sub)
		}
		sp.SetFill(fp)
		r, err := client.Open(context.Background(), &message.Subscribe{Track: track("v", "ns"), Params: sp}, ch, ch)
		if err != nil {
			t.Fatal(err)
		}
		in := recv(t, sh.requests, "request")
		order := fill
		if order == message.GroupOrderUnset {
			order = sub
		}
		out, err := in.OpenFetch(context.Background(), r.RequestID(), order, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range groups {
			if err = out.WriteObject(&message.FetchObject{Group: g, Priority: 1}); err != nil {
				t.Fatal(err)
			}
		}
		_ = out.Close()
		got := recv(t, ch.fetches, "fill")
		for i, g := range groups {
			if i >= len(got) || got[i].o.Group != g {
				t.Fatalf("fill decoded as %+v, want groups %v", got, groups)
			}
		}
		r.Cancel(message.ResetCancelled)
		recv(t, ch.closes, "close")
	}
	run(message.GroupOrderDescending, message.GroupOrderUnset, 9, 5, 1)
	run(message.GroupOrderAscending, message.GroupOrderDescending, 6, 2)
	run(message.GroupOrderUnset, message.GroupOrderUnset, 1, 3)
}

func TestRejectTwice(t *testing.T) {
	sh, ch := newRecorder(), newRecorder()
	client, _ := pair(t, sh, ch, pairOpts{})
	r := subscribe(t, client, ch, "v")
	in := recv(t, sh.requests, "request")
	if err := in.Reject(message.RequestUnauthorized, "no", 0); err != nil {
		t.Fatal(err)
	}
	if err := in.Reject(message.RequestUnauthorized, "no", 0); !errors.Is(err, session.ErrNotAllowed) &&
		!errors.Is(err, session.ErrRequestEnded) {
		t.Errorf("a second rejection: %v", err)
	}
	if resp, err := r.Response(context.Background()); err != nil || resp.Kind() != message.KindRequestError {
		t.Errorf("response %v %v", resp, err)
	}
	recv(t, ch.closes, "close")
	if resp, err := r.Response(context.Background()); err != nil || resp == nil {
		t.Errorf("the response outlives the request: %v %v", resp, err)
	}
	if _, err := io.ReadAll(io.LimitReader(nil, 0)); err != nil {
		t.Fatal(err)
	}
}
