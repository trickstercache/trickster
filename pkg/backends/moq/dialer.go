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
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	// SchemeMoQT is the URI scheme of a native QUIC MoQ origin
	SchemeMoQT = "moqt"
	// SchemeHTTPS is the URI scheme of a WebTransport MoQ origin
	SchemeHTTPS = "https"

	defaultPort       = "443"
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
	connect      func(ctx context.Context) (session.Conn, *codec.Codec, error)
	transport    *webtransport.Transport
	webTransport bool
	setup        message.Setup

	mu      sync.Mutex
	sess    *session.Session
	dialing chan struct{}
	err     error
	closed  bool
}

var _ relay.Upstream = (*dialer)(nil)

func newDialer(origin *url.URL, base *tls.Config, drafts []int, defaultDraft int,
	implementation string,
) (*dialer, error) {
	host, port := origin.Hostname(), origin.Port()
	if host == "" {
		return nil, fmt.Errorf("moq: origin_url %q names no host", origin)
	}
	if port == "" {
		port = defaultPort
	}
	tc := base
	if tc == nil {
		tc = &tls.Config{MinVersion: tls.VersionTLS13}
	} else {
		tc = tc.Clone()
	}
	if tc.ServerName == "" {
		tc.ServerName = host
	}
	qc := &quic.Config{
		EnableDatagrams: true, EnableStreamResetPartialDelivery: true, MaxIdleTimeout: upstreamIdle,
		KeepAlivePeriod: upstreamKeepAlive,
	}
	d := &dialer{setup: message.Setup{Implementation: implementation}}
	switch origin.Scheme {
	case SchemeMoQT:
		tc.NextProtos = codec.ALPNs(drafts)
		addr := net.JoinHostPort(host, port)
		d.connect = func(ctx context.Context) (session.Conn, *codec.Codec, error) {
			conn, err := quic.DialAddr(ctx, addr, tc, qc)
			if err != nil {
				return nil, nil, err
			}
			c, ok := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
			if !ok {
				_ = conn.CloseWithError(quic.ApplicationErrorCode(message.CodeVersionNegotiation), "no supported draft")
				return nil, nil, errNoDraft
			}
			return session.FromQUIC(conn), c, nil
		}
		path := origin.Path
		if path == "" {
			path = rootPath
		}
		authority := host
		if origin.Port() != "" {
			authority = net.JoinHostPort(host, port)
		}
		d.setup.Path, d.setup.HasPath, d.setup.Authority, d.setup.HasAuthority = path, true, authority, true
	case SchemeHTTPS:
		// the CONNECT request carries the authority and path, so SETUP names neither
		tc.NextProtos = []string{http3.NextProtoH3}
		d.webTransport = true
		// quic.DialAddr, unlike webtransport-go's default, never sends 0-RTT data
		d.transport = &webtransport.Transport{
			TLSClientConfig: tc, QUICConfig: qc, ApplicationProtocols: codec.ALPNs(drafts), DialAddr: quic.DialAddr,
		}
		target := origin.String()
		d.connect = func(ctx context.Context) (session.Conn, *codec.Codec, error) {
			_, s, err := d.transport.Dial(ctx, target, nil)
			if err != nil {
				return nil, nil, err
			}
			c, ok := negotiated(s.SessionState().ApplicationProtocol, defaultDraft)
			if !ok {
				// draft-ietf-webtrans-http3 section 3.3: a client that needs a protocol closes with WT_ALPN_ERROR
				_ = s.CloseWithError(webtransport.WTALPNErrorCode, "no supported draft")
				return nil, nil, errNoDraft
			}
			return session.FromWebTransport(s), c, nil
		}
	default:
		return nil, errUnsupportedScheme
	}
	return d, nil
}

func negotiated(protocol string, defaultDraft int) (*codec.Codec, bool) {
	// an origin that answers without WT-Protocol negotiated nothing, so only a configured default draft applies
	if protocol == "" {
		if defaultDraft == 0 {
			return nil, false
		}
		return codec.ForDraft(defaultDraft)
	}
	return codec.ForALPN(protocol)
}

// Session returns the backend's session toward its origin, dialing one when none is up; with a single origin, the
// release has no choice to inform
func (d *dialer) Session(ctx context.Context, _ relay.Scope, _ message.FullTrackName) (*session.Session, func(error),
	error,
) {
	s, err := d.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	return s, noRelease, nil
}

func noRelease(error) {}

func (d *dialer) session(ctx context.Context) (*session.Session, error) {
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
	conn, c, err := d.connect(dctx)
	if err != nil {
		return nil, err
	}
	s, err := session.New(conn, session.Options{
		Role: session.RoleClient, Codec: c, Setup: d.setup, WebTransport: d.webTransport,
	})
	if err != nil {
		_ = conn.CloseWithError(uint64(message.CodeInternalError), "")
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
	if d.transport != nil {
		// a session still being established toward the origin is abandoned
		_ = d.transport.Close()
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
