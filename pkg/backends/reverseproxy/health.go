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

package reverseproxy

import (
	"context"
	"net"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
)

// Origin schemes of a member that a stream listener relays to rather than an HTTP origin
const (
	schemeTCP = "tcp"
	schemeUDP = "udp"
)

// DefaultHealthCheckConfig returns the default HealthCheck Config for this backend provider
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	o := ho.New()
	u := c.BaseUpstreamURL()
	if u.Scheme == schemeTCP || u.Scheme == schemeUDP {
		// a stream member is not probed with a request, so it has no request to describe
		return o
	}
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.Path = u.Path
	return o
}

// HealthCheckProbe returns the probe of a tcp origin: the member is healthy when a connection
// to it can be opened. It is nil for every other origin, which is probed with an HTTP request.
func (c *Client) HealthCheckProbe() healthcheck.Probe {
	u := c.BaseUpstreamURL()
	if u == nil || u.Scheme != schemeTCP || u.Host == "" {
		return nil
	}
	addr := u.Host
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, schemeTCP, addr)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// HealthCheckUnsupported names why an origin cannot be actively probed, or is empty. A udp
// origin has no handshake to test and no request to send: whether it is up shows only in
// discovery readiness and in the datagrams it refuses.
func (c *Client) HealthCheckUnsupported() string {
	if u := c.BaseUpstreamURL(); u != nil && u.Scheme == schemeUDP {
		return "a udp origin has no generic health probe"
	}
	return ""
}
