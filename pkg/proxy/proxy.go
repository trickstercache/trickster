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
	"net"
	"net/http"
	"time"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

const connectTimeout = time.Second * 10

// NewHTTPClient returns an HTTP client configured to the specifications of the
// running Trickster config.
func NewHTTPClient(o *bo.Options) (*http.Client, error) {
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

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				KeepAlive: time.Duration(o.KeepAliveTimeout),
				Timeout:   connectTimeout,
			}).DialContext,
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
		},
	}

	if o.SigV4 != nil {
		inner, _ := client.Transport.(*http.Transport)
		wrapped, err := taws.NewRoundTripper(o.SigV4, client.Transport)
		if err != nil {
			return nil, err
		}
		// sigV4RoundTripper does not satisfy idleCloser; wrap to keep
		// CloseIdleConnections reachable on reload.
		client.Transport = &idleClosingRoundTripper{RoundTripper: wrapped, inner: inner}
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
