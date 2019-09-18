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

var handlers = make(map[string]http.Handler)
var handlersRegistered = false

func (c *Client) registerHandlers() {
	handlersRegistered = true
	// This is the registry of handlers that Trickster supports for Prometheus,
	// and are able to be referenced by name (map key) in Config Files
	handlers["health"] = http.HandlerFunc(c.HealthHandler)
	handlers[mnQueryRange] = http.HandlerFunc(c.QueryRangeHandler)
	handlers[mnQuery] = http.HandlerFunc(c.QueryHandler)
	handlers[mnSeries] = http.HandlerFunc(c.SeriesHandler)
	handlers["proxycache"] = http.HandlerFunc(c.ObjectProxyCacheHandler)
	handlers["proxy"] = http.HandlerFunc(c.ProxyHandler)
}

// Handlers returns a map of the HTTP Handlers the client has registered
func (c *Client) Handlers() map[string]http.Handler {
	if !handlersRegistered {
		c.registerHandlers()
	}
	return handlers
}

// DefaultPathConfigs returns the default PathConfigs for the given OriginType
func (c *Client) DefaultPathConfigs() (map[string]*config.ProxyPathConfig, []string) {

	paths := map[string]*config.ProxyPathConfig{

		APIPath + mnQueryRange: &config.ProxyPathConfig{
			Path:            APIPath + mnQueryRange,
			HandlerName:     mnQueryRange,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upStep},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().TimeseriesTTLSecs,
			DefaultTTL:      c.cache.Configuration().TimeseriesTTL,
			ResponseHeaders: map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, c.Cache().Configuration().TimeseriesTTLSecs)},
		},

		APIPath + mnQuery: &config.ProxyPathConfig{
			Path:            APIPath + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upTime},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
			ResponseHeaders: map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d", headers.ValueSharedMaxAge, c.Cache().Configuration().ObjectTTLSecs)},
		},

		APIPath + mnSeries: &config.ProxyPathConfig{
			Path:            APIPath + mnSeries,
			HandlerName:     mnSeries,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upMatch, upStart, upEnd},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnLabels: &config.ProxyPathConfig{
			Path:            APIPath + mnLabels,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnLabel: &config.ProxyPathConfig{
			Path:            APIPath + mnLabel,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnTargets: &config.ProxyPathConfig{
			Path:            APIPath + mnTargets,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnRules: &config.ProxyPathConfig{
			Path:            APIPath + mnRules,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnAlerts: &config.ProxyPathConfig{
			Path:            APIPath + mnAlerts,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnAlertManagers: &config.ProxyPathConfig{
			Path:            APIPath + mnAlertManagers,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath + mnStatus: &config.ProxyPathConfig{
			Path:            APIPath + mnStatus,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		},

		APIPath: &config.ProxyPathConfig{
			Path:        APIPath,
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		},

		"/": &config.ProxyPathConfig{
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		},
	}

	orderedPaths := []string{APIPath + mnQueryRange, APIPath + mnQuery,
		APIPath + mnSeries, APIPath + mnLabels, APIPath + mnLabel, APIPath + mnTargets, APIPath + mnRules,
		APIPath + mnAlerts, APIPath + mnAlertManagers, APIPath + mnStatus, APIPath, "/"}

	return paths, orderedPaths

}
