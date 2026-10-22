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

package listener

import (
	"context"
	"crypto/tls"
	stderrors "errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	ttls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestNewPacketListener(t *testing.T) {
	conn, err := NewPacketListener("127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, ok := conn.LocalAddr().(*net.UDPAddr); !ok {
		t.Errorf("expected a UDP socket, got %T", conn.LocalAddr())
	}

	if _, err = NewPacketListener("240.0.0.1", 1); err == nil {
		t.Error("expected an error binding an unusable address")
	}
}

type fakePacketServer struct {
	handler http.Handler
	tls     *tls.Config
	done    chan struct{}
}

func (f *fakePacketServer) Serve(net.PacketConn) error {
	<-f.done
	return nil
}

func (f *fakePacketServer) Shutdown(context.Context) error {
	close(f.done)
	return nil
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	keyPEM, certPEM, err := ttls.GetTestKeyAndCert(false)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestStartPacketListenerCertSwapper(t *testing.T) {
	first := testCertificate(t)
	second := testCertificate(t)

	lg := NewGroup()
	svr := &fakePacketServer{done: make(chan struct{})}
	var built *tls.Config
	go lg.StartPacketListener("h3test", "http3", "127.0.0.1", 0,
		&tls.Config{Certificates: []tls.Certificate{first}, MinVersion: tls.VersionTLS13},
		http.NotFoundHandler(),
		func(h http.Handler, tc *tls.Config) PacketServer {
			svr.handler, svr.tls, built = h, tc, tc
			return svr
		}, nil)

	l := waitForListener(t, lg, "h3test")
	defer lg.DrainAndClose("h3test", 0)

	if l.CertSwapper() == nil {
		t.Fatal("packet listener has no cert swapper")
	}
	if built == nil {
		t.Fatal("build did not receive a TLS config")
	}
	if built.GetCertificate == nil {
		t.Fatal("TLS config does not route through the swapper")
	}
	if len(built.Certificates) != 0 {
		t.Error("static certificates should be cleared so the swapper is authoritative")
	}

	hello := &tls.ClientHelloInfo{ServerName: "", Conn: nil}
	got, err := built.GetCertificate(hello)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Leaf.Equal(first.Leaf) && got.Certificate[0][0] != first.Certificate[0][0] {
		t.Error("swapper did not return the initial certificate")
	}

	// a rotation must reach the HTTP/3 handshake without rebinding the socket
	l.CertSwapper().SetCerts([]tls.Certificate{second})
	got, err = built.GetCertificate(hello)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Certificate[0]) != string(second.Certificate[0]) {
		t.Error("rotated certificate did not reach the HTTP/3 TLS config")
	}
}

func TestStartPacketListenerNoTLS(t *testing.T) {
	lg := NewGroup()
	svr := &fakePacketServer{done: make(chan struct{})}
	go lg.StartPacketListener("plain", "udp", "127.0.0.1", 0, nil,
		http.NotFoundHandler(),
		func(_ http.Handler, tc *tls.Config) PacketServer {
			if tc != nil {
				t.Error("expected a nil TLS config")
			}
			return svr
		}, nil)

	l := waitForListener(t, lg, "plain")
	defer lg.DrainAndClose("plain", 0)
	if l.CertSwapper() != nil {
		t.Error("a plaintext packet listener should have no cert swapper")
	}
}

func waitForListener(t *testing.T, lg *Group, name string) *Listener {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l := lg.Get(name); l != nil {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("listener never became available")
	return nil
}

type fakeMember struct {
	left      chan struct{}
	once      sync.Once
	shutdowns atomic.Int32
	closes    atomic.Int32
}

func newFakeMember() *fakeMember {
	return &fakeMember{left: make(chan struct{})}
}

func (f *fakeMember) Serve() error {
	<-f.left
	return nil
}

func (f *fakeMember) Shutdown(context.Context) error {
	f.shutdowns.Add(1)
	f.once.Do(func() { close(f.left) })
	return nil
}

func (f *fakeMember) Close() error {
	f.closes.Add(1)
	f.once.Do(func() { close(f.left) })
	return nil
}

func TestStartSharedPacketListener(t *testing.T) {
	first := testCertificate(t)
	lg := NewGroup()
	member := newFakeMember()
	var joinedHandler http.Handler
	var joinedCerts sw.CertSwapper
	var joinedTLS *tls.Config
	routed := make(chan string, 2)
	router := func(name string) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { routed <- name })
	}
	go lg.StartSharedPacketListener("h3shared", "http3", "127.0.0.1", 0,
		&tls.Config{Certificates: []tls.Certificate{first}, MinVersion: tls.VersionTLS13}, router("first"),
		func(h http.Handler, certs sw.CertSwapper, tc *tls.Config) (PacketMember, error) {
			joinedHandler, joinedCerts, joinedTLS = h, certs, tc
			return member, nil
		}, nil)

	l := waitForListener(t, lg, "h3shared")
	if l.CertSwapper() == nil || l.CertSwapper() != joinedCerts {
		t.Fatal("join should receive the listener's own certificate swapper")
	}
	if joinedTLS == nil || len(joinedTLS.Certificates) != 0 || joinedTLS.GetCertificate == nil {
		t.Fatal("join should receive a TLS config whose certificates come from the swapper")
	}
	if l.RouteSwapper() == nil || joinedHandler == nil {
		t.Fatal("a listener with a router should hand join its route swapper")
	}
	// a reload swaps the routes served through the shared endpoint without rejoining it
	joinedHandler.ServeHTTP(nil, nil)
	lg.UpdateRouter("h3shared", router("second"))
	joinedHandler.ServeHTTP(nil, nil)
	if a, b := <-routed, <-routed; a != "first" || b != "second" {
		t.Errorf("routes served %s then %s", a, b)
	}

	if err := lg.DrainAndClose("h3shared", time.Second); err != nil {
		t.Errorf("draining a shared member: %v", err)
	}
	if member.shutdowns.Load() != 1 {
		t.Error("draining should shut the member down")
	}
	if l.State() != StateStopped {
		t.Error("a drained member should be stopped")
	}
}

func TestStartSharedPacketListenerWithoutRouter(t *testing.T) {
	lg := NewGroup()
	member := newFakeMember()
	go lg.StartSharedPacketListener("moqshared", "moq", "127.0.0.1", 0,
		&tls.Config{MinVersion: tls.VersionTLS13}, nil,
		func(h http.Handler, _ sw.CertSwapper, _ *tls.Config) (PacketMember, error) {
			if h != nil {
				t.Error("a listener without a router should hand join no handler")
			}
			return member, nil
		}, nil)
	l := waitForListener(t, lg, "moqshared")
	defer lg.DrainAndClose("moqshared", 0)
	if l.RouteSwapper() != nil {
		t.Error("a listener without a router should have no route swapper")
	}
}

func TestStartSharedPacketListenerJoinFails(t *testing.T) {
	lg := NewGroup()
	var failed atomic.Bool
	wantErr := stderrors.New("join failed")
	err := lg.StartSharedPacketListener("broken", "moq", "127.0.0.1", 0, nil, nil,
		func(http.Handler, sw.CertSwapper, *tls.Config) (PacketMember, error) { return nil, wantErr },
		func() { failed.Store(true) })
	if !stderrors.Is(err, wantErr) {
		t.Errorf("got %v, want %v", err, wantErr)
	}
	if !failed.Load() {
		t.Error("a failed start should call the error func")
	}
	if lg.Get("broken") != nil {
		t.Error("a listener that failed to join should not be published")
	}
}

func TestStartSharedPacketListenerAfterShutdown(t *testing.T) {
	lg := NewGroup()
	if err := lg.Shutdown(0); err != nil {
		t.Fatal(err)
	}
	member := newFakeMember()
	err := lg.StartSharedPacketListener("late", "moq", "127.0.0.1", 0, nil, nil,
		func(http.Handler, sw.CertSwapper, *tls.Config) (PacketMember, error) { return member, nil }, nil)
	if !stderrors.Is(err, errors.ErrListenerGroupClosed) {
		t.Errorf("got %v, want %v", err, errors.ErrListenerGroupClosed)
	}
	if member.closes.Load() != 1 {
		t.Error("a member refused at publish should leave its endpoint")
	}
}

type shutdownOnly struct {
	ctxDone atomic.Bool
}

func (s *shutdownOnly) Shutdown(ctx context.Context) error {
	s.ctxDone.Store(ctx.Err() != nil)
	return nil
}

func TestCloseServerWithoutClose(t *testing.T) {
	s := &shutdownOnly{}
	closeServer(s)
	if !s.ctxDone.Load() {
		t.Error("a server without Close should be shut down with a context already done")
	}
}
