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

package backends

import (
	"net/http"
	"net/url"

	bo "github.com/tricksterproxy/trickster/pkg/backends/options"
	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/proxy"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/urls"
)

// Backend is the primary interface for interoperating with backends
type Backend interface {
	// RegisterHandlers registers the provided Handlers into the Router
	RegisterHandlers(map[string]http.Handler)
	// Handlers returns a map of the HTTP Handlers the Backend has registered
	Handlers() map[string]http.Handler
	// DefaultPathConfigs returns the default PathConfigs for the given Provider
	DefaultPathConfigs(*bo.Options) map[string]*po.Options
	// Configuration returns the configuration for the Backend
	Configuration() *bo.Options
	// Name returns the name of the Backend
	Name() string
	// HTTPClient will return the HTTP Client for this Backend
	HTTPClient() *http.Client
	// SetCache sets the Cache object the Backend will use when caching origin content
	SetCache(cache.Cache)
	// Router returns a Router that handles HTTP Requests for this Backend
	Router() http.Handler
	// Cache returns a handle to the Cache instance used by the Backend
	Cache() cache.Cache
	// BaseUpstreamURL returns the base URL for upstream requests
	BaseUpstreamURL() *url.URL
}

type backend struct {
	name               string
	config             *bo.Options
	cache              cache.Cache
	webClient          *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	healthURL          *url.URL
	healthHeaders      http.Header
	healthMethod       string
	router             http.Handler
	baseUpstreamURL    *url.URL
	registrar          func(map[string]http.Handler)
}

// Registrar defines a function that registers http.Handlers with a router
type Registrar func(map[string]http.Handler)

// New returns a new Backend
func New(name string, o *bo.Options, registrar Registrar,
	router http.Handler, cache cache.Cache) (Backend, error) {

	c, err := proxy.NewHTTPClient(o)
	var bur *url.URL
	if o != nil {
		bur = urls.FromParts(o.Scheme, o.Host, o.PathPrefix, "", "")
	}
	return &backend{name: name, config: o, router: router, cache: cache,
		webClient: c, baseUpstreamURL: bur, registrar: registrar}, err

}

// Name returns the name of the upstream Configuration proxied by the Client
func (b *backend) Name() string {
	return b.name
}

// Configuration returns the upstream Configuration for this Client
func (b *backend) Configuration() *bo.Options {
	return b.config
}

func (b *backend) BaseUpstreamURL() *url.URL {
	return b.baseUpstreamURL
}

// SetCache sets the Cache object the client will use for caching origin content
func (b *backend) SetCache(cc cache.Cache) {
	b.cache = cc
}

// Cache returns and handle to the Cache instance used by the Client
func (b *backend) Cache() cache.Cache {
	return b.cache
}

// DELETE AFTER interface is otherwise implemented
func (b *backend) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {
	return nil
}

// HTTPClient returns the HTTP Client for this Backend
func (b *backend) HTTPClient() *http.Client {
	return b.webClient
}

func (b *backend) Handlers() map[string]http.Handler {
	if !b.handlersRegistered {
		if b.registrar != nil {
			b.registrar(nil)
		}
	}
	return b.handlers
}

// Router returns the http.Handler that handles request routing for this Client
func (b *backend) Router() http.Handler {
	return b.router
}

func (b *backend) RegisterHandlers(h map[string]http.Handler) {
	if !b.handlersRegistered {
		b.handlersRegistered = true
		b.handlers = h
	}
}
