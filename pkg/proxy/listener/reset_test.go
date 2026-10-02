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
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"time"
)

func acceptOne(t *testing.T, ln net.Listener) (client, server net.Conn) {
	t.Helper()
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
	t.Cleanup(func() { _ = client.Close() })
	select {
	case server = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was accepted")
	}
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func expectReset(t *testing.T, client net.Conn) {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Read(make([]byte, 1)); !errors.Is(err, syscall.ECONNRESET) {
		t.Errorf("the client read %v, want a reset", err)
	}
}

// a reset reaches the TCP connection beneath the accounting wrapper, and beneath a PROXY protocol
// connection too, and the client sees a reset rather than a close; what cannot be reset says so
func TestObservedConnectionReset(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, server := acceptOne(t, ln)
	if err := (&observedConnection{Conn: server}).Reset(); err != nil {
		t.Fatalf("Reset = %v", err)
	}
	expectReset(t, client)

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	behindProxy := (&ProxyProtocolOptions{Enabled: true}).wrap(tcp)
	defer behindProxy.Close()
	client, server = acceptOne(t, behindProxy)
	if _, err := io.WriteString(client, "PROXY TCP4 203.0.113.9 10.0.0.1 4242 80\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := (&observedConnection{Conn: server}).Reset(); err != nil {
		t.Fatalf("Reset behind a PROXY header = %v", err)
	}
	expectReset(t, client)

	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	if err := (&observedConnection{Conn: p1}).Reset(); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a pipe was reset: %v", err)
	}
}
