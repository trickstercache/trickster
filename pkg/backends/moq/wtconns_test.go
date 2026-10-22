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
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func startFlowControlledWTOrigin(t *testing.T) (*origin, *atomic.Int32) {
	t.Helper()
	// an origin with WebTransport flow control accepts several sessions on one connection; conns counts them
	o := &origin{seen: make(chan *message.Setup, 8), sessions: make(chan *session.Session, 8)}
	srv := &webtransport.Server{
		H3: &http3.Server{}, ApplicationProtocols: codec.ALPNs(codec.ServerPreference),
		CheckOrigin: func(*http.Request) bool { return true },
		Config:      &webtransport.Config{MaxIncomingStreams: 100, MaxIncomingUniStreams: 1000, MaxIncomingData: 1 << 24},
	}
	webtransport.ConfigureHTTP3Server(srv.H3)
	srv.H3.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := srv.Upgrade(w, r)
		if err != nil {
			return
		}
		c, ok := codec.ForALPN(ws.SessionState().ApplicationProtocol)
		if !ok {
			return
		}
		s, _ := session.New(session.FromWebTransport(ws), session.Options{
			Role: session.RoleServer, Codec: c, WebTransport: true, Handler: o,
			OnSetup: func(_ *session.Session, m *message.Setup) error { o.seen <- m; return nil },
		})
		if s.Start(context.Background()) == nil {
			<-s.Done()
		}
	})
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{testCert(t)}, NextProtos: []string{http3.NextProtoH3},
		MinVersion: tls.VersionTLS13,
	}, &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	o.addr = ln.Addr().String()
	var conns atomic.Int32
	t.Cleanup(func() {
		_ = ln.Close()
		_ = srv.Close()
	})
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			conns.Add(1)
			go func() { _ = srv.ServeQUICConn(conn) }()
		}
	}()
	return o, &conns
}

func TestWebTransportOriginsShareAConnection(t *testing.T) {
	org, conns := startFlowControlledWTOrigin(t)
	a := newBackend(t, "wt-share-a", "https://"+org.addr+"/a")
	b := newBackend(t, "wt-share-b", "https://"+org.addr+"/b")
	for _, c := range []*Client{a, b} {
		if _, err := c.sh.dialer.session(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// two backends, two WebTransport sessions, one QUIC connection
	if n := conns.Load(); n != 1 {
		t.Errorf("the origin accepted %d connections", n)
	}
	// the connection outlives one backend's session, and closes with the last
	a.sh.dialer.close()
	if _, err := b.sh.dialer.session(context.Background()); err != nil {
		t.Errorf("the remaining backend's session survives: %v", err)
	}
	b.sh.dialer.close()
	key := a.sh.dialer.connKey
	deadline := time.Now().Add(wait)
	for {
		wtConnsMu.Lock()
		gone := wtConns[key] == nil
		wtConnsMu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the shared connection closes with its last session")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWebTransportOriginWithoutFlowControlIsNotShared(t *testing.T) {
	org := startWTOrigin(t, true)
	a := newBackend(t, "wt-alone-a", "https://"+org.addr+"/a")
	b := newBackend(t, "wt-alone-b", "https://"+org.addr+"/b")
	for _, c := range []*Client{a, b} {
		if _, err := c.sh.dialer.session(context.Background()); err != nil {
			t.Fatalf("each backend reaches the origin: %v", err)
		}
	}
	// the second session is refused on the shared connection, so that origin is dialed one session at a time
	wtConnsMu.Lock()
	_, alone := unpooled[a.sh.dialer.connKey]
	wtConnsMu.Unlock()
	if !alone {
		t.Error("an origin that refuses a second session is no longer shared")
	}
}

func TestSharedWebTransportDials(t *testing.T) {
	org, conns := startFlowControlledWTOrigin(t)
	// backends dialing at once wait for the one connection being dialed
	clients := []*Client{
		newBackend(t, "wt-race-a", "https://"+org.addr+"/a"), newBackend(t, "wt-race-b", "https://"+org.addr+"/b"),
		newBackend(t, "wt-race-c", "https://"+org.addr+"/c"),
	}
	errs := make(chan error, len(clients))
	for _, c := range clients {
		go func() {
			_, err := c.sh.dialer.session(context.Background())
			errs <- err
		}()
	}
	for range clients {
		if err := recv(t, errs, "a session"); err != nil {
			t.Fatal(err)
		}
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("the origin accepted %d connections", n)
	}
	// an origin that cannot be reached fails the dial, and leaves nothing shared behind
	down := newBackend(t, "wt-down-origin", "https://127.0.0.1:1/live")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := down.sh.dialer.session(ctx); err == nil {
		t.Fatal("an unreachable origin fails")
	}
	wtConnsMu.Lock()
	left := wtConns[down.sh.dialer.connKey]
	wtConnsMu.Unlock()
	if left != nil {
		t.Error("a failed dial is not shared")
	}
}

func TestConnKeys(t *testing.T) {
	u, _ := url.Parse("https://relay.example.com/live")
	plain := &bo.Options{}
	secured := &bo.Options{TLS: &to.Options{InsecureSkipVerify: true, ServerName: "relay"}}
	a, b := connKey(u, plain, []int{21}), connKey(u, secured, []int{21})
	if a == "" || a == b || !strings.HasPrefix(a, "relay.example.com:443|") {
		t.Errorf("keys %q %q", a, b)
	}
	if connKey(u, plain, []int{20}) == a {
		t.Error("drafts are part of the key")
	}
	native, _ := url.Parse("moqt://relay.example.com/live")
	if connKey(native, plain, nil) != "" {
		t.Error("native sessions are never shared")
	}
	if !rejected(&http3.Error{ErrorCode: http3.ErrCodeRequestRejected}) ||
		!rejected(&quic.StreamError{ErrorCode: quic.StreamErrorCode(http3.ErrCodeRequestRejected)}) ||
		rejected(&http3.Error{ErrorCode: http3.ErrCodeNoError}) {
		t.Error("rejected recognizes H3_REQUEST_REJECTED")
	}
}
