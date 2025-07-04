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

package influxdb

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb/influxql"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func (c *Client) RegisterHandlers(handlers.Lookup) {

	c.TimeseriesBackend.RegisterHandlers(
		handlers.Lookup{
			// This is the registry of handlers that Trickster supports for InfluxDB,
			// and are able to be referenced by name (map key) in Config Files
			"health":        http.HandlerFunc(c.HealthHandler),
			"query":         http.HandlerFunc(c.QueryHandler),
			providers.Proxy: http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(_ *bo.Options) po.Lookup {
	return po.List{
		{
			Path:            "/" + mnQuery,
			HandlerName:     mnQuery,
			Methods:         methods.GetAndPost(),
			CacheKeyParams:  []string{influxql.ParamDB, influxql.ParamQuery, "u", "p"},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},
		{
			Path:            "/" + apiv2Query,
			HandlerName:     mnQuery,
			Methods:         methods.GetAndPost(),
			CacheKeyParams:  []string{influxql.ParamDB, influxql.ParamQuery, "u", "p"},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},
		{
			Path:          "/",
			HandlerName:   providers.Proxy,
			Methods:       methods.GetAndPost(),
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}.ToLookup()
}
