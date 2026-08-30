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

// Package http3 builds the QUIC/HTTP-3 server served by a Trickster packet
// listener. It is deliberately a leaf: quic-go and the HTTP middleware stack
// stay out of the core listener package, which knows only about datagram
// sockets, so neither can participate in an import cycle with it.
package http3

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"

	"github.com/quic-go/quic-go"
	qh3 "github.com/quic-go/quic-go/http3"
)

// AltSvcMaxAge is the advertised lifetime, in seconds, of an HTTP/3 alternative
// service record. It matches quic-go's own default.
const AltSvcMaxAge = 2592000

// NewServer returns an HTTP/3 server bound to the supplied handler.
// HTTP/3 is TLS-only by definition, and RFC 9114 3.1 requires the h3 ALPN, so
// the caller's TLS config is cloned with h3 as the sole protocol.
func NewServer(handler http.Handler, tlsConfig *tls.Config,
	advertisedPort int, readHeaderTimeout time.Duration,
) *qh3.Server {
	tc := tlsConfig.Clone()
	if !slices.Contains(tc.NextProtos, qh3.NextProtoH3) {
		tc.NextProtos = []string{qh3.NextProtoH3}
	}
	return &qh3.Server{
		Handler:   requestDeadline(handler, readHeaderTimeout),
		TLSConfig: tc,
		Port:      advertisedPort,
		QUICConfig: &quic.Config{
			// RFC 9221 datagrams are not used by the HTTP paths today, but
			// WebTransport and MoQ require them; enabling here costs nothing
			// and keeps the negotiation available.
			EnableDatagrams: true,
			Allow0RTT:       false,
		},
	}
}

// requestDeadline applies the request deadline that HTTP/3 cannot take from
// http.Server: there is no ReadHeaderTimeout on a QUIC stream, so the bound
// has to be set per-request through the ResponseController.
func requestDeadline(next http.Handler, readHeaderTimeout time.Duration) http.Handler {
	if readHeaderTimeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// quic-go never leaves Body nil or http.NoBody, so neither can be used
		// to detect an absent body; ContentLength 0 means the client declared
		// none, and -1 means unknown but possibly present.
		if r.ContentLength != 0 {
			// a writer that cannot take a deadline still serves the request
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(readHeaderTimeout))
		}
		next.ServeHTTP(w, r)
	})
}

// AltSvcAdvertiser wraps a TCP listener's handler so its responses advertise an
// HTTP/3 endpoint, which is how clients discover they may upgrade. The value is
// derived from configuration rather than the running server so the TCP listener
// carries no startup-order dependency on the UDP one.
func AltSvcAdvertiser(next http.Handler, advertisedPort int) http.Handler {
	if advertisedPort <= 0 {
		return next
	}
	value := fmt.Sprintf(`%s=":%d"; ma=%d`, qh3.NextProtoH3, advertisedPort, AltSvcMaxAge)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// an upgrade hijacks the connection before headers are written, and a
		// tunnel has no use for an alternative service record
		if !middleware.IsUpgradeRequest(r) {
			w.Header().Add(headers.NameAltSvc, value)
		}
		next.ServeHTTP(w, r)
	})
}
