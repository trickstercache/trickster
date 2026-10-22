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

package diagstreamer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/proxy/quicdial"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	moqImplementation = "trickster-diagstreamer"
	schemeMoQT        = "moqt"
	schemeHTTPS       = "https"
	moqDefaultPort    = "443"
	moqRootPath       = "/"
	moqKeepAlive      = 15 * time.Second
)

var errNoDraft = errors.New("moq: the relay negotiated no supported draft")

func moqTLS(cfg *Config, host string) (*tls.Config, error) {
	tc := &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: host,
		InsecureSkipVerify: cfg.MoQInsecure, // #nosec G402 -- a development tool's opt-in flag
	}
	if cfg.MoQCA == "" {
		return tc, nil
	}
	pem, err := os.ReadFile(cfg.MoQCA)
	if err != nil {
		return nil, fmt.Errorf("reading the MoQ CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates in %s", cfg.MoQCA)
	}
	tc.RootCAs = pool
	return tc, nil
}

func dialMoQ(ctx context.Context, cfg *Config, h session.Handler) (*session.Session, func(), error) {
	// opens a session to the relay at cfg.MoQURL, over native QUIC for moqt:// or WebTransport for https://;
	// the returned close ends the session and its transport
	u, err := url.Parse(cfg.MoQURL)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing the MoQ URL: %w", err)
	}
	tc, err := moqTLS(cfg, u.Hostname())
	if err != nil {
		return nil, nil, err
	}
	qc := &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true, KeepAlivePeriod: moqKeepAlive}
	setup := message.Setup{Implementation: moqImplementation}
	var (
		conn    session.Conn
		c       *codec.Codec
		ok, wt  bool
		closeFn = func() {}
	)
	switch strings.ToLower(u.Scheme) {
	case schemeMoQT:
		port := u.Port()
		if port == "" {
			port = moqDefaultPort
		}
		tc.NextProtos = codec.ALPNs(codec.ClientOffer)
		qconn, err := quicdial.DialAddr(ctx, net.JoinHostPort(u.Hostname(), port), tc, qc)
		if err != nil {
			return nil, nil, err
		}
		if c, ok = codec.ForALPN(qconn.ConnectionState().TLS.NegotiatedProtocol); !ok {
			_ = qconn.CloseWithError(quic.ApplicationErrorCode(message.CodeVersionNegotiation), "")
			return nil, nil, errNoDraft
		}
		path := u.Path
		if path == "" {
			path = moqRootPath
		}
		setup.Path, setup.HasPath, setup.Authority, setup.HasAuthority = path, true, u.Host, true
		conn = session.FromQUIC(qconn)
	case schemeHTTPS:
		tc.NextProtos = []string{http3.NextProtoH3}
		tr := &webtransport.Transport{
			TLSClientConfig: tc, QUICConfig: qc, ApplicationProtocols: codec.ALPNs(codec.ClientOffer),
			DialAddr: quicdial.DialAddr,
		}
		_, ws, err := tr.Dial(ctx, u.String(), nil)
		if err != nil {
			return nil, nil, err
		}
		closeFn = func() { _ = tr.Close() }
		if c, ok = codec.ForALPN(ws.SessionState().ApplicationProtocol); !ok {
			_ = ws.CloseWithError(webtransport.WTALPNErrorCode, "")
			closeFn()
			return nil, nil, errNoDraft
		}
		conn, wt = session.FromWebTransport(ws), true
	default:
		return nil, nil, fmt.Errorf("the MoQ URL must use the %s:// or %s:// scheme", schemeMoQT, schemeHTTPS)
	}
	s, err := session.New(conn, session.Options{
		Role: session.RoleClient, Codec: c, Setup: setup, Handler: h, WebTransport: wt,
	})
	if err != nil {
		_ = conn.CloseWithError(uint64(message.CodeInternalError), "")
		closeFn()
		return nil, nil, err
	}
	if err = s.Start(ctx); err != nil {
		closeFn()
		return nil, nil, err
	}
	return s, func() {
		_ = s.Close(message.CodeNoError, "")
		closeFn()
	}, nil
}
