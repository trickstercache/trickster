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

package l4

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func selfSigned(t *testing.T, hosts ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "l4 test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: hosts, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestPeekClientHello(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		// the handshake never completes, since the peek answers nothing; it only sends the hello
		_ = tls.Client(client, &tls.Config{
			ServerName:         "shop.example.com",
			InsecureSkipVerify: true,
		}).Handshake() // #nosec G402 -- test client
	}()
	name, replay, err := peekClientHello(server, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if name != "shop.example.com" {
		t.Errorf("server name = %q", name)
	}
	head := make([]byte, 6)
	if _, err := io.ReadFull(replay, head); err != nil {
		t.Fatal(err)
	}
	if head[0] != 0x16 || head[5] != 0x01 {
		t.Errorf("replayed bytes do not begin with a ClientHello record: % x", head)
	}
	if err := replay.(*replayConn).CloseWrite(); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("CloseWrite over a pipe = %v", err)
	}
}

func TestPeekClientHelloRefusesPlaintextAndSilence(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() { _, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")) }()
	if _, _, err := peekClientHello(server, time.Second); err == nil {
		t.Error("plaintext was read as a client hello")
	}
	quietClient, quietServer := net.Pipe()
	t.Cleanup(func() { _ = quietClient.Close(); _ = quietServer.Close() })
	if _, _, err := peekClientHello(quietServer, 50*time.Millisecond); err == nil {
		t.Error("a silent client was not timed out")
	}
	pc := peekConn{}
	if _, err := pc.Write(nil); err == nil || pc.Close() != nil || pc.LocalAddr() != nil ||
		pc.RemoteAddr() != nil || pc.SetDeadline(time.Time{}) != nil ||
		pc.SetReadDeadline(time.Time{}) != nil || pc.SetWriteDeadline(time.Time{}) != nil {
		t.Error("peek conn must refuse writes and accept everything else")
	}
}
