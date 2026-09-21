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

package postgres

import (
	"context"
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

var errProbeConfig = errors.New("postgres health probe configuration is invalid")

// DefaultHealthCheckConfig returns the protocol-neutral health-check defaults. Interval,
// timeout and thresholds apply; HTTP request and response matching options do not.
func (c *Client) DefaultHealthCheckConfig() *ho.Options {
	return ho.New()
}

// HealthCheckProbe returns a native probe that logs in to the origin on a fresh
// connection with the backend's origin credentials and waits for ReadyForQuery.
func (c *Client) HealthCheckProbe() healthcheck.Probe {
	config, err := pgwire.ConfigFromOptions(c.Configuration(), Engine())
	if err != nil {
		return func(context.Context) error { return errProbeConfig }
	}
	return config.Probe
}
