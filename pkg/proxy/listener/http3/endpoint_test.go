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
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/quicmux"
	sw "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	qh3 "github.com/quic-go/quic-go/http3"
)

const (
	testHost        = "localhost"
	testDialTimeout = 5 * time.Second
	testLoopback    = "127.0.0.1"
)

func testEndpoint(tb testing.TB, handler http.Handler) (*http.Client, string, func()) {
	tb.Helper()
	return testEndpointWith(tb, NewHandler(handler, listener.ServerLimits{}))
}

func testEndpointWith(tb testing.TB, h *Handler) (*http.Client, string, func()) {
	tb.Helper()
	key, cert, err := testtls.GetTestKeyAndCertWithNames(testHost)
	if err != nil {
		tb.Fatal(err)
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		tb.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(cert)

	m, err := quicmux.Join(testLoopback, 0, quicmux.MemberOptions{
		Name: "h3test", ALPNs: []string{ALPN}, Handler: h,
		Certs: sw.NewSwapper([]tls.Certificate{pair}),
		TLS:   &tls.Config{MinVersion: tls.VersionTLS13}, QUIC: QUICConfig(),
	})
	if err != nil {
		tb.Fatal(err)
	}
	go func() { _ = m.Serve() }()

	tr := &qh3.Transport{TLSClientConfig: &tls.Config{
		RootCAs: pool, ServerName: testHost,
		MinVersion: tls.VersionTLS13,
	}}
	client := &http.Client{Transport: tr, Timeout: testDialTimeout}
	return client, "https://" + m.LocalAddr().String() + "/", func() {
		_ = tr.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	}
}
