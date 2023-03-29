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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func (c *Client) RegisterHandlers(map[string]http.Handler) {

	c.TimeseriesBackend.RegisterHandlers(
		map[string]http.Handler{
			// This is the registry of handlers that Trickster supports for InfluxDB,
			// and are able to be referenced by name (map key) in Config Files
			"health": http.HandlerFunc(c.HealthHandler),
			"query":  http.HandlerFunc(c.QueryHandler),
			"proxy":  http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *Client) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {

	paths := map[string]*po.Options{
		"/" + mnQuery: {
			Path:            "/" + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upDB, upQuery, "u", "p"},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},
		"/" + apiv2Query: {
			Path:            "/" + apiv2Query,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upDB, upQuery, "u", "p"},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},
		"/": {
			Path:          "/",
			HandlerName:   "proxy",
			Methods:       []string{http.MethodGet, http.MethodPost},
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}
	return paths
}
