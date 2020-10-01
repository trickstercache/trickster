/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package influxdb provides the InfluxDB Backend provider
package influxdb

import (
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/tricksterproxy/trickster/pkg/backends"
	oo "github.com/tricksterproxy/trickster/pkg/backends/options"
	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/proxy"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

var _ backends.Client = (*Client)(nil)

// Client Implements the Proxy Client Interface
type Client struct {
	name               string
	config             *oo.Options
	cache              cache.Cache
	webClient          *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	baseUpstreamURL    *url.URL
	router             http.Handler

	healthURL        *url.URL
	healthHeaders    http.Header
	healthMethod     string
	healthBody       io.Reader
	healthHeaderLock *sync.Mutex
	modeler          *timeseries.Modeler
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *oo.Options, router http.Handler,
	cache cache.Cache, modeler *timeseries.Modeler) (backends.Client, error) {
	c, err := proxy.NewHTTPClient(oc)
	bur := urls.FromParts(oc.Scheme, oc.Host, oc.PathPrefix, "", "")
	// explicitly disable Fast Forward for this client
	oc.FastForwardDisable = true
	return &Client{name: name, config: oc, router: router, cache: cache,
		webClient: c, baseUpstreamURL: bur, healthHeaderLock: &sync.Mutex{},
		modeler: modeler}, err
}

// Configuration returns the upstream Configuration for this Client
func (c *Client) Configuration() *oo.Options {
	return c.config
}

// HTTPClient returns the HTTP Transport the client is using
func (c *Client) HTTPClient() *http.Client {
	return c.webClient
}

// Cache returns a handle to the Cache instance used by the Client
func (c *Client) Cache() cache.Cache {
	return c.cache
}

// Name returns the name of the upstream Configuration proxied by the Client
func (c *Client) Name() string {
	return c.name
}

// SetCache sets the Cache object the client will use for caching origin content
func (c *Client) SetCache(cc cache.Cache) {
	c.cache = cc
}

// Router returns the http.Handler that handles request routing for this Client
func (c *Client) Router() http.Handler {
	return c.router
}
