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

package prometheus

import (
	"fmt"
	"net/http"

	"github.com/trickstercache/trickster/pkg/proxy/headers"
	oo "github.com/trickstercache/trickster/pkg/proxy/origins/options"
	"github.com/trickstercache/trickster/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/pkg/proxy/paths/options"
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

func populateHeathCheckRequestValues(oc *oo.Options) {
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
func (c *Client) DefaultPathConfigs(oc *oo.Options) map[string]*po.Options {

	populateHeathCheckRequestValues(oc)

	var rhts map[string]string
	if oc != nil {
		rhts = map[string]string{
			headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, oc.TimeseriesTTLSecs)}
	}
	rhinst := map[string]string{
		headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, 30)}

	paths := map[string]*po.Options{

		APIPath + mnQueryRange: {
			Path:            APIPath + mnQueryRange,
			HandlerName:     mnQueryRange,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upStep},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhts,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnQuery: {
			Path:            APIPath + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upTime},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnSeries: {
			Path:            APIPath + mnSeries,
			HandlerName:     mnSeries,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upMatch, upStart, upEnd},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnLabels: {
			Path:            APIPath + mnLabels,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnLabel + "/": {
			Path:            APIPath + mnLabel + "/",
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "prefix",
			MatchType:       matching.PathMatchTypePrefix,
			ResponseHeaders: rhinst,
		},

		APIPath + mnTargets: {
			Path:            APIPath + mnTargets,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnTargetsMeta: {
			Path:            APIPath + mnTargetsMeta,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{"match_target", "metric", "limit"},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnRules: {
			Path:            APIPath + mnRules,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnAlerts: {
			Path:            APIPath + mnAlerts,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnAlertManagers: {
			Path:            APIPath + mnAlertManagers,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			ResponseHeaders: rhinst,
			MatchTypeName:   "exact",
			MatchType:       matching.PathMatchTypeExact,
		},

		APIPath + mnStatus: {
			Path:            APIPath + mnStatus,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{},
			MatchTypeName:   "prefix",
			MatchType:       matching.PathMatchTypePrefix,
			ResponseHeaders: rhinst,
		},

		APIPath: {
			Path:          APIPath,
			HandlerName:   "proxy",
			Methods:       []string{http.MethodGet, http.MethodPost},
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},

		"/": {
			Path:          "/",
			HandlerName:   "proxy",
			Methods:       []string{http.MethodGet, http.MethodPost},
			MatchType:     matching.PathMatchTypePrefix,
			MatchTypeName: "prefix",
		},
	}

	oc.FastForwardPath = paths[APIPath+mnQuery].Clone()

	return paths

}
