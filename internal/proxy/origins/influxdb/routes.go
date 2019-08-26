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

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/middleware"
)

// RegisterRoutes registers the routes for the Client into the proxy's HTTP multiplexer
func (c Client) RegisterRoutes(originName string, o *config.OriginConfig) {

	decorate := func(path string, f func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
		return middleware.Decorate(originName, otInfluxDb, path, f)
	}

	// Host Header-based routing
	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.Type, "originName": originName})
	routing.Router.HandleFunc("/"+health, decorate("health", c.HealthHandler)).Methods("GET").Host(originName)
	routing.Router.HandleFunc("/"+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST").Host(originName)
	routing.Router.PathPrefix("/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST").Host(originName)

	// Path-based routing
	routing.Router.HandleFunc("/"+originName+"/"+health, decorate("health", c.HealthHandler)).Methods("GET")
	routing.Router.HandleFunc("/"+originName+"/"+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST")
	routing.Router.PathPrefix("/"+originName+"/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST")

	// Default Origin Routing
	if o.IsDefault {
		log.Debug("Registering Default Origin Handlers", log.Pairs{"originType": o.Type})
		routing.Router.HandleFunc("/"+health, decorate("health", c.HealthHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST")
		routing.Router.PathPrefix("/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST")
	}
}
