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
// Package postgres provides the PostgreSQL backend provider, which also serves
// TimescaleDB. The backend is served by a listener whose protocol is 'postgres'
// rather than by HTTP routes.
package postgres

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
)

// DefaultPort is the default PostgreSQL server port, used when an origin_url
// does not specify one.
const DefaultPort = "5432"

var _ backends.Backend = (*Client)(nil)

// Client implements the PostgreSQL backend provider
type Client struct {
	backends.Backend
}

var _ types.NewBackendClientFunc = NewClient

// NewClient returns a new PostgreSQL backend Client Instance
func NewClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, _ backends.Backends,
	_ types.Lookup,
) (backends.Backend, error) {
	c := &Client{}
	b, err := backends.New(name, o, c.RegisterHandlers, router, cache)
	c.Backend = b
	return c, err
}

// RegisterHandlers registers the provided Handlers into the Router. The
// PostgreSQL backend serves no HTTP routes.
func (c *Client) RegisterHandlers(handlers.Lookup) {}

// DefaultPathConfigs returns the default PathConfigs for this backend
// provider. The PostgreSQL backend serves no HTTP paths.
func (c *Client) DefaultPathConfigs(*bo.Options) po.List {
	return nil
}

type engine struct{}

var _ pgwire.Engine = engine{}

// Engine returns the PostgreSQL engine for the postgres wire-protocol listener.
func Engine() pgwire.Engine { return engine{} }

func (engine) Name() string { return providers.Postgres }

func (engine) DefaultPort() string { return DefaultPort }
