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
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
	ts "github.com/Comcast/trickster/internal/util/strings"
)

var handlers = map[string]func(w http.ResponseWriter, r *http.Request){}

// RegisterRoutes registers the routes for the Client into the proxy's HTTP multiplexer
func (c *Client) RegisterRoutes(originName string, o *config.OriginConfig) {

	const hnAuthorization = "Authorization"

	// Ensure the configured health check endpoint starts with "/""
	if !strings.HasPrefix(o.HealthCheckEndpoint, "/") {
		o.HealthCheckEndpoint = "/" + o.HealthCheckEndpoint
	}

	handlers["health"] = c.HealthHandler
	handlers[mnQueryRange] = c.QueryRangeHandler
	handlers[mnQuery] = c.QueryHandler
	handlers[mnSeries] = c.SeriesHandler
	handlers["proxycache"] = c.ObjectProxyCacheHandler
	handlers["proxy"] = c.ProxyHandler

	o.PathsLookup[o.HealthCheckEndpoint] = &config.ProxyPathConfig{
		Path:        o.HealthCheckEndpoint,
		HandlerName: "health",
		Methods:     []string{http.MethodGet, http.MethodHead},
	}

	if _, ok := o.PathsLookup[APIPath+mnQueryRange]; !ok {
		o.PathsLookup[APIPath+mnQueryRange] = &config.ProxyPathConfig{
			Path:            APIPath + mnQueryRange,
			HandlerName:     mnQueryRange,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upStep},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().TimeseriesTTLSecs,
			DefaultTTL:      c.cache.Configuration().TimeseriesTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnQuery]; !ok {
		o.PathsLookup[APIPath+mnQuery] = &config.ProxyPathConfig{
			Path:            APIPath + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upTime},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnSeries]; !ok {
		o.PathsLookup[APIPath+mnSeries] = &config.ProxyPathConfig{
			Path:            APIPath + mnSeries,
			HandlerName:     mnSeries,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upMatch, upStart, upEnd},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnLabels]; !ok {
		o.PathsLookup[APIPath+mnLabels] = &config.ProxyPathConfig{
			Path:            APIPath + mnLabels,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnLabel]; !ok {
		o.PathsLookup[APIPath+mnLabel] = &config.ProxyPathConfig{
			Path:            APIPath + mnLabel,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnTargets]; !ok {
		o.PathsLookup[APIPath+mnTargets] = &config.ProxyPathConfig{
			Path:            APIPath + mnTargets,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnRules]; !ok {
		o.PathsLookup[APIPath+mnRules] = &config.ProxyPathConfig{
			Path:            APIPath + mnRules,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnAlerts]; !ok {
		o.PathsLookup[APIPath+mnAlerts] = &config.ProxyPathConfig{
			Path:            APIPath + mnAlerts,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnAlertManagers]; !ok {
		o.PathsLookup[APIPath+mnAlertManagers] = &config.ProxyPathConfig{
			Path:            APIPath + mnAlertManagers,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath+mnStatus]; !ok {
		o.PathsLookup[APIPath+mnStatus] = &config.ProxyPathConfig{
			Path:            APIPath + mnStatus,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{"Authorization"},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.PathsLookup[APIPath]; !ok {
		o.PathsLookup[APIPath] = &config.ProxyPathConfig{
			Path:        APIPath,
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		}
	}

	if _, ok := o.PathsLookup[APIPath]; !ok {
		o.PathsLookup[APIPath] = &config.ProxyPathConfig{
			Path:        APIPath,
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		}
	}

	if _, ok := o.PathsLookup["/"]; !ok {
		o.PathsLookup["/"] = &config.ProxyPathConfig{
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		}
	}

	orderedPaths := []string{o.HealthCheckEndpoint, APIPath + mnQueryRange, APIPath + mnQuery,
		APIPath + mnSeries, APIPath + mnLabels, APIPath + mnLabel, APIPath + mnTargets, APIPath + mnRules,
		APIPath + mnAlerts, APIPath + mnAlertManagers, APIPath + mnStatus, APIPath, "/"}

	for _, p := range o.PathsLookup {
		if p.Path != "" && ts.IndexOfString(orderedPaths, p.Path) == -1 {
			orderedPaths = append(orderedPaths, p.Path)
		}
		if h, ok := handlers[p.HandlerName]; ok {
			p.Handler = h
		}
	}

	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.OriginType, "originName": originName})

	for _, v := range orderedPaths {
		p := o.PathsLookup[v]
		if p.Handler != nil && len(p.Methods) > 0 {
			// Host Header Routing
			routing.Router.HandleFunc(p.Path, p.Handler).Methods(p.Methods...).Host(originName)
			// Path Routing
			routing.Router.HandleFunc("/"+originName+p.Path, p.Handler).Methods(p.Methods...)
		}
	}

	if o.IsDefault {
		for _, v := range orderedPaths {
			p := o.PathsLookup[v]
			if p.Handler != nil && len(p.Methods) > 0 {
				routing.Router.HandleFunc(p.Path, p.Handler).Methods(p.Methods...)
			}
		}
	}

}
