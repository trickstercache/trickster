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
	"net"
	"net/http"
	"testing"
	"time"

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
