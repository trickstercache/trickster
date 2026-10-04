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
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func wtPair(t *testing.T) (session.Conn, session.Conn) {
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
	accepted := make(chan *webtransport.Session, 1)
	srv := &webtransport.Server{
		H3: &http3.Server{
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{kp}, NextProtos: []string{http3.NextProtoH3}, MinVersion: tls.VersionTLS13,
			},
			QUICConfig: qc,
		},
		ApplicationProtocols: []string{alpn},
	}
	srv.H3.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := srv.Upgrade(w, r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		accepted <- s
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(pc) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = pc.Close()
	})
	tr := &webtransport.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}, // #nosec G402 -- loopback test
			MinVersion: tls.VersionTLS13,
		},
		QUICConfig: qc, ApplicationProtocols: []string{alpn},
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	_, cs, err := tr.Dial(ctx, "https://"+pc.LocalAddr().String()+"/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs.SessionState().ApplicationProtocol != alpn {
		t.Fatalf("negotiated %q", cs.SessionState().ApplicationProtocol)
	}
	return session.FromWebTransport(cs), session.FromWebTransport(recv(t, accepted, "session"))
}

func TestOverWebTransport(t *testing.T) {
	cconn, sconn := wtPair(t)
	overTransport(t, cconn, sconn, true)
}

func TestWebTransportAdapter(t *testing.T) {
	cconn, sconn := wtPair(t)
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
	// WebTransport streams take neither, and neither call may fail
	st.SetPriority(1, false)
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
	us.SetPriority(1, true)
	us.SetReliableBoundary()
	if us.SetWriteDeadline(deadline) != nil || us.Context() == nil {
		t.Error("send stream accessors")
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
	buf := make([]byte, 3)
	if _, err = urs.Read(buf); err != nil || string(buf) != "uni" {
		t.Errorf("uni read %q %v", buf, err)
	}
	us.CancelWrite(uint64(message.ResetDeliveryTimeout))
	if _, err = urs.Read(buf); !isRemoteReset(err, message.ResetDeliveryTimeout) {
		t.Errorf("a reset WebTransport stream reports the MoQ code: %v", err)
	}
	urs.CancelRead(uint64(message.ResetCancelled))
	peer.CancelRead(uint64(message.ResetCancelled))
	if _, err = st.Write(make([]byte, 1<<20)); !isRemoteReset(err, message.ResetCancelled) {
		t.Errorf("a write into a stopped stream: %v", err)
	}
	// a stopped stream's context ends with the MoQ code too, decoded from the HTTP/3 error space
	<-st.Context().Done()
	peer.CancelWrite(uint64(message.ResetCancelled))
	if err = sconn.SendDatagram([]byte("dg")); err != nil {
		t.Fatal(err)
	}
	if b, err := cconn.ReceiveDatagram(ctx); err != nil || string(b) != "dg" {
		t.Errorf("datagram %q %v", b, err)
	}
	if cconn.Cause() != nil {
		t.Error("an open session has no cause")
	}
	_ = sconn.CloseWithError(uint64(message.CodeProtocolViolation), "bye")
	<-cconn.Context().Done()
	if ce, ok := errors.AsType[*session.CloseError](cconn.Cause()); !ok || !ce.Remote ||
		ce.Code != uint64(message.CodeProtocolViolation) || ce.Reason != "bye" {
		t.Errorf("remote close cause %v", cconn.Cause())
	}
	if ce, ok := errors.AsType[*session.CloseError](sconn.Cause()); !ok || ce.Remote {
		t.Errorf("local close cause %v", sconn.Cause())
	}
	if _, err = cconn.OpenStreamSync(ctx); err == nil {
		t.Error("no streams open on a closed session")
	}
	if _, err = cconn.OpenUniStreamSync(ctx); err == nil {
		t.Error("no streams open on a closed session")
	}
	if _, err = cconn.AcceptStream(ctx); err == nil {
		t.Error("no streams arrive on a closed session")
	}
	if _, err = cconn.AcceptUniStream(ctx); err == nil {
		t.Error("no streams arrive on a closed session")
	}
	if _, err = cconn.ReceiveDatagram(ctx); err == nil {
		t.Error("no datagrams arrive on a closed session")
	}
}
