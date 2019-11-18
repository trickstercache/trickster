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

package prometheus

import (
	"fmt"
	"net/http"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
)

func (c *Client) registerHandlers() {
	c.handlersRegistered = true
	c.handlers = make(map[string]http.Handler)
	// This is the registry of handlers that Trickster supports for Prometheus,
	// and are able to be referenced by name (map key) in Config Files
	c.handlers["health"] = http.HandlerFunc(c.HealthHandler)
	c.handlers["query_range"] = http.HandlerFunc(c.QueryRangeHandler)
	c.handlers["query"] = http.HandlerFunc(c.QueryHandler)
	c.handlers["series"] = http.HandlerFunc(c.SeriesHandler)
	c.handlers["proxycache"] = http.HandlerFunc(c.ObjectProxyCacheHandler)
	c.handlers["proxy"] = http.HandlerFunc(c.ProxyHandler)
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	if !c.handlersRegistered {
		c.registerHandlers()
	}
	return c.handlers
}

func populateHeathCheckRequestValues(oc *config.OriginConfig) {
	if oc.HealthCheckUpstreamPath == "-" {
		oc.HealthCheckUpstreamPath = APIPath + mnQuery
	}
	if oc.HealthCheckVerb == "-" {
		oc.HealthCheckVerb = http.MethodGet
	}
	if oc.HealthCheckQuery == "-" {
		oc.HealthCheckQuery = "query=up"
	}
}

// DefaultPathConfigs returns the default PathConfigs for the given OriginType
func (c *Client) DefaultPathConfigs(oc *config.OriginConfig) map[string]*config.PathConfig {

	populateHeathCheckRequestValues(oc)

	var rhts map[string]string
	if oc != nil {
		rhts = map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, oc.TimeseriesTTLSecs)}
	}
	rhinst := map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, 30)}

	paths := map[string]*config.PathConfig{

		APIPath + mnQueryRange: &config.PathConfig{
			Path:            APIPath + mnQueryRange,
			HandlerName:     mnQueryRange,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upStep},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhts,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnQuery: &config.PathConfig{
			Path:            APIPath + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upTime},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnSeries: &config.PathConfig{
			Path:            APIPath + mnSeries,
			HandlerName:     mnSeries,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upMatch, upStart, upEnd},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnLabels: &config.PathConfig{
			Path:            APIPath + mnLabels,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnLabel + "/": &config.PathConfig{
			Path:            APIPath + mnLabel + "/",
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "prefix",
			MatchType:       config.PathMatchTypePrefix,
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
		},

		APIPath + mnTargets: &config.PathConfig{
			Path:            APIPath + mnTargets,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnTargetsMeta: &config.PathConfig{
			Path:            APIPath + mnTargetsMeta,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{"match_target", "metric", "limit"},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnRules: &config.PathConfig{
			Path:            APIPath + mnRules,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnAlerts: &config.PathConfig{
			Path:            APIPath + mnAlerts,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnAlertManagers: &config.PathConfig{
			Path:            APIPath + mnAlertManagers,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
			MatchTypeName:   "exact",
			MatchType:       config.PathMatchTypeExact,
		},

		APIPath + mnStatus: &config.PathConfig{
			Path:            APIPath + mnStatus,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "prefix",
			MatchType:       config.PathMatchTypePrefix,
			ResponseHeaders: rhinst,
			OriginConfig:    oc,
		},

		APIPath: &config.PathConfig{
			Path:          APIPath,
			HandlerName:   "proxy",
			Methods:       []string{http.MethodGet, http.MethodPost},
			OriginConfig:  oc,
			MatchType:     config.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},

		"/": &config.PathConfig{
			Path:          "/",
			HandlerName:   "proxy",
			Methods:       []string{http.MethodGet, http.MethodPost},
			OriginConfig:  oc,
			MatchType:     config.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}

	oc.FastForwardPath = paths[APIPath+mnQuery]

	return paths

}
