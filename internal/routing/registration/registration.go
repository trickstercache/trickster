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
	"fmt"
	"strings"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/proxy/origins/influxdb"
	"github.com/Comcast/trickster/internal/proxy/origins/prometheus"
	"github.com/Comcast/trickster/internal/proxy/origins/reverseproxycache"
	"github.com/Comcast/trickster/internal/util/log"
)

// ProxyClients maintains a list of proxy clients configured for use by Trickster
var ProxyClients = make(map[string]model.Client)

// RegisterProxyRoutes iterates the Trickster Configuration and registers the routes for the configured origins
func RegisterProxyRoutes() error {

	hasDefault := false

	// Iterate our origins from the config and register their path handlers into the mux.
	for k, o := range config.Origins {

		fmt.Println(k)

		// Ensure only one default origin exists
		if o.IsDefault {
			if hasDefault {
				log.Error("too many default origins", log.Pairs{})
				return fmt.Errorf("too many default origins%s", "")
			}
			hasDefault = true
		}

		var client model.Client
		var c cache.Cache
		var err error

		c, err = registration.GetCache(o.CacheName)
		if err != nil {
			log.Error("invalid cache name in origin config", log.Pairs{"originName": k, "cacheName": o.CacheName})
			return fmt.Errorf("invalid cache name in origin config. originName: %s, cacheName: %s", k, o.CacheName)
		}
		switch strings.ToLower(o.OriginType) {
		case "prometheus", "":
			log.Info("Registering Prometheus Route Paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
			client = prometheus.NewClient(k, o, c)
		case "influxdb":
			log.Info("Registering Influxdb Route Paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
			client = influxdb.NewClient(k, o, c)
		case "rpc", "reverseproxycache":
			log.Info("Registering ReverseProxyCache Route Paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
			client = reverseproxycache.NewClient(k, o, c)
		default:
			log.Error("unknown origin type", log.Pairs{"originName": k, "originType": o.OriginType})
			return fmt.Errorf("unknown origin type in origin config. originName: %s, originType: %s", k, o.OriginType)
		}
		if client != nil {
			ProxyClients[k] = client
			// If it's the default origin, register it last
			if o.IsDefault {
				defer client.RegisterRoutes(k, o)
			} else {
				client.RegisterRoutes(k, o)
			}
		}
	}
	return nil
}
