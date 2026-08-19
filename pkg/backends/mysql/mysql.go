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

// Package mysql provides the MySQL backend provider. The backend is served by
// a listener whose protocol is 'mysql' rather than by HTTP routes.
package mysql

import (
	"net"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

// DefaultPort is the default MySQL server port, used when an origin_url does
// not specify one.
const DefaultPort = "3306"

var _ backends.Backend = (*Client)(nil)

// MySQLRouteConfig returns this backend's typed native route capability. The
// User Router retains only a protocol-neutral backend reference; the MySQL
// listener asserts this capability after selecting a target.
func (c *Client) MySQLRouteConfig() (ProtocolConfig, error) {
	return ProtocolConfigFromOptions(c.Configuration())
}

// Client implements the MySQL backend provider
type Client struct {
	backends.Backend
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new MySQL backend Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup,
) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.New(name, o, c.RegisterHandlers, router, cache)
	c.Backend = b
	return c, err
}

// RegisterHandlers registers the provided Handlers into the Router. The MySQL
// backend serves no HTTP routes.
func (c *Client) RegisterHandlers(handlers.Lookup) {}

// DefaultPathConfigs returns the default PathConfigs for this backend
// provider. The MySQL backend serves no HTTP paths.
func (c *Client) DefaultPathConfigs(*bo.Options) po.List {
	return nil
}

// OriginAddress returns the TCP host:port of this backend's origin, applying
// the default MySQL port when the origin_url did not specify one.
func OriginAddress(o *bo.Options) string {
	if o == nil || o.Host == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(o.Host); err == nil {
		return o.Host
	}
	return net.JoinHostPort(o.Host, DefaultPort)
}
