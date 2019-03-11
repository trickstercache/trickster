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

package proxy

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/timeseries"
)

// Origin Database Types
const (
	OtPrometheus = "prometheus"
	OtInfluxDb   = "influxdb"
)

// Clients ...
var Clients = make(map[string]Client)

// Client ...
type Client interface {

	// Client Endpoint Registration
	RegisterRoutes(string, config.OriginConfig)
	ParseTimeRangeQuery(*http.Request) (*timeseries.TimeRangeQuery, error)
	OriginName() string
	BaseURL() *url.URL
	// Required Handler Implementations
	SetExtent(*Request, *timeseries.Extent)
	HealthHandler(http.ResponseWriter, *http.Request)
	CacheInstance() cache.Cache
	DeriveCacheKey(string, url.Values, string, string) string
	BuildUpstreamURL(*http.Request) *url.URL
	UnmarshalTimeseries([]byte) (timeseries.Timeseries, error)
	MarshalTimeseries(timeseries.Timeseries) ([]byte, error)
	Configuration() config.OriginConfig

	UnmarshalInstantaneous() timeseries.Timeseries
}
