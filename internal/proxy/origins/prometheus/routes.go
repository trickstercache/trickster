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
)

// RegisterRoutes registers the routes for the Client into the proxy's HTTP multiplexer
func (c *Client) RegisterRoutes(originName string, o *config.OriginConfig) {

	if !strings.HasPrefix(o.HealthCheckEndpoint, "/") {
		o.HealthCheckEndpoint = "/" + o.HealthCheckEndpoint
	}

	// Host Header-based routing
	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.OriginType, "originName": originName})
	routing.Router.HandleFunc(o.HealthCheckEndpoint, c.HealthHandler).Methods(http.MethodGet, http.MethodHead).Host(originName)
	routing.Router.HandleFunc(APIPath+mnQueryRange, c.QueryRangeHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)
	routing.Router.HandleFunc(APIPath+mnQuery, c.QueryHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)
	routing.Router.HandleFunc(APIPath+mnSeries, c.SeriesHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)
	routing.Router.HandleFunc(APIPath+mnLabels, c.ObjectProxyCacheHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)
	routing.Router.HandleFunc(APIPath+mnLabel, c.ObjectProxyCacheHandler).Methods(http.MethodGet).Host(originName)
	routing.Router.HandleFunc(APIPath+mnTargets, c.ObjectProxyCacheHandler).Methods(http.MethodGet).Host(originName)
	routing.Router.HandleFunc(APIPath+mnRules, c.ObjectProxyCacheHandler).Methods(http.MethodGet).Host(originName)
	routing.Router.HandleFunc(APIPath+mnAlerts, c.ObjectProxyCacheHandler).Methods(http.MethodGet).Host(originName)
	routing.Router.HandleFunc(APIPath+mnAlertManagers, c.ObjectProxyCacheHandler).Methods(http.MethodGet).Host(originName)
	routing.Router.HandleFunc(APIPath+mnStatus, c.ObjectProxyCacheHandler).Methods(http.MethodGet).Host(originName)
	routing.Router.PathPrefix(APIPath).HandlerFunc(c.ProxyHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)
	routing.Router.PathPrefix("/").HandlerFunc(c.ProxyHandler).Methods(http.MethodGet, http.MethodPost).Host(originName)

	// Path based routing
	routing.Router.HandleFunc("/"+originName+o.HealthCheckEndpoint, c.HealthHandler).Methods(http.MethodGet, http.MethodHead)
	routing.Router.HandleFunc("/"+originName+APIPath+mnQueryRange, c.QueryRangeHandler).Methods(http.MethodGet, http.MethodPost)
	routing.Router.HandleFunc("/"+originName+APIPath+mnQuery, c.QueryHandler).Methods(http.MethodGet, http.MethodPost)
	routing.Router.HandleFunc("/"+originName+APIPath+mnSeries, c.SeriesHandler).Methods(http.MethodGet, http.MethodPost)
	routing.Router.HandleFunc("/"+originName+APIPath+mnLabels, c.ObjectProxyCacheHandler).Methods(http.MethodGet, http.MethodPost)
	routing.Router.HandleFunc("/"+originName+APIPath+mnLabel, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
	routing.Router.HandleFunc("/"+originName+APIPath+mnTargets, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
	routing.Router.HandleFunc("/"+originName+APIPath+mnRules, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
	routing.Router.HandleFunc("/"+originName+APIPath+mnAlerts, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
	routing.Router.HandleFunc("/"+originName+APIPath+mnAlertManagers, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
	routing.Router.HandleFunc("/"+originName+APIPath+mnStatus, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
	routing.Router.PathPrefix("/"+originName+APIPath).HandlerFunc(c.ProxyHandler).Methods(http.MethodGet, http.MethodPost)
	routing.Router.PathPrefix("/"+originName+"/").HandlerFunc(c.ProxyHandler).Methods(http.MethodGet, http.MethodPost)

	// If default origin, set those routes too
	if o.IsDefault {
		log.Debug("Registering Default Origin Handlers", log.Pairs{"originType": o.OriginType})
		routing.Router.HandleFunc(o.HealthCheckEndpoint, c.HealthHandler).Methods(http.MethodGet, http.MethodHead)
		routing.Router.HandleFunc(APIPath+mnQueryRange, c.QueryRangeHandler).Methods(http.MethodGet, http.MethodPost)
		routing.Router.HandleFunc(APIPath+mnQuery, c.QueryHandler).Methods(http.MethodGet, http.MethodPost)
		routing.Router.HandleFunc(APIPath+mnSeries, c.SeriesHandler).Methods(http.MethodGet, http.MethodPost)
		routing.Router.HandleFunc(APIPath+mnLabels, c.ObjectProxyCacheHandler).Methods(http.MethodGet, http.MethodPost)
		routing.Router.HandleFunc(APIPath+mnLabel, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
		routing.Router.HandleFunc(APIPath+mnTargets, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
		routing.Router.HandleFunc(APIPath+mnRules, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
		routing.Router.HandleFunc(APIPath+mnAlerts, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
		routing.Router.HandleFunc(APIPath+mnAlertManagers, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
		routing.Router.HandleFunc(APIPath+mnStatus, c.ObjectProxyCacheHandler).Methods(http.MethodGet)
		routing.Router.PathPrefix(APIPath).HandlerFunc(c.ProxyHandler).Methods(http.MethodGet, http.MethodPost)
		routing.Router.PathPrefix("/").HandlerFunc(c.ProxyHandler).Methods(http.MethodGet, http.MethodPost)
	}

}
