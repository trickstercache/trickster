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

package questdb

import (
	"context"
	"errors"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

var errProbeConfig = errors.New("questdb health probe configuration is invalid")

// DefaultHealthCheckConfig selects HTTP health checks for HTTP listener
// mappings and leaves native-only deployments to the pgwire probe.
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	o := ho.New()
	if options := c.Configuration(); options != nil && options.HasHTTPListener {
		u := c.BaseUpstreamURL()
		o.Scheme, o.Host, o.Path = u.Scheme, u.Host, strings.TrimSuffix(u.Path, "/")+"/execute"
		o.Query = "query=SELECT%201"
	}
	return o
}

// HealthCheckProbe uses a cleartext pgwire login and Terminate for native-only
// deployments. HTTP-capable deployments use the configured HTTP health check.
func (c *Client) HealthCheckProbe() healthcheck.Probe {
	if options := c.Configuration(); options != nil && options.HasHTTPListener {
		return nil
	}
	config, err := pgwire.ConfigFromOptions(c.Configuration(), Engine())
	if err != nil {
		return func(context.Context) error { return errProbeConfig }
	}
	return config.Probe
}
