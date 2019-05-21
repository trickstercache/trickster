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
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
)

// RegisterRoutes registers the routes for the Client into the proxy's HTTP multiplexer
func (c Client) RegisterRoutes(originName string, o *config.OriginConfig) {

	// Host Header-based routing
	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.Type, "originName": originName})
	routing.Router.HandleFunc("/"+health, c.HealthHandler).Methods("GET").Host(originName)
	routing.Router.HandleFunc("/"+mnQuery, c.QueryHandler).Methods("GET", "POST").Host(originName)
	routing.Router.PathPrefix("/").HandlerFunc(c.ProxyHandler).Methods("GET", "POST").Host(originName)

	// Path-based routing
	routing.Router.HandleFunc("/"+originName+"/"+health, c.HealthHandler).Methods("GET")
	routing.Router.HandleFunc("/"+originName+"/"+mnQuery, c.QueryHandler).Methods("GET", "POST")
	routing.Router.PathPrefix("/"+originName+"/").HandlerFunc(c.ProxyHandler).Methods("GET", "POST")

	// Default Origin Routing
	if o.IsDefault {
		log.Debug("Registering Default Origin Handlers", log.Pairs{"originType": o.Type})
		routing.Router.HandleFunc("/"+health, c.HealthHandler).Methods("GET")
		routing.Router.HandleFunc("/"+mnQuery, c.QueryHandler).Methods("GET", "POST")
		routing.Router.PathPrefix("/").HandlerFunc(c.ProxyHandler).Methods("GET", "POST")
	}
}
