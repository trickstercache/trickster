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
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
)

func isRemoteReset(err error, code message.ResetCode) bool {
	se, ok := errors.AsType[*session.StreamError](err)
	return ok && se.Remote && se.Code == uint64(code)
}

func quicPair(t *testing.T) (session.Conn, session.Conn) {
	t.Helper()
	key, cert, err := testtls.GetTestKeyAndCertWithNames("localhost")
	if err != nil {
		t.Fatal(err)
	}
	kp, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	alpn := c22(t).ALPN()
	qc := &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true}
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{kp}, NextProtos: []string{alpn}, MinVersion: tls.VersionTLS13,
	}, qc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan *quic.Conn, 1)
	go func() {
		c, err := ln.Accept(context.Background())
		if err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cc, err := quic.DialAddr(ctx, ln.Addr().String(), &tls.Config{
		InsecureSkipVerify: true, NextProtos: []string{alpn}, MinVersion: tls.VersionTLS13, // #nosec G402 -- loopback test
	}, qc)
	if err != nil {
		t.Fatal(err)
	}
	sc := recv(t, accepted, "accept")
	return session.FromQUIC(cc), session.FromQUIC(sc)
}

func TestQUICAdapter(t *testing.T) {
	cconn, sconn := quicPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	st, err := cconn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(wait)
	if st.SetReadDeadline(deadline) != nil || st.SetWriteDeadline(deadline) != nil {
		t.Error("stream deadlines")
	}
	st.SetReliableBoundary()
	if _, err = st.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	peer, err := sconn.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	us, err := sconn.OpenUniStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = us.Write([]byte("uni")); err != nil {
		t.Fatal(err)
	}
	urs, err := cconn.AcceptUniStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if urs.SetReadDeadline(deadline) != nil {
		t.Error("receive stream deadline")
	}
	peer.CancelRead(uint64(message.ResetCancelled))
	if _, err = st.Write(make([]byte, 1<<20)); !isRemoteReset(err, message.ResetCancelled) {
		t.Errorf("a write into a stopped stream: %v", err)
	}
	_ = cconn.CloseWithError(uint64(message.CodeNoError), "")
	if _, err = cconn.OpenStreamSync(ctx); err == nil {
		t.Error("no streams open on a closed connection")
	}
	if _, err = cconn.OpenUniStreamSync(ctx); err == nil {
		t.Error("no streams open on a closed connection")
	}
}

func TestOverQUIC(t *testing.T) {
	cconn, sconn := quicPair(t)
	overTransport(t, cconn, sconn, false)
}

func overTransport(t *testing.T, cconn, sconn session.Conn, webTransport bool) {
	t.Helper()
	sh, ch := newRecorder(), newRecorder()
	server, err := session.New(sconn, session.Options{
		Role: session.RoleServer, Codec: c22(t), Handler: sh, WebTransport: webTransport,
	})
	if err != nil {
		t.Fatal(err)
	}
	// over WebTransport the CONNECT request names the path, and SETUP may not
	setup := message.Setup{Path: "/live", HasPath: true}
	if webTransport {
		setup = message.Setup{}
	}
	client, err := session.New(cconn, session.Options{
		Role: session.RoleClient, Codec: c22(t), Handler: ch, Setup: setup, WebTransport: webTransport,
	})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err = recv(t, errs, "start"); err != nil {
			t.Fatal(err)
		}
	}

	r := subscribe(t, client, ch, "video")
	in := recv(t, sh.requests, "request")
	if err = in.Send(&message.SubscribeOK{TrackAlias: server.NewTrackAlias()}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Response(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 1, DefaultPriority: true},
		session.Urgency(128, 128), false)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 64<<10)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	if err = out.SetWriteDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	if err = out.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: uint64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err = out.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = out.Close(); err != nil {
		t.Fatal(err)
	}
	if objs := recv(t, ch.subgroups, "objects"); len(objs) != 1 || objs[0].payload != string(payload) {
		t.Fatalf("got %d objects", len(objs))
	}
	select {
	case <-out.Context().Done():
	case <-time.After(wait):
		t.Error("a closed stream's context ends")
	}
	if err = in.SendDatagram(&message.Datagram{Group: 2, Payload: []byte("dg")}); err != nil {
		t.Fatal(err)
	}
	if d := recv(t, ch.datagrams, "datagram"); string(d.Payload) != "dg" {
		t.Errorf("datagram %+v", d)
	}
	reset, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 3, DefaultPriority: true}, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = reset.WriteObject(&message.SubgroupObject{ID: 0, PayloadLen: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err = reset.Write([]byte("part")); err != nil {
		t.Fatal(err)
	}
	reset.Reset(message.ResetDeliveryTimeout)
	// webtransport-go offers no reliable boundary, so a reset WebTransport stream may lose its header and arrive as
	// nothing a subscription can claim
	if !webTransport {
		if objs := recv(t, ch.subgroups, "reset objects"); len(objs) > 1 {
			t.Errorf("a reset stream delivers at most its partial object: %+v", objs)
		}
	}

	// a stream reset and a stop each surface as a *session.StreamError
	f, err := client.Open(context.Background(), &message.Fetch{Track: track("video", "live")}, ch, ch)
	if err != nil {
		t.Fatal(err)
	}
	recv(t, sh.requests, "fetch")
	f.Cancel(message.ResetCancelled)
	if err = recv(t, ch.closes, "local fetch close"); err == nil {
		t.Error("a canceled fetch ends with its reset")
	}
	if err = recv(t, sh.closes, "fetch close"); !isRemoteReset(err, message.ResetCancelled) {
		t.Errorf("fetch ended with %v", err)
	}

	if err = in.Send(&message.PublishDone{Code: message.DoneTrackEnded, StreamCount: in.StreamCount()}); err != nil {
		t.Fatal(err)
	}
	if err = in.CloseSend(); err != nil {
		t.Fatal(err)
	}
	recv(t, ch.messages, "publish done")
	if err = recv(t, ch.closes, "subscription close"); err != nil {
		t.Errorf("subscription ended with %v", err)
	}

	sub2 := subscribe(t, client, ch, "audio")
	recv(t, sh.requests, "second request")
	if err = server.Close(message.CodeGoAwayTimeout, "done"); err != nil {
		t.Fatal(err)
	}
	err = recv(t, ch.closes, "second subscription close")
	if ce, ok := errors.AsType[*session.CloseError](err); !ok || !ce.Remote || ce.Code != uint64(message.CodeGoAwayTimeout) {
		t.Errorf("subscription ended with %v", err)
	}
	if sub2.Err() == nil {
		t.Error("the session's end ends its requests")
	}
	<-client.Done()
	if ce, ok := errors.AsType[*session.CloseError](client.Err()); !ok || !ce.Remote || ce.Reason != "done" {
		t.Errorf("client ended with %v", client.Err())
	}
	// streams can report the close before the connection's context ends
	<-cconn.Context().Done()
	if ce, ok := errors.AsType[*session.CloseError](cconn.Cause()); !ok || !ce.Remote {
		t.Errorf("connection cause %v", cconn.Cause())
	}
}
