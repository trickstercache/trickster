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
	handlers[mnQuery] = c.QueryHandler
	handlers["proxy"] = c.ProxyHandler

	o.Paths[o.HealthCheckEndpoint] = &config.ProxyPathConfig{
		Path:        o.HealthCheckEndpoint,
		HandlerName: "health",
		Methods:     []string{http.MethodGet, http.MethodHead},
	}

	if _, ok := o.Paths["/"+mnQuery]; !ok {
		o.Paths["/"+mnQuery] = &config.ProxyPathConfig{
			Path:            "/" + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upDB, upQuery, "u", "p"},
			CacheKeyHeaders: []string{hnAuthorization},
			DefaultTTLSecs:  c.cache.Configuration().ObjectTTLSecs,
			DefaultTTL:      c.cache.Configuration().ObjectTTL,
		}
	}

	if _, ok := o.Paths["/"]; !ok {
		o.Paths["/"] = &config.ProxyPathConfig{
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		}
	}

	orderedPaths := []string{o.HealthCheckEndpoint, "/" + mnQuery, "/"}

	for _, p := range o.Paths {
		if p.Path != "" && ts.IndexOfString(orderedPaths, p.Path) == -1 {
			orderedPaths = append(orderedPaths, p.Path)
		}
		if h, ok := handlers[p.HandlerName]; ok {
			p.Handler = h
		}
	}

	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.OriginType, "originName": originName})

	for _, v := range orderedPaths {
		p := o.Paths[v]
		if p.Handler != nil && len(p.Methods) > 0 {
			// Host Header Routing
			routing.Router.HandleFunc(p.Path, p.Handler).Methods(p.Methods...).Host(originName)
			// Path Routing
			routing.Router.HandleFunc("/"+originName+p.Path, p.Handler).Methods(p.Methods...)
		}
	}

	if o.IsDefault {
		for _, v := range orderedPaths {
			p := o.Paths[v]
			if p.Handler != nil && len(p.Methods) > 0 {
				routing.Router.HandleFunc(p.Path, p.Handler).Methods(p.Methods...)
			}
		}
	}

}
