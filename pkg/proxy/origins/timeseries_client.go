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

package origins

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/cache"
	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// TimeseriesClient is the primary interface for interoperating with Trickster and upstream TSDB's
type TimeseriesClient interface {
	// Handlers returns a map of the HTTP Handlers the client has registered
	Handlers() map[string]http.Handler
	// DefaultPathConfigs returns the default PathConfigs for the given OriginType
	DefaultPathConfigs(*oo.Options) map[string]*po.Options
	// ParseTimeRangeQuery returns a timeseries.TimeRangeQuery based on the provided HTTP Request
	ParseTimeRangeQuery(*http.Request) (*timeseries.TimeRangeQuery, *timeseries.RequestOptions, bool, error)
	// Configuration returns the configuration for the Proxy Client
	Configuration() *oo.Options
	// Name returns the name of the origin the Proxy Client is handling
	Name() string
	// FastForwardRequest returns an *http.Request crafted to collect Fast Forward data
	// from the Origin, based on the provided HTTP Request. If the inbound request is
	// POST/PUT/PATCH, a Content-Type header and non-nil body with the query parameters
	// must be set, in lieu of updated url query values, in the returned request
	FastForwardRequest(*http.Request) (*http.Request, error)
	// SetExtent will update an upstream request's timerange
	// parameters based on the provided timeseries.Extent
	SetExtent(*http.Request, *timeseries.TimeRangeQuery, *timeseries.Extent)
	// HTTPClient will return the HTTP Client for this Origin
	HTTPClient() *http.Client
	// SetCache sets the Cache object the client will use when caching origin content
	SetCache(cache.Cache)
	// Cache returns a handle to the Cache object used by the client
	Cache() cache.Cache
	// Router returns a Router that handles HTTP Requests for this client
	Router() http.Handler
}
