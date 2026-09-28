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

package greptimedb

import (
	"context"
	"errors"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

var errProbeConfig = errors.New("greptimedb health probe configuration is invalid")

// DefaultHealthCheckConfig selects HTTP health only for HTTP listener mappings.
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	o := ho.New()
	if options := c.Configuration(); options != nil && options.HasHTTPListener {
		u := c.BaseUpstreamURL()
		o.Scheme, o.Host, o.Path = u.Scheme, u.Host, u.Path+"/health"
	}
	return o
}

// HealthCheckProbe uses a native login probe for native-only deployments.
func (c *Client) HealthCheckProbe() healthcheck.Probe {
	if options := c.Configuration(); options != nil && options.HasHTTPListener {
		return nil
	}
	if o := c.Configuration(); o != nil && slices.Contains(o.NativeListenerProtocols, listener.ProtocolMySQL) &&
		!slices.Contains(o.NativeListenerProtocols, listener.ProtocolPostgres) {
		probe, err := mysql.HealthCheckProbeForEngine(o, MySQLEngine())
		if err != nil {
			return func(context.Context) error { return errProbeConfig }
		}
		return probe
	}
	config, err := pgwire.ConfigFromOptions(c.Configuration(), Engine())
	if err != nil {
		return func(context.Context) error { return errProbeConfig }
	}
	return config.Probe
}
