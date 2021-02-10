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
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
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

// FastForwardRequest is not used for InfluxDB and is here to conform to the Proxy Client interface
func (b *timeseriesBackend) FastForwardRequest(r *http.Request) (*http.Request, error) {
	return nil, nil
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (b *timeseriesBackend) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error) {
	return nil, nil, false, nil
}

func (b *timeseriesBackend) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent) {
}

// Modeler returns the modeler for the time series provider
func (b *timeseriesBackend) Modeler() *timeseries.Modeler {
	return b.modeler
}
