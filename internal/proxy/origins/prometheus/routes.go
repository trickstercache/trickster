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
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/middleware"
)

// RegisterRoutes registers the routes for the Client into the proxy's HTTP multiplexer
func (c *Client) RegisterRoutes(originName string, o *config.OriginConfig) {

	decorate := func(path string, f func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
		return middleware.Decorate(originName, otPrometheus, path, f)
	}

	if o.TLS != nil {
		// Host Header-based routing
		log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.Type, "originName": originName})
		routing.TLSRouter.HandleFunc("/"+mnHealth, decorate("health", c.HealthHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnQueryRange, decorate("query_range", c.QueryRangeHandler)).Methods("GET", "POST").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnSeries, decorate("series", c.SeriesHandler)).Methods("GET", "POST").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnLabels, decorate("labels", c.ObjectProxyCacheHandler)).Methods("GET", "POST").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnLabel, decorate("label", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnTargets, decorate("targets", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnRules, decorate("rules", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnAlerts, decorate("alerts", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnAlertManagers, decorate("alert_managersj", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.HandleFunc(APIPath+mnStatus, decorate("status", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.TLSRouter.PathPrefix(APIPath).HandlerFunc(decorate("api", c.ProxyHandler)).Methods("GET", "POST").Host(originName)
		routing.TLSRouter.PathPrefix("/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST").Host(originName)

		// Path based routing
		routing.TLSRouter.HandleFunc("/"+originName+"/"+mnHealth, decorate("health", c.HealthHandler)).Methods("GET")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnQueryRange, decorate("query_range", c.QueryRangeHandler)).Methods("GET", "POST")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnSeries, decorate("series", c.SeriesHandler)).Methods("GET", "POST")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnLabels, decorate("labels", c.ObjectProxyCacheHandler)).Methods("GET", "POST")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnLabel, decorate("label", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnTargets, decorate("targets", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnRules, decorate("rules", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnAlerts, decorate("alerts", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnAlertManagers, decorate("alert_managers", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.TLSRouter.HandleFunc("/"+originName+APIPath+mnStatus, decorate("status", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.TLSRouter.PathPrefix("/"+originName+APIPath).HandlerFunc(decorate("api", c.ProxyHandler)).Methods("GET", "POST")
		routing.TLSRouter.PathPrefix("/"+originName+"/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST")

		// If default origin, set those routes too
		if o.IsDefault {
			log.Debug("Registering Default Origin Handlers", log.Pairs{"originType": o.Type, "originName": originName})
			routing.TLSRouter.HandleFunc("/"+mnHealth, decorate("health", c.HealthHandler)).Methods("GET")
			routing.TLSRouter.HandleFunc(APIPath+mnQueryRange, decorate("query_range", c.QueryRangeHandler)).Methods("GET", "POST")
			routing.TLSRouter.HandleFunc(APIPath+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST")
			routing.TLSRouter.HandleFunc(APIPath+mnSeries, decorate("series", c.SeriesHandler)).Methods("GET", "POST")
			routing.TLSRouter.HandleFunc(APIPath+mnLabels, decorate("labels", c.ObjectProxyCacheHandler)).Methods("GET", "POST")
			routing.TLSRouter.HandleFunc(APIPath+mnLabel, decorate("label", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.TLSRouter.HandleFunc(APIPath+mnTargets, decorate("targets", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.TLSRouter.HandleFunc(APIPath+mnRules, decorate("rules", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.TLSRouter.HandleFunc(APIPath+mnAlerts, decorate("alerts", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.TLSRouter.HandleFunc(APIPath+mnAlertManagers, decorate("alert_managers", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.TLSRouter.HandleFunc(APIPath+mnStatus, decorate("status", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.TLSRouter.PathPrefix(APIPath).HandlerFunc(decorate("api", c.ProxyHandler)).Methods("GET", "POST")
			routing.TLSRouter.PathPrefix("/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST")
		}
	}

	if !o.RequireTLS {

		fmt.Println("REGISTERING NON-TLS ON ", originName)

		// Host Header-based routing
		log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.Type, "originName": originName})
		routing.Router.HandleFunc("/"+mnHealth, decorate("health", c.HealthHandler)).Methods("GET").Host(originName)
		routing.Router.HandleFunc(APIPath+mnQueryRange, decorate("query_range", c.QueryRangeHandler)).Methods("GET", "POST").Host(originName)
		routing.Router.HandleFunc(APIPath+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST").Host(originName)
		routing.Router.HandleFunc(APIPath+mnSeries, decorate("series", c.SeriesHandler)).Methods("GET", "POST").Host(originName)
		routing.Router.HandleFunc(APIPath+mnLabels, decorate("labels", c.ObjectProxyCacheHandler)).Methods("GET", "POST").Host(originName)
		routing.Router.HandleFunc(APIPath+mnLabel, decorate("label", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.Router.HandleFunc(APIPath+mnTargets, decorate("targets", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.Router.HandleFunc(APIPath+mnRules, decorate("rules", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.Router.HandleFunc(APIPath+mnAlerts, decorate("alerts", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.Router.HandleFunc(APIPath+mnAlertManagers, decorate("alert_managersj", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.Router.HandleFunc(APIPath+mnStatus, decorate("status", c.ObjectProxyCacheHandler)).Methods("GET").Host(originName)
		routing.Router.PathPrefix(APIPath).HandlerFunc(decorate("api", c.ProxyHandler)).Methods("GET", "POST").Host(originName)
		routing.Router.PathPrefix("/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST").Host(originName)

		// Path based routing
		routing.Router.HandleFunc("/"+originName+"/"+mnHealth, decorate("health", c.HealthHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+originName+APIPath+mnQueryRange, decorate("query_range", c.QueryRangeHandler)).Methods("GET", "POST")
		routing.Router.HandleFunc("/"+originName+APIPath+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST")
		routing.Router.HandleFunc("/"+originName+APIPath+mnSeries, decorate("series", c.SeriesHandler)).Methods("GET", "POST")
		routing.Router.HandleFunc("/"+originName+APIPath+mnLabels, decorate("labels", c.ObjectProxyCacheHandler)).Methods("GET", "POST")
		routing.Router.HandleFunc("/"+originName+APIPath+mnLabel, decorate("label", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+originName+APIPath+mnTargets, decorate("targets", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+originName+APIPath+mnRules, decorate("rules", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+originName+APIPath+mnAlerts, decorate("alerts", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+originName+APIPath+mnAlertManagers, decorate("alert_managers", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.Router.HandleFunc("/"+originName+APIPath+mnStatus, decorate("status", c.ObjectProxyCacheHandler)).Methods("GET")
		routing.Router.PathPrefix("/"+originName+APIPath).HandlerFunc(decorate("api", c.ProxyHandler)).Methods("GET", "POST")
		routing.Router.PathPrefix("/"+originName+"/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST")

		// If default origin, set those routes too
		if o.IsDefault {
			log.Debug("Registering Default Origin Handlers", log.Pairs{"originType": o.Type, "originName": originName})
			routing.Router.HandleFunc("/"+mnHealth, decorate("health", c.HealthHandler)).Methods("GET")
			routing.Router.HandleFunc(APIPath+mnQueryRange, decorate("query_range", c.QueryRangeHandler)).Methods("GET", "POST")
			routing.Router.HandleFunc(APIPath+mnQuery, decorate("query", c.QueryHandler)).Methods("GET", "POST")
			routing.Router.HandleFunc(APIPath+mnSeries, decorate("series", c.SeriesHandler)).Methods("GET", "POST")
			routing.Router.HandleFunc(APIPath+mnLabels, decorate("labels", c.ObjectProxyCacheHandler)).Methods("GET", "POST")
			routing.Router.HandleFunc(APIPath+mnLabel, decorate("label", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.Router.HandleFunc(APIPath+mnTargets, decorate("targets", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.Router.HandleFunc(APIPath+mnRules, decorate("rules", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.Router.HandleFunc(APIPath+mnAlerts, decorate("alerts", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.Router.HandleFunc(APIPath+mnAlertManagers, decorate("alert_managers", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.Router.HandleFunc(APIPath+mnStatus, decorate("status", c.ObjectProxyCacheHandler)).Methods("GET")
			routing.Router.PathPrefix(APIPath).HandlerFunc(decorate("api", c.ProxyHandler)).Methods("GET", "POST")
			routing.Router.PathPrefix("/").HandlerFunc(decorate("proxy", c.ProxyHandler)).Methods("GET", "POST")
		}
	}
}
