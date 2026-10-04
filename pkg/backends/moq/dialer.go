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

package moq

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"

	"github.com/quic-go/quic-go"
)

const (
	// SchemeMoQT is the URI scheme of a native QUIC MoQ origin
	SchemeMoQT = "moqt"
	// SchemeHTTPS is the URI scheme of a WebTransport MoQ origin
	SchemeHTTPS = "https"

	defaultMoQTPort   = "443"
	upstreamIdle      = time.Minute
	upstreamKeepAlive = 15 * time.Second
	dialTimeout       = 10 * time.Second
)

var (
	errUnsupportedScheme = errors.New("moq: origin_url must use the moqt:// or https:// scheme")
	errNoDraft           = errors.New("moq: the origin negotiated no supported draft")
	errDialerClosed      = errors.New("moq: backend closed")
)

type dialer struct {
	addr  string
	tls   *tls.Config
	quic  *quic.Config
	setup message.Setup

	mu      sync.Mutex
	sess    *session.Session
	dialing chan struct{}
	err     error
	closed  bool
}

var _ relay.Upstream = (*dialer)(nil)

func newDialer(origin *url.URL, base *tls.Config, drafts []int, implementation string) (*dialer, error) {
	if origin.Scheme != SchemeMoQT {
		// a WebTransport origin is reached through its own client
		return nil, errUnsupportedScheme
	}
	host, port := origin.Hostname(), origin.Port()
	if host == "" {
		return nil, fmt.Errorf("moq: origin_url %q names no host", origin)
	}
	if port == "" {
		port = defaultMoQTPort
	}
	tc := base
	if tc == nil {
		tc = &tls.Config{MinVersion: tls.VersionTLS13}
	} else {
		tc = tc.Clone()
	}
	tc.NextProtos = codec.ALPNs(drafts)
	if tc.ServerName == "" {
		tc.ServerName = host
	}
	path := origin.Path
	if path == "" {
		path = "/"
	}
	authority := net.JoinHostPort(host, port)
	if origin.Port() == "" {
		authority = host
	}
	return &dialer{
		addr: net.JoinHostPort(host, port), tls: tc,
		quic: &quic.Config{
			EnableDatagrams: true, EnableStreamResetPartialDelivery: true, MaxIdleTimeout: upstreamIdle,
			KeepAlivePeriod: upstreamKeepAlive,
		},
		setup: message.Setup{
			Path: path, HasPath: true, Authority: authority, HasAuthority: true, Implementation: implementation,
		},
	}, nil
}

// Session returns the backend's session toward its origin, dialing one when none is up
func (d *dialer) Session(ctx context.Context, _ relay.Scope) (*session.Session, error) {
	// one session is kept per origin and dialed again once it ends
	for {
		d.mu.Lock()
		switch {
		case d.closed:
			d.mu.Unlock()
			return nil, errDialerClosed
		case d.sess != nil && d.sess.Err() == nil:
			s := d.sess
			d.mu.Unlock()
			return s, nil
		case d.dialing != nil:
			// one dial at a time; the others wait for its outcome
			ch := d.dialing
			d.mu.Unlock()
			select {
			case <-ch:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			d.mu.Lock()
			s, err := d.sess, d.err
			d.mu.Unlock()
			if err != nil {
				return nil, err
			}
			if s != nil && s.Err() == nil {
				return s, nil
			}
			continue
		}
		ch := make(chan struct{})
		d.dialing = ch
		d.mu.Unlock()
		s, err := d.dial(ctx)
		d.mu.Lock()
		d.dialing, d.sess, d.err = nil, s, err
		closed := d.closed
		d.mu.Unlock()
		if s != nil && closed {
			_ = s.Close(message.CodeNoError, "backend closed")
		}
		close(ch)
		return s, err
	}
}

func (d *dialer) dial(ctx context.Context) (*session.Session, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	conn, err := quic.DialAddr(dctx, d.addr, d.tls, d.quic)
	if err != nil {
		return nil, err
	}
	c, ok := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
	if !ok {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeVersionNegotiation), "no supported draft")
		return nil, errNoDraft
	}
	s, err := session.New(session.FromQUIC(conn), session.Options{Role: session.RoleClient, Codec: c, Setup: d.setup})
	if err != nil {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeInternalError), "")
		return nil, err
	}
	if err = s.Start(dctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (d *dialer) probe(ctx context.Context) error {
	// a fresh session that completes SETUP shows the origin is serving
	s, err := d.dial(ctx)
	if err != nil {
		return err
	}
	return s.Close(message.CodeNoError, "health check")
}

func (d *dialer) close() {
	d.mu.Lock()
	d.closed = true
	s := d.sess
	d.sess = nil
	d.mu.Unlock()
	if s != nil {
		_ = s.Close(message.CodeNoError, "backend closed")
	}
}

func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case SchemeMoQT, SchemeHTTPS:
		u.Scheme = strings.ToLower(u.Scheme)
		return u, nil
	}
	return nil, errUnsupportedScheme
}
