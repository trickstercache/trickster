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
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	"github.com/dunglas/httpsfv"
	"github.com/quic-go/quic-go"
	qh3 "github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	// HeaderWTAvailableProtocols lists the application protocols a WebTransport client offers
	HeaderWTAvailableProtocols = "WT-Available-Protocols"
	// HeaderWTProtocol names the application protocol a WebTransport server selected
	HeaderWTProtocol = "WT-Protocol"

	protocolWebTransport       = "webtransport-h3"
	protocolWebTransportLegacy = "webtransport"
)

// ErrNotWebTransport is returned for a request that is not a WebTransport CONNECT on a connection served through
// WebTransport
var ErrNotWebTransport = errors.New("http3: not a WebTransport session request")

type (
	wtConnKey    struct{}
	wtRequestKey struct{}
)

type webTransport struct {
	srv      *webtransport.Server
	used     atomic.Bool
	draining atomic.Bool

	mu      sync.Mutex
	conns   map[*quic.Conn]*wtConn
	drained chan struct{}
	closed  bool
}

type wtConn struct {
	wt       *webTransport
	conn     *quic.Conn
	listener string
	active   atomic.Int64
}

type wtRequest struct {
	conn *wtConn
	w    http.ResponseWriter
}

func newWebTransport(handler http.Handler, limits listener.ServerLimits) *webTransport {
	wt := &webTransport{conns: make(map[*quic.Conn]*wtConn), drained: make(chan struct{})}
	wt.srv = &webtransport.Server{
		H3: &qh3.Server{
			Handler:        wt.track(handler),
			IdleTimeout:    limits.IdleTimeout,
			MaxHeaderBytes: limits.MaxHeaderBytes,
			ConnContext:    wt.connContext,
		},
		// the handler that accepts a session verifies its Origin header, since the allowed origins are its own
		CheckOrigin: func(*http.Request) bool { return true },
	}
	return wt
}

func (wt *webTransport) connContext(ctx context.Context, conn *quic.Conn) context.Context {
	wt.mu.Lock()
	c := wt.conns[conn]
	wt.mu.Unlock()
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, wtConnKey{}, c)
}

func (wt *webTransport) serve(conn *quic.Conn, listenerName string) {
	c := &wtConn{wt: wt, conn: conn, listener: listenerName}
	wt.mu.Lock()
	if wt.closed || wt.draining.Load() {
		wt.mu.Unlock()
		closeConn(conn)
		return
	}
	wt.conns[conn] = c
	wt.mu.Unlock()
	wt.used.Store(true)
	// webtransport-go serves the connection's HTTP/3 requests too, handing it the streams that open sessions
	_ = wt.srv.ServeQUICConn(conn)
	closeConn(conn)
	wt.mu.Lock()
	delete(wt.conns, conn)
	if wt.draining.Load() && len(wt.conns) == 0 && !wt.closed {
		wt.closed = true
		close(wt.drained)
	}
	wt.mu.Unlock()
}

func closeConn(conn *quic.Conn) {
	_ = conn.CloseWithError(quic.ApplicationErrorCode(qh3.ErrCodeNoError), "")
}

func (wt *webTransport) track(next http.Handler) http.Handler {
	// a connection is idle while it runs no request; a WebTransport session is a request for its whole life
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Context().Value(wtConnKey{}).(*wtConn)
		if c == nil {
			next.ServeHTTP(w, r)
			return
		}
		c.active.Add(1)
		defer c.done()
		if r.Method == http.MethodConnect && (r.Proto == protocolWebTransport || r.Proto == protocolWebTransportLegacy) {
			// quic-go's own writer reaches the upgrade however the routes in between wrap it
			r = r.WithContext(context.WithValue(r.Context(), wtRequestKey{}, &wtRequest{conn: c, w: w}))
		}
		next.ServeHTTP(w, r)
	})
}

func (c *wtConn) done() {
	if c.active.Add(-1) == 0 && c.wt.draining.Load() {
		closeConn(c.conn)
	}
}

func (wt *webTransport) shutdown(ctx context.Context) error {
	// HTTP/3 GOAWAY is unavailable on a connection webtransport-go serves, so each closes once it runs no request
	wt.mu.Lock()
	wt.draining.Store(true)
	idle := make([]*quic.Conn, 0, len(wt.conns))
	for conn, c := range wt.conns {
		if c.active.Load() == 0 {
			idle = append(idle, conn)
		}
	}
	waiting := len(wt.conns) > 0
	if !waiting && !wt.closed {
		wt.closed = true
		close(wt.drained)
	}
	wt.mu.Unlock()
	for _, conn := range idle {
		closeConn(conn)
	}
	var err error
	select {
	case <-wt.drained:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if wt.used.Load() {
		// closes whatever is still open
		_ = wt.srv.Close()
	}
	return err
}

// WebTransportRequest reports whether r asks to open a WebTransport session on a connection that can carry one,
// and names the listener it arrived on
func WebTransportRequest(r *http.Request) (string, bool) {
	wr, ok := r.Context().Value(wtRequestKey{}).(*wtRequest)
	if !ok {
		return "", false
	}
	return wr.conn.listener, true
}

// UpgradeWebTransport accepts the WebTransport session r asks for, answering with protocol as its WT-Protocol when
// not empty. The caller selects the protocol from the client's WT-Available-Protocols and verifies the Origin header.
func UpgradeWebTransport(r *http.Request, protocol string) (*webtransport.Session, error) {
	wr, ok := r.Context().Value(wtRequestKey{}).(*wtRequest)
	if !ok {
		return nil, ErrNotWebTransport
	}
	// webtransport-go negotiates nothing of its own, since the caller has chosen
	r.Header.Del(HeaderWTAvailableProtocols)
	if protocol != "" {
		v, err := httpsfv.Marshal(httpsfv.NewItem(protocol))
		if err != nil {
			return nil, err
		}
		wr.w.Header().Set(HeaderWTProtocol, v)
	}
	return wr.conn.wt.srv.Upgrade(wr.w, r)
}
