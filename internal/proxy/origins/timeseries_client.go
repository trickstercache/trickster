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

package origins

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/timeseries"
)

// TimeseriesClient is the primary interface for interoperating with Trickster and upstream TSDB's
type TimeseriesClient interface {
	// Handlers returns a map of the HTTP Handlers the client has registered
	Handlers() map[string]http.Handler
	// DefaultPathConfigs returns the default PathConfigs for the given OriginType
	DefaultPathConfigs(*config.OriginConfig) map[string]*config.PathConfig
	// ParseTimeRangeQuery returns a timeseries.TimeRangeQuery based on the provided HTTP Request
	ParseTimeRangeQuery(*http.Request) (*timeseries.TimeRangeQuery, error)
	// Configuration returns the configuration for the Proxy Client
	Configuration() *config.OriginConfig
	// Name returns the name of the origin the Proxy Client is handling
	Name() string
	// FastForwardURL returns the URL to the origin to collect Fast Forward data points based on the provided HTTP Request
	FastForwardURL(*http.Request) (*url.URL, error)
	// SetExtent will update an upstream request's timerange parameters based on the provided timeseries.Extent
	SetExtent(*http.Request, *timeseries.TimeRangeQuery, *timeseries.Extent)
	// UnmarshalTimeseries will return a Timeseries from the provided byte slice
	UnmarshalTimeseries([]byte) (timeseries.Timeseries, error)
	// MarshalTimeseries will return a byte slice from  the provided Timeseries
	MarshalTimeseries(timeseries.Timeseries) ([]byte, error)
	// UnmarshalInstantaneous will return an Instantaneous Timeseries (only one value instead of a series) from the provided byte slice
	UnmarshalInstantaneous([]byte) (timeseries.Timeseries, error)
	// HTTPClient will return the HTTP Client for this Origin
	HTTPClient() *http.Client
	// SetCache sets the Cache object the client will use when caching origin content
	SetCache(cache.Cache)
}
