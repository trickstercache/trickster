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

// Package http3 serves HTTP/3 on the QUIC connections a Trickster packet listener
// hands it. It is deliberately a leaf: quic-go and the HTTP middleware stack
// stay out of the core listener package, which knows only about datagram
// sockets, so neither can participate in an import cycle with it.
package http3

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"

	"github.com/quic-go/quic-go"
	qh3 "github.com/quic-go/quic-go/http3"
)

const (
	// AltSvcMaxAge is the advertised lifetime, in seconds, of an HTTP/3 alternative
	// service record. It matches quic-go's own default.
	AltSvcMaxAge = 2592000
	// ALPN is the protocol an HTTP/3 endpoint negotiates; RFC 9114 3.1 requires h3
	ALPN = qh3.NextProtoH3
)

// Handler serves HTTP/3 on the connections a shared QUIC endpoint dispatches to it
type Handler struct {
	svr *qh3.Server
}

// NewHandler returns an HTTP/3 handler bound to the supplied HTTP handler. TLS and the QUIC transport
// belong to the endpoint that hands it connections.
func NewHandler(handler http.Handler, limits listener.ServerLimits) *Handler {
	return &Handler{svr: &qh3.Server{
		// quic-go sets its own server context key rather than net/http's, so
		// the served marker is what lets the proxy recognize an H3 client
		Handler:        middleware.MarkServed(requestDeadline(handler, bodyDeadline(limits))),
		IdleTimeout:    limits.IdleTimeout,
		MaxHeaderBytes: limits.MaxHeaderBytes,
	}}
}

// QUICConfig returns the transport settings HTTP/3 needs from the endpoint serving it.
// RFC 9221 datagrams are not used by the HTTP paths today, but WebTransport and MoQ
// require them; enabling them costs nothing and keeps the negotiation available.
func QUICConfig() *quic.Config {
	return &quic.Config{EnableDatagrams: true}
}

// ServeQUIC serves HTTP/3 on one connection until it closes
func (h *Handler) ServeQUIC(conn *quic.Conn) {
	_ = h.svr.ServeQUICConn(conn)
	// a server already shutting down refuses the connection without closing it; closing a
	// connection that has ended is a no-op
	_ = conn.CloseWithError(quic.ApplicationErrorCode(qh3.ErrCodeNoError), "")
}

// Shutdown sends GOAWAY on every connection, waits for their requests until ctx is done, then closes them
func (h *Handler) Shutdown(ctx context.Context) error {
	return h.svr.Shutdown(ctx)
}

// Server returns the underlying HTTP/3 server
func (h *Handler) Server() *qh3.Server {
	return h.svr
}

func bodyDeadline(limits listener.ServerLimits) time.Duration {
	// a QUIC stream has no whole-request deadline either, so ReadTimeout, when set, bounds the body instead
	if limits.ReadTimeout > 0 {
		return limits.ReadTimeout
	}
	return limits.ReadHeaderTimeout
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
