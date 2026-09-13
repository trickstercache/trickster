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
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// recordingPacketServer notes the socket it was given and blocks until shut down
type recordingPacketServer struct {
	served   atomic.Pointer[net.PacketConn]
	shutdown atomic.Bool
	done     chan struct{}
}

func (s *recordingPacketServer) Serve(pc net.PacketConn) error {
	s.served.Store(&pc)
	<-s.done
	return nil
}

func (s *recordingPacketServer) Shutdown(context.Context) error {
	s.shutdown.Store(true)
	close(s.done)
	return nil
}

func TestStartDatagramListenerAndProtocolServerAs(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	lg := NewGroup()
	svr := &recordingPacketServer{done: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() { errCh <- lg.StartDatagramListener("udp-test", "udp", "127.0.0.1", 0, svr, nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for lg.Get("udp-test") == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	l := lg.Get("udp-test")
	if l == nil || !l.WaitForReady(time.Second) {
		t.Fatal("datagram listener did not become ready")
	}
	if l.RouteSwapper() != nil || l.CertSwapper() != nil {
		t.Error("a datagram listener carries no routes and no certificates")
	}
	if got, ok := ProtocolServerAs[*recordingPacketServer](lg, "udp-test"); !ok || got != svr {
		t.Errorf("ProtocolServerAs = %v, %v", got, ok)
	}
	if _, ok := ProtocolServerAs[*stubPacketServerPtr](lg, "udp-test"); ok {
		t.Error("ProtocolServerAs matched the wrong type")
	}
	if _, ok := ProtocolServerAs[*recordingPacketServer](lg, "missing"); ok {
		t.Error("ProtocolServerAs found a listener that does not exist")
	}
	if _, ok := ProtocolServerAs[*recordingPacketServer](nil, "udp-test"); ok {
		t.Error("ProtocolServerAs on a nil group")
	}
	for svr.served.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if svr.served.Load() == nil {
		t.Fatal("the server was not handed the socket")
	}
	if err := lg.DrainAndClose("udp-test", time.Second); err != nil {
		t.Fatal(err)
	}
	if !svr.shutdown.Load() {
		t.Error("the server was not shut down")
	}
	if err := <-errCh; err != nil {
		t.Errorf("StartDatagramListener returned %v", err)
	}
}

// stubPacketServerPtr is a type no listener holds, for the negative type assertion
type stubPacketServerPtr struct{}

func TestStartDatagramListenerBindFailure(t *testing.T) {
	logger.SetLogger(logging.NoopLogger())
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	var called atomic.Bool
	lg := NewGroup()
	err = lg.StartDatagramListener("taken", "udp", "127.0.0.1", port,
		&recordingPacketServer{done: make(chan struct{})}, func() { called.Store(true) })
	if err == nil {
		t.Fatal("a bound port was bound again")
	}
	if !called.Load() {
		t.Error("the failure callback was not invoked")
	}
	if lg.Get("taken") != nil {
		t.Error("a listener that failed to bind was published")
	}
}

func TestObservedConnectionCloseWrite(t *testing.T) {
	// the accepted connection's half-close reaches the TCP connection beneath the accounting
	// wrapper, and a connection that cannot half-close says so
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server := <-accepted
	defer server.Close()
	oc := &observedConnection{Conn: server}
	if err := oc.CloseWrite(); err != nil {
		t.Fatalf("CloseWrite = %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Error("the client did not see the half-close")
	}
	if _, err := client.Write([]byte("still open")); err != nil {
		t.Errorf("the client's side was closed too: %v", err)
	}
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	if err := (&observedConnection{Conn: p1}).CloseWrite(); err == nil {
		t.Error("a pipe was half-closed")
	}
}
