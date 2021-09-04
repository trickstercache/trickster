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
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// TimeseriesBackend is the primary interface for interoperating with Trickster and upstream TSDB's
type TimeseriesBackend interface {
	// RegisterHandlers registers the provided Handlers into the Router
	RegisterHandlers(map[string]http.Handler)
	// Handlers returns a map of the HTTP Handlers the Backend has registered
	Handlers() map[string]http.Handler
	// DefaultPathConfigs returns the default PathConfigs for the given Provider
	DefaultPathConfigs(*bo.Options) map[string]*po.Options
	// ParseTimeRangeQuery returns a timeseries.TimeRangeQuery based on the provided HTTP Request
	ParseTimeRangeQuery(*http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error)
	// Configuration returns the configuration for the Backend
	Configuration() *bo.Options
	// Name returns the name of the Backend
	Name() string
	// FastForwardRequest returns an *http.Request crafted to collect Fast Forward data
	// from the Origin, based on the provided HTTP Request. If the inbound request is
	// POST/PUT/PATCH, a Content-Type header and non-nil body with the query parameters
	// must be set, in lieu of updated url query values, in the returned request
	FastForwardRequest(*http.Request) (*http.Request, error)
	// SetExtent will update an upstream request's timerange
	// parameters based on the provided timeseries.Extent
	SetExtent(*http.Request, *timeseries.TimeRangeQuery, *timeseries.Extent)
	// HTTPClient will return the HTTP Client for this Backend
	HTTPClient() *http.Client
	// SetCache sets the Cache object the Backend will use when caching origin content
	SetCache(cache.Cache)
	// Cache returns a handle to the Cache object used by the Backend
	Cache() cache.Cache
	// Router returns a Router that handles HTTP Requests for this Backend
	Router() http.Handler
	// BaseUpstreamURL returns the base URL for upstream requests
	BaseUpstreamURL() *url.URL
	// Modeler returns the Modeler for converting between Datasets and wire documents
	Modeler() *timeseries.Modeler
	// DefaultHealthCheckConfig returns the default HealthCHeck Config for the given Provider
	DefaultHealthCheckConfig() *ho.Options
	// SetHealthCheckProbe sets the Health Check Status Prober for the Client
	SetHealthCheckProbe(healthcheck.DemandProbe)
	// HealthHandler executes a Health Check Probe when called
	HealthHandler(http.ResponseWriter, *http.Request)
	// HealthCheckHTTPClient returns the HTTP Client used for Health Checking
	HealthCheckHTTPClient() *http.Client
	// ProcessTransformations executes any provider-specific transformations, like injecting
	// labels into the dataset
	ProcessTransformations(timeseries.Timeseries)
}

// MergeableTimeseriesBackend defines the interface for mergeable time series
type MergeableTimeseriesBackend interface {
	// MergePaths should return a slice of HTTP Paths that are safe to merge with
	// other requests of the same path (e.g.,   /api/v1/query_range in prometheus)
	MergeablePaths() []string
}

var _ TimeseriesBackend = (*timeseriesBackend)(nil)

type timeseriesBackend struct {
	Backend
	modeler *timeseries.Modeler
}

// NewTimeseriesBackend returns a new BaseTimeseriesBackend Instance
func NewTimeseriesBackend(name string, o *bo.Options, registrar Registrar, router http.Handler,
	cache cache.Cache, modeler *timeseries.Modeler) (TimeseriesBackend, error) {
	backend, err := New(name, o, registrar, router, cache)
	return &timeseriesBackend{Backend: backend, modeler: modeler}, err
}

// FastForwardRequest is the default implementation for the Timeseries Backend interface
func (b *timeseriesBackend) FastForwardRequest(r *http.Request) (*http.Request, error) {
	return nil, nil
}

// ParseTimeRangeQuery is the default implementation for the Timeseries Backend interface
func (b *timeseriesBackend) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error) {
	return nil, nil, false, nil
}

// SetExtent is the default implementation for the Timeseries Backend interface
func (b *timeseriesBackend) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent) {
}

// Modeler is the default implementation for the Timeseries Backend interface
func (b *timeseriesBackend) Modeler() *timeseries.Modeler {
	return b.modeler
}

// ProcessTransformations is the default implementation for the Timeseries Backend interface
func (b *timeseriesBackend) ProcessTransformations(timeseries.Timeseries) {}
