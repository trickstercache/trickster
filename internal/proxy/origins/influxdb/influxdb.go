/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

// Package influxdb provides the InfluxDB Origin Type
package influxdb

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
)

// Client Implements the Proxy Client Interface
type Client struct {
	name               string
	config             *config.OriginConfig
	cache              cache.Cache
	webClient          *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	healthURL          *url.URL
	healthHeaders      http.Header
	healthMethod       string
	logUpstreamRequest bool
}

// NewClient returns a new Client Instance
func NewClient(name string, oc *config.OriginConfig, cache cache.Cache) (*Client, error) {
	c, err := proxy.NewHTTPClient(oc)
	return &Client{name: name, config: oc, cache: cache, webClient: c}, err
}

// Configuration returns the upstream Configuration for this Client
func (c *Client) Configuration() *config.OriginConfig {
	return c.config
}

// HTTPClient returns the HTTP Transport the client is using
func (c *Client) HTTPClient() *http.Client {
	return c.webClient
}

// Cache returns and handle to the Cache instance used by the Client
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

// SetUpstreamLogging enables or disables the logging of upstream requests
func (c *Client) SetUpstreamLogging(logUpstreamRequest bool) {
	c.logUpstreamRequest = logUpstreamRequest
}
