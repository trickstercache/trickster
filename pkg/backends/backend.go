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

package backends

import (
	"net/http"
	"net/url"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
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
	// SetHealthCheckProbe sets the Health Check Status Prober for the Client
	SetHealthCheckProbe(healthcheck.DemandProbe)
	// HealthHandler executes a Health Check Probe when called
	HealthHandler(http.ResponseWriter, *http.Request)
	// DefaultHealthCheckConfig returns the default Health Check Config for the given Provider
	DefaultHealthCheckConfig() *ho.Options
	// HealthCheckHTTPClient returns the HTTP Client used for Health Checking
	HealthCheckHTTPClient() *http.Client
}

type backend struct {
	name               string
	config             *bo.Options
	cache              cache.Cache
	webClient          *http.Client
	healthCheckClient  *http.Client
	handlers           map[string]http.Handler
	handlersRegistered bool
	healthProbe        healthcheck.DemandProbe
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

	// this section sets up the health check HTTP client with a reasonable timeout
	hco := o
	if hco == nil {
		hco = bo.New()
		hco.HealthCheck = ho.New()
	}
	hcc, err2 := proxy.NewHTTPClient(hco)
	if err == nil {
		err = err2
	}

	var tms int
	if o != nil && o.HealthCheck != nil {
		tms = o.HealthCheck.TimeoutMS
	}
	if hcc != nil {
		hcc.Timeout = ho.CalibrateTimeout(tms)
	}

	var bur *url.URL
	if o != nil {
		bur = urls.FromParts(o.Scheme, o.Host, o.PathPrefix, "", "")
	}
	return &backend{name: name, config: o, router: router, cache: cache,
		webClient: c, healthCheckClient: hcc, baseUpstreamURL: bur, registrar: registrar}, err

}

// Name returns the name of the Backend
func (b *backend) Name() string {
	return b.name
}

// Configuration returns the Backend's Configuration
func (b *backend) Configuration() *bo.Options {
	return b.config
}

func (b *backend) BaseUpstreamURL() *url.URL {
	return b.baseUpstreamURL
}

// SetCache sets the Cache object the Backend will use for caching origin content
func (b *backend) SetCache(cc cache.Cache) {
	b.cache = cc
}

// Cache returns a handle to the Cache instance used by the Backend
func (b *backend) Cache() cache.Cache {
	return b.cache
}

// HTTPClient returns the HTTP Client for this Backend
func (b *backend) HTTPClient() *http.Client {
	return b.webClient
}

// Handlers returns the list of handlers used by this Backend
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

// RegisterHandlers registers the provided handlers with the backend
func (b *backend) RegisterHandlers(h map[string]http.Handler) {
	if !b.handlersRegistered {
		b.handlersRegistered = true
		b.handlers = h
	}
}

// SetHealthCheckProbe sets the Health Check Status Prober for the Client
func (b *backend) SetHealthCheckProbe(p healthcheck.DemandProbe) {
	b.healthProbe = p
}

// HealthHandler is the Health Check Handler for the backend
func (b *backend) HealthHandler(w http.ResponseWriter, r *http.Request) {
	if b.healthProbe != nil {
		b.healthProbe(w)
	}
}

// DefaultPathConfigs is a stub function and should be overridden by Backend implementations
func (b *backend) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {
	return nil
}

// DefaultHealthCheckConfig is a stub function and should be overridden by Backend implementations
func (b *backend) DefaultHealthCheckConfig() *ho.Options {
	return nil
}

// HealthCheckHTTPClient returns the HTTP Client used for Health Checking
func (b *backend) HealthCheckHTTPClient() *http.Client {
	return b.healthCheckClient
}
