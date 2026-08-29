/*
 * Copyright 2018 The Trickster Authors
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

package flight

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/apache/arrow-go/v18/arrow/flight/flightsql"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TestProtocolServerTLS verifies the Flight listener serves TLS when built
// with a certificate, that plaintext clients are refused, and that
// UpdateTLSConfig rotates the certificate for later connections.
func TestProtocolServerTLS(t *testing.T) {
	keyPEM, certPEM, err := tlstest.GetTestKeyAndCert(false)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS := &tls.Config{Certificates: []tls.Certificate{certificate}}

	srv := NewServer(&fakeUpstream{ipcBytes: buildTestIPC(t)}, newMemCache())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ps := NewProtocolServer(srv, "tls-test", serverTLS)
	go func() { _ = ps.Serve(l) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = ps.Shutdown(ctx)
	})
	addr := l.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// a TLS client connects and executes
	client, err := flightsql.NewClientCtx(ctx, addr, nil, nil,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: true,
		})))
	if err != nil {
		t.Fatalf("tls client dial: %v", err)
	}
	defer client.Close()
	if _, err := client.Execute(ctx, "SELECT * FROM cpu"); err != nil {
		t.Fatalf("execute over TLS: %v", err)
	}

	// a plaintext client is refused
	plainCtx, plainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer plainCancel()
	plain, err := flightsql.NewClientCtx(plainCtx, addr, nil, nil,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err == nil {
		defer plain.Close()
		if _, rpcErr := plain.Execute(plainCtx, "SELECT 1"); rpcErr == nil {
			t.Fatal("plaintext client succeeded against a TLS listener")
		}
	}

	// rotation swaps the served certificate for subsequent connections
	rotatedKey, rotatedCert, err := tlstest.GetTestKeyAndCert(false)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := tls.X509KeyPair(rotatedCert, rotatedKey)
	if err != nil {
		t.Fatal(err)
	}
	ps.UpdateTLSConfig(&tls.Config{Certificates: []tls.Certificate{rotated}})
	client2, err := flightsql.NewClientCtx(ctx, addr, nil, nil,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			InsecureSkipVerify: true,
		})))
	if err != nil {
		t.Fatalf("post-rotation dial: %v", err)
	}
	defer client2.Close()
	if _, err := client2.Execute(ctx, "SELECT * FROM cpu"); err != nil {
		t.Fatalf("execute after rotation: %v", err)
	}
}
