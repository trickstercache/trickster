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

package rule

import (
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/proxy/methods"
	"github.com/Comcast/trickster/internal/proxy/origins"
	oo "github.com/Comcast/trickster/internal/proxy/origins/options"
	"github.com/Comcast/trickster/internal/proxy/paths/matching"
	po "github.com/Comcast/trickster/internal/proxy/paths/options"
)

// Client Implements the Proxy Client Interface
type Client struct {
	name               string
	options            *oo.Options
	handlers           map[string]http.Handler
	handlersRegistered bool

	// this exists so the rule can route the request to a destination by router name
	clients origins.Origins

	rule       *rule
	pathPrefix string
	router     http.Handler
}

// NewClient returns a new Rules Router client reference
func NewClient(name string, options *oo.Options, router http.Handler,
	clients origins.Origins) (*Client, error) {
	return &Client{
		name:       name,
		options:    options,
		clients:    clients,
		pathPrefix: "/" + name,
		router:     router,
	}, nil
}

// Configuration returns the Client Configuration
func (c *Client) Configuration() *oo.Options {
	return c.options
}

// DefaultPathConfigs returns the default PathConfigs for the given OriginType
func (c *Client) DefaultPathConfigs(oc *oo.Options) map[string]*po.Options {

	m := methods.CacheableHTTPMethods()
	m = append(m, methods.CacheableHTTPMethods()...)

	paths := map[string]*po.Options{
		"/" + strings.Join(m, "-"): {
			Path:          "/",
			HandlerName:   "rule",
			Methods:       m,
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}
	return paths
}

func (c *Client) registerHandlers() {
	c.handlersRegistered = true
	c.handlers = make(map[string]http.Handler)
	// This is the registry of handlers that Trickster supports for the Reverse Proxy Cache,
	// and are able to be referenced by name (map key) in Config Files
	c.handlers["rule"] = http.HandlerFunc(c.Handler)
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	if !c.handlersRegistered {
		c.registerHandlers()
	}
	return c.handlers
}

// HTTPClient is not used by the Rule, and is present to conform to the Client interface
func (c *Client) HTTPClient() *http.Client {
	return nil
}

// Cache is not used by the Rule, and is present to conform to the Client interface
func (c *Client) Cache() cache.Cache {
	return nil
}

// Name returns the name of the upstream Configuration proxied by the Client
func (c *Client) Name() string {
	return c.name
}

// SetCache is not used by the Rule, and is present to conform to the Client interface
func (c *Client) SetCache(cc cache.Cache) {}

// Router returns the http.Handler that handles request routing for this Client
func (c *Client) Router() http.Handler {
	return c.router
}
