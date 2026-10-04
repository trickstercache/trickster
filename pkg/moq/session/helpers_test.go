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
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const wait = 5 * time.Second

func c22(t testing.TB) *codec.Codec {
	t.Helper()
	c, ok := codec.ForDraft(22)
	if !ok {
		t.Fatal("draft 22 missing")
	}
	return c
}

func ns(fields ...string) wire.Namespace {
	out := make(wire.Namespace, len(fields))
	for i, f := range fields {
		out[i] = []byte(f)
	}
	return out
}

func track(name string, fields ...string) message.FullTrackName {
	return message.FullTrackName{Namespace: ns(fields...), Name: []byte(name)}
}

type recorder struct {
	onRequest func(r *session.Request)
	requests  chan *session.Request
	messages  chan message.Message
	closes    chan error
	subgroups chan []obj
	fetches   chan []fetched
	datagrams chan *message.Datagram
	goaways   chan *message.GoAway
}

type obj struct {
	id      uint64
	status  message.ObjectStatus
	payload string
}

type fetched struct {
	o       message.FetchObject
	payload string
}

func newRecorder() *recorder {
	return &recorder{
		requests: make(chan *session.Request, 16), messages: make(chan message.Message, 64),
		closes: make(chan error, 16), subgroups: make(chan []obj, 16), fetches: make(chan []fetched, 16),
		datagrams: make(chan *message.Datagram, 64), goaways: make(chan *message.GoAway, 4),
	}
}

func (h *recorder) HandleRequest(r *session.Request) {
	r.SetHandlers(h, h)
	if h.onRequest != nil {
		h.onRequest(r)
	}
	h.requests <- r
}

func (h *recorder) HandleGoAway(_ *session.Session, m *message.GoAway)  { h.goaways <- m }
func (h *recorder) HandleMessage(_ *session.Request, m message.Message) { h.messages <- m }
func (h *recorder) HandleClose(_ *session.Request, err error)           { h.closes <- err }
func (h *recorder) HandleDatagram(_ *session.Request, d *message.Datagram) {
	cp := *d
	cp.Payload = append([]byte(nil), d.Payload...)
	h.datagrams <- &cp
}

func (h *recorder) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	var objs []obj
	for {
		o, err := in.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(in)
		objs = append(objs, obj{id: o.ID, status: o.Status, payload: string(b)})
	}
	h.subgroups <- objs
}

func (h *recorder) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	var out []fetched
	for {
		o, err := in.Next()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(in)
		out = append(out, fetched{o: o, payload: string(b)})
	}
	h.fetches <- out
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(wait):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

type pairOpts struct {
	server, client func(*session.Options)
}

func pair(t *testing.T, sh, ch *recorder, o pairOpts) (*session.Session, *session.Session) {
	// starts a client and a server session over an in-memory connection
	t.Helper()
	cc, sc := memconn.Pair()
	so := session.Options{Role: session.RoleServer, Codec: c22(t), Setup: message.Setup{Implementation: "server"}}
	co := session.Options{Role: session.RoleClient, Codec: c22(t), Setup: message.Setup{Implementation: "client"}}
	if sh != nil {
		so.Handler = sh
	}
	if ch != nil {
		co.Handler = ch
	}
	if o.server != nil {
		o.server(&so)
	}
	if o.client != nil {
		o.client(&co)
	}
	server, err := session.New(sc, so)
	if err != nil {
		t.Fatal(err)
	}
	client, err := session.New(cc, co)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err := recv(t, errs, "session start"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = client.Close(message.CodeNoError, "")
		_ = server.Close(message.CodeNoError, "")
	})
	return client, server
}

type raw struct {
	t    *testing.T
	conn *memconn.Conn
	c    *codec.Codec
	ctrl session.SendStream
}

func newRaw(t *testing.T, conn *memconn.Conn) *raw {
	return &raw{t: t, conn: conn, c: c22(t)}
}

func (r *raw) encode(m message.Message) []byte {
	r.t.Helper()
	b, err := r.c.AppendMessage(nil, m)
	if err != nil {
		r.t.Fatal(err)
	}
	return b
}

func (r *raw) setup(m *message.Setup) {
	r.t.Helper()
	st, err := r.conn.OpenUniStreamSync(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	r.ctrl = st
	r.write(st, m)
}

func (r *raw) write(w io.Writer, m message.Message) {
	r.t.Helper()
	if _, err := w.Write(r.encode(m)); err != nil {
		r.t.Fatal(err)
	}
}

func (r *raw) read(rd *bufio.Reader) message.Message {
	r.t.Helper()
	typ, body, err := wire.ReadMessage(rd, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	m, err := r.c.ParseMessage(typ, body)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *raw) acceptControl() *message.Setup {
	r.t.Helper()
	rs, err := r.conn.AcceptUniStream(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	br := bufio.NewReader(rs)
	if typ, err := wire.ReadVarintFrom(br); err != nil || typ != codec.StreamTypeControl {
		r.t.Fatalf("control stream type %#x %v", typ, err)
	}
	var lb [2]byte
	if _, err := io.ReadFull(br, lb[:]); err != nil {
		r.t.Fatal(err)
	}
	body := make([]byte, int(lb[0])<<8|int(lb[1]))
	if _, err := io.ReadFull(br, body); err != nil {
		r.t.Fatal(err)
	}
	m, err := r.c.ParseMessage(codec.StreamTypeControl, body)
	if err != nil {
		r.t.Fatal(err)
	}
	return m.(*message.Setup)
}

func (r *raw) openBidi(m message.Message) (session.Stream, *bufio.Reader) {
	r.t.Helper()
	st, err := r.conn.OpenStreamSync(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(st, m)
	return st, bufio.NewReader(st)
}

func (r *raw) acceptBidi() (session.Stream, *bufio.Reader, message.Message) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	st, err := r.conn.AcceptStream(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	br := bufio.NewReader(st)
	return st, br, r.read(br)
}

func (r *raw) acceptUni() *memconn.ReceiveStream {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	rs, err := r.conn.AcceptUniStream(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return rs.(*memconn.ReceiveStream)
}

func (r *raw) openUni(b []byte) session.SendStream {
	r.t.Helper()
	st, err := r.conn.OpenUniStreamSync(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err = st.Write(b); err != nil {
		r.t.Fatal(err)
	}
	return st
}

func stopped(t *testing.T, st session.SendStream, within time.Duration) (uint64, bool) {
	// waits for the session to stop st, returning the STOP_SENDING code
	t.Helper()
	select {
	case <-st.Context().Done():
	case <-time.After(within):
		return 0, false
	}
	se, ok := errors.AsType[*session.StreamError](context.Cause(st.Context()))
	if !ok || !se.Remote {
		t.Fatalf("stream ended with %v", context.Cause(st.Context()))
	}
	return se.Code, true
}

func rawPeer(t *testing.T, role session.Role, o func(*session.Options)) (*session.Session, *raw) {
	t.Helper()
	return rawPeerCap(t, role, 0, o)
}

func rawPeerCap(t *testing.T, role session.Role, capacity int, o func(*session.Options)) (*session.Session, *raw) {
	// starts a session against a hand-driven peer, which sends what a well-behaved session never would; capacity
	// bounds what each of the session's streams buffers before its writes block
	t.Helper()
	sc, pc := memconn.Pair()
	sc.Capacity = capacity
	opts := session.Options{Role: role, Codec: c22(t), Handler: newRecorder(), UnknownAliasWait: 200 * time.Millisecond}
	if o != nil {
		o(&opts)
	}
	s, err := session.New(sc, opts)
	if err != nil {
		t.Fatal(err)
	}
	p := newRaw(t, pc)
	p.c = opts.Codec
	started := make(chan error, 1)
	go func() { started <- s.Start(context.Background()) }()
	p.setup(&message.Setup{Implementation: "raw"})
	p.acceptControl()
	if err := recv(t, started, "start"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(message.CodeNoError, "") })
	return s, p
}

func closeCode(t *testing.T, s *session.Session) uint64 {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(wait):
		t.Fatal("session did not close")
	}
	ce, ok := errors.AsType[*session.CloseError](s.Err())
	if !ok {
		t.Fatalf("session ended with %v", s.Err())
	}
	return ce.Code
}
