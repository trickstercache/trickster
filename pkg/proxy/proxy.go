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

// Package proxy provides all proxy services for Trickster
package proxy

import (
	"context"
	"net"
	"net/http"
	"time"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/resolution"
)

const connectTimeout = time.Second * 10

// NewHTTPClient returns an HTTP client configured to the specifications of the
// running Trickster config.
func NewHTTPClient(o *bo.Options) (*http.Client, error) {
	s, err := NewSigner(o)
	if err != nil {
		return nil, err
	}
	return NewHTTPClientWith(o, s, NewOriginDialer(o))
}

// NewOriginDialer returns the dialer for the backend's origin_resolution, or nil when the
// origin is dialed by its A/AAAA addresses. Share it across a backend's clients.
func NewOriginDialer(o *bo.Options) *resolution.Dialer {
	if o == nil {
		return nil
	}
	return resolution.New(o.Name, o.OriginResolution, time.Duration(o.KeepAliveTimeout), connectTimeout)
}

// NewHTTPClientWith is NewHTTPClient signing with s and dialing with d, either of which may be nil;
// a backend's clients share both, and so their credential and SRV caches
func NewHTTPClientWith(o *bo.Options, s *taws.Signer, d *resolution.Dialer) (*http.Client, error) {
	if o == nil {
		return nil, nil
	}

	// client TLS construction is shared with the discovery pollers and the
	// health checker; see (*to.Options).ToClientTLSConfig
	TLSConfig, err := o.TLS.ToClientTLSConfig()
	if err != nil {
		return nil, err
	}

	// Deliberately no Client.Timeout: it bounds the entire body read, which
	// truncates long-lived streams and large objects mid-transfer and presents
	// the result as a complete response. Time-to-first-byte is bounded by
	// ResponseHeaderTimeout below, and a stalled transfer is bounded by the
	// per-read idle deadline the proxy engine applies to the response body.
	// The health check client sets its own total timeout after construction.
	// prior-knowledge h2c: cleartext HTTP/2 applies only when HTTP1 is absent
	// from the set, so the transport must not offer an HTTP/1 fallback
	var protocols *http.Protocols
	if o.H2CPriorKnowledge {
		var p http.Protocols
		p.SetUnencryptedHTTP2(true)
		protocols = &p
	}

	dial := (&net.Dialer{
		KeepAlive: time.Duration(o.KeepAliveTimeout),
		Timeout:   connectTimeout,
	}).DialContext
	if d != nil {
		dial = d.DialContext
	}
	tr := &http.Transport{
		DialContext:           dial,
		MaxIdleConns:          o.MaxIdleConns,
		MaxIdleConnsPerHost:   o.MaxIdleConns,
		MaxConnsPerHost:       o.MaxConcurrentConns,
		IdleConnTimeout:       time.Duration(o.KeepAliveTimeout),
		TLSHandshakeTimeout:   connectTimeout,
		ExpectContinueTimeout: time.Duration(o.Timeout),
		ResponseHeaderTimeout: time.Duration(o.Timeout),
		TLSClientConfig:       TLSConfig,
		// explicit: Go suppresses h2 auto-enable when DialContext or TLSClientConfig is custom.
		ForceAttemptHTTP2: true,
		// nil unless h2c is configured, leaving ForceAttemptHTTP2 in charge
		Protocols: protocols,
	}
	if d.VerifiesTarget() {
		// the transport adds its ALPN protocols to TLSClientConfig on first use, so read it per dial
		tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return d.DialTLSContext(ctx, network, addr, tr.TLSClientConfig)
		}
	}
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: tr,
	}

	if s != nil {
		wrapped := taws.WrapTransport(s, tr, sigV4Observer(o.Name))
		// sigV4RoundTripper does not satisfy idleCloser; wrap to keep
		// CloseIdleConnections reachable on reload.
		client.Transport = &idleClosingRoundTripper{RoundTripper: wrapped, inner: tr}
	}

	return client, nil
}

type idleClosingRoundTripper struct {
	http.RoundTripper
	inner *http.Transport
}

func (i *idleClosingRoundTripper) CloseIdleConnections() {
	if i.inner != nil {
		i.inner.CloseIdleConnections()
	}
}
