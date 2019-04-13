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

package registration

import (
	"strings"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/proxy/influxdb"
	"github.com/Comcast/trickster/internal/proxy/prometheus"
	"github.com/Comcast/trickster/internal/util/log"
)

// ProxyClients maintains a list of proxy clients configured for use by Trickster
var ProxyClients = make(map[string]proxy.Client)

// RegisterProxyRoutes iterates the Trickster Configuration and registers the routes for the configured origins
func RegisterProxyRoutes() {

	// Iterate our origins from the config and register their path handlers into the mux.
	for k, o := range config.Origins {

		var client proxy.Client
		var c cache.Cache
		var err error

		c, err = registration.GetCache(o.CacheName)
		if err != nil {
			log.Fatal(1, "invalid cache name in origin config", log.Pairs{"originName": k, "cacheName": o.CacheName})
		}
		switch strings.ToLower(o.Type) {
		case proxy.OtPrometheus, "":
			log.Info("Registering Prometheus Route Paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
			client = prometheus.NewClient(k, o, c)
		case proxy.OtInfluxDb:
			log.Info("Registering Influxdb Route Paths", log.Pairs{"originName": k})
			client = influxdb.NewClient(k, o, c)
		}

		if client != nil {
			ProxyClients[k] = client
			client.RegisterRoutes(k, o)
		}
	}
}
