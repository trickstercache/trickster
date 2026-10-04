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

package http3

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	qh3 "github.com/quic-go/quic-go/http3"
)

const (
	benchBodySize    = 64 * 1024
	benchHost        = "localhost"
	benchDialTimeout = 5 * time.Second
	benchContentType = "application/octet-stream"
)

func benchServer(b *testing.B) (*http.Client, string, func()) {
	b.Helper()
	key, cert, err := testtls.GetTestKeyAndCertWithNames(benchHost)
	if err != nil {
		b.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		b.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(cert)

	body := make([]byte, benchBodySize)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameContentType, benchContentType)
		_, _ = w.Write(body)
	})
	svr := NewServer(handler, &tls.Config{Certificates: []tls.Certificate{pair},
		MinVersion: tls.VersionTLS13}, 0, listener.ServerLimits{})

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go func() { _ = svr.Serve(conn) }()

	tr := &qh3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: benchHost,
		MinVersion: tls.VersionTLS13}}
	client := &http.Client{Transport: tr, Timeout: benchDialTimeout}
	addr := "https://" + conn.LocalAddr().String() + "/"
	return client, addr, func() {
		_ = tr.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = svr.Shutdown(ctx)
		_ = conn.Close()
	}
}

func BenchmarkHTTP3RoundTrip(b *testing.B) {
	client, addr, stop := benchServer(b)
	defer stop()

	// one warm-up request establishes the QUIC connection outside the timed loop
	resp, err := client.Get(addr)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	b.SetBytes(benchBodySize)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		resp, err := client.Get(addr)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}
		_ = resp.Body.Close()
	}
}
