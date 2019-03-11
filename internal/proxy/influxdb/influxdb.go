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

package influxdb

import (
	"net/http"
	"net/url"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/timeseries"
)

// Client Implements the Database Client Interface
type Client struct {
	Name   string
	User   string
	Pass   string
	Config config.OriginConfig
	Cache  cache.Cache
}

// Configuration ...
func (c Client) Configuration() config.OriginConfig {
	return c.Config
}

// SetExtent ...
func (c Client) SetExtent(r *proxy.Request, extent *timeseries.Extent) {}

// CacheInstance ...
func (c Client) CacheInstance() cache.Cache {
	return c.Cache
}

// BaseURL ...
func (c Client) BaseURL() *url.URL {
	u := &url.URL{}
	u.Scheme = c.Config.Scheme
	u.Host = c.Config.Host
	u.Path = c.Config.PathPrefix
	return u
}

// UnmarshalInstantaneous ...
func (c Client) UnmarshalInstantaneous() timeseries.Timeseries {
	return SeriesEnvelope{}
}

// BuildUpstreamURL ...
func (c Client) BuildUpstreamURL(r *http.Request) *url.URL {
	return &url.URL{}
}

// OriginName ...
func (c Client) OriginName() string {
	return c.Name
}

// DeriveCacheKey ...
func (c Client) DeriveCacheKey(string, url.Values, string, string) string {
	return ""
}

// ParseTimeRangeQuery ...
func (c Client) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery, error) {
	return nil, nil
}

// HealthHandler ...
func (c Client) HealthHandler(w http.ResponseWriter, r *http.Request) {}

// MarshalTimeseries ...
func (c Client) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	// Marshal the Envelope back to a json object for Cache Storage
	return []byte{}, nil
}

// RegisterRoutes ...
func (c Client) RegisterRoutes(name string, o config.OriginConfig) {}
