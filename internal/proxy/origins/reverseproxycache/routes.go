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

package reverseproxycache

import (
	"fmt"
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
	handlers["proxy"] = c.ProxyHandler
	handlers["proxycache"] = c.ProxyCacheHandler
	handlers["localresponse"] = c.LocalResponseHandler

	o.Paths[o.HealthCheckEndpoint] = &config.ProxyPathConfig{
		Path:        o.HealthCheckEndpoint,
		HandlerName: "health",
		Methods:     []string{http.MethodGet, http.MethodHead},
	}

	// By default we proxy everything
	if _, ok := o.Paths["/"]; !ok {
		o.Paths["/"] = &config.ProxyPathConfig{
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		}
	}

	orderedPaths := []string{o.HealthCheckEndpoint}

	for _, p := range o.Paths {
		if p.Path != "" && ts.IndexOfString(orderedPaths, p.Path) == -1 {
			orderedPaths = append(orderedPaths, p.Path)
		}
		if h, ok := handlers[p.HandlerName]; ok {
			p.Handler = h
		}
	}

	orderedPaths = append(orderedPaths, "/")

	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.OriginType, "originName": originName})

	for _, v := range orderedPaths {
		p := o.Paths[v]
		if p.Handler != nil && len(p.Methods) > 0 {
			fmt.Println("REGISTERING ROUTE", v, p.Path, p.Methods)
			// Host Header Routing
			routing.Router.HandleFunc(p.Path, p.Handler).Methods(p.Methods...).Host(originName)
			// Path Routing
			routing.Router.HandleFunc("/"+originName+p.Path, p.Handler).Methods(p.Methods...)
		}
		// Host Header Routing
		routing.Router.PathPrefix("/").HandlerFunc(c.ProxyCacheHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)
		// Path Routing
		routing.Router.PathPrefix("/"+originName+"/").HandlerFunc(c.ProxyCacheHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)

	}

	if o.IsDefault {
		for _, v := range orderedPaths {
			p := o.Paths[v]
			if p.Handler != nil && len(p.Methods) > 0 {
				fmt.Println("OH HRM", p.Path, p.HandlerName, p.Methods)
				routing.Router.HandleFunc(p.Path, p.Handler).Methods(p.Methods...)
			}
		}
		routing.Router.PathPrefix("/").HandlerFunc(c.ProxyCacheHandler).Methods(http.MethodGet, http.MethodPost)
	}

}

// RegisterRoutes registers the routes for the Client into the proxy's HTTP multiplexer
func (c *Client) OldRegisterRoutes(originName string, o *config.OriginConfig) {

	// Host Header-based routing
	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.OriginType, "originName": originName})
	routing.Router.HandleFunc("/health", c.HealthHandler).Host(originName)
	routing.Router.PathPrefix("/").HandlerFunc(c.ProxyCacheHandler).Host(originName)

	// Path based routing
	routing.Router.HandleFunc("/"+originName+"/health", c.HealthHandler)
	routing.Router.PathPrefix("/" + originName + "/").HandlerFunc(c.ProxyCacheHandler)

	// If default origin, set those routes too
	if o.IsDefault {
		log.Debug("Registering Default Origin Handlers", log.Pairs{"originType": o.OriginType})
		routing.Router.HandleFunc("/health", c.HealthHandler)
		routing.Router.PathPrefix("/").HandlerFunc(c.ProxyCacheHandler)
	}

}
