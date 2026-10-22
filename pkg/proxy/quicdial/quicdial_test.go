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

package quicdial

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
)

const testALPN = "quicdial-test"

func listen(t *testing.T, addr string) (*quic.Listener, chan string) {
	t.Helper()
	keyPEM, certPEM, err := testtls.GetTestKeyAndCertWithNames("localhost")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	names := make(chan string, 4)
	ln, err := quic.ListenAddr(addr, &tls.Config{
		Certificates: []tls.Certificate{cert}, NextProtos: []string{testALPN},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			names <- hello.ServerName
			return nil, nil
		},
	}, nil)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() { <-c.Context().Done() }()
		}
	}()
	return ln, names
}

func clientTLS(serverName string) *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{testALPN}, // #nosec G402 -- test certificate
		ServerName: serverName}
}

func TestDialAddrIPv4(t *testing.T) {
	ln, names := listen(t, "127.0.0.1:0")
	port := strconv.Itoa(ln.Addr().(*net.UDPAddr).Port)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialAddr(ctx, net.JoinHostPort("localhost", port), clientTLS(""), nil)
	if err != nil {
		t.Fatal(err)
	}
	// the server name comes from the host, and the socket is IPv4 alone rather than dual-stack
	if n := <-names; n != "localhost" {
		t.Errorf("server name %q", n)
	}
	local := conn.LocalAddr().(*net.UDPAddr)
	if local.IP.To4() == nil {
		t.Errorf("local address %v is not an IPv4 socket", local)
	}
	_ = conn.CloseWithError(0, "")
	// the socket closes with its connection, so its port can be bound again
	deadline := time.Now().Add(5 * time.Second)
	for {
		pc, err := net.ListenUDP("udp4", &net.UDPAddr{Port: local.Port})
		if err == nil {
			_ = pc.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the socket outlived its connection: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDialAddrEarlyIPv6(t *testing.T) {
	ln, names := listen(t, "[::1]:0")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := DialAddrEarly(ctx, ln.Addr().String(), clientTLS("relay.example"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	if n := <-names; n != "relay.example" {
		t.Errorf("a configured server name is kept, got %q", n)
	}
	if local := conn.LocalAddr().(*net.UDPAddr); local.IP.To4() != nil {
		t.Errorf("local address %v is not an IPv6 socket", local)
	}
}

func TestDialErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := DialAddr(ctx, "no-port", nil, nil); err == nil {
		t.Error("an address without a port is refused")
	}
	if _, err := DialAddr(ctx, "localhost:no-such-service", nil, nil); err == nil {
		t.Error("an unknown service is refused")
	}
	// resolution stops with the dial's context
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := DialAddr(canceled, "quicdial.invalid:443", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled dial resolves nothing: %v", err)
	}
	// nothing listens on the port, so the handshake gives up when the context does
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	if _, err = DialAddr(ctx, addr, nil, nil); err == nil {
		t.Error("a dial nothing answers fails")
	}
}
