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

package listener

import (
	"net"
	"net/netip"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"

	"github.com/pires/go-proxyproto"
)

// proxyHeaderTimeout bounds how long a connection may take to send its PROXY header.
const proxyHeaderTimeout = 10 * time.Second

// ProxyProtocolOptions configures PROXY protocol v1/v2 acceptance on a listener.
type ProxyProtocolOptions struct {
	// Enabled reads a PROXY header ahead of each connection's first bytes.
	Enabled bool
	// Trusted limits whose PROXY header is honored; empty trusts every peer.
	Trusted clientip.Trusted
}

// NewProxyProtocolOptions returns options for enabled listeners, or nil when disabled.
func NewProxyProtocolOptions(enabled bool, trusted clientip.Trusted) *ProxyProtocolOptions {
	if !enabled {
		return nil
	}
	return &ProxyProtocolOptions{Enabled: true, Trusted: trusted}
}

func (o *ProxyProtocolOptions) wrap(l net.Listener) net.Listener {
	return &proxyproto.Listener{
		Listener:          l,
		ConnPolicy:        o.policy,
		ReadHeaderTimeout: proxyHeaderTimeout,
	}
}

func (o *ProxyProtocolOptions) policy(c proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
	// the header is honored from trusted peers; nothing is read ahead of an untrusted peer's
	// bytes, so its data is never mistaken for a header
	if len(o.Trusted) == 0 {
		return proxyproto.USE, nil
	}
	if ap, err := netip.ParseAddrPort(c.Upstream.String()); err == nil && o.Trusted.Contains(ap.Addr()) {
		return proxyproto.USE, nil
	}
	return proxyproto.SKIP, nil
}
