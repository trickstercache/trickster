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
	"net/http"
	"strings"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/proxy/origins/influxdb"
	"github.com/Comcast/trickster/internal/proxy/origins/irondb"
	"github.com/Comcast/trickster/internal/proxy/origins/prometheus"
	"github.com/Comcast/trickster/internal/proxy/origins/reverseproxycache"
	"github.com/Comcast/trickster/internal/routing"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/middleware"
	ts "github.com/Comcast/trickster/internal/util/strings"
)

// ProxyClients maintains a list of proxy clients configured for use by Trickster
var ProxyClients = make(map[string]model.Client)

// RegisterProxyRoutes iterates the Trickster Configuration and registers the routes for the configured origins
func RegisterProxyRoutes() error {

	defaultOrigin := ""
	var ndo *config.OriginConfig // points to the origin config named "default"
	var cdo *config.OriginConfig // points to the origin config with IsDefault set to true

	// This iteration will ensure default origins are handled properly
	for k, o := range config.Origins {

		// Ensure only one default origin exists
		if o.IsDefault {
			if cdo != nil {
				return fmt.Errorf("only one origin can be marked as default. Found both %s and %s", defaultOrigin, k)
			}
			log.Debug("default origin identified", log.Pairs{"name": k})
			defaultOrigin = k
			cdo = o
			continue
		}

		// handle origin named "default" last as it needs special handling based on a full pass over the range
		if k == "default" {
			ndo = o
			continue
		}

		err := registerOriginRoutes(k, o)
		if err != nil {
			return err
		}
	}

	if ndo != nil {
		if cdo == nil {
			ndo.IsDefault = true
			cdo = ndo
			defaultOrigin = "default"
		} else {
			err := registerOriginRoutes("default", ndo)
			if err != nil {
				return err
			}
		}
	}

	if cdo != nil {
		return registerOriginRoutes(defaultOrigin, cdo)
	}

	return nil
}

func registerOriginRoutes(k string, o *config.OriginConfig) error {

	var client model.Client
	var c cache.Cache
	var err error

	c, err = registration.GetCache(o.CacheName)
	if err != nil {
		return err
	}
	switch strings.ToLower(o.OriginType) {
	case "prometheus", "":
		log.Info("registering Prometheus route paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client = prometheus.NewClient(k, o, c)
	case "influxdb":
		log.Info("registering Influxdb route paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client = influxdb.NewClient(k, o, c)
	case "irondb":
		log.Info("registering IRONdb route paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client = irondb.NewClient(k, o, c)
	case "rpc", "reverseproxycache":
		log.Info("Registering ReverseProxyCache Route Paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client = reverseproxycache.NewClient(k, o, c)
	default:
		log.Error("unknown origin type", log.Pairs{"originName": k, "originType": o.OriginType})
		return fmt.Errorf("unknown origin type in origin config. originName: %s, originType: %s", k, o.OriginType)
	}
	if client != nil {
		ProxyClients[k] = client
		paths, orderedPaths := client.DefaultPathConfigs()
		registerPathRoutes(client.Handlers(), o, c, paths, orderedPaths)
	}

	return nil
}

func registerPathRoutes(handlers map[string]http.Handler, o *config.OriginConfig, c cache.Cache, paths map[string]*config.PathConfig, orderedPaths []string) {

	decorate := func(p *config.PathConfig) http.Handler {
		// Add Origin, Cache, and Path Configs to the HTTP Request's context
		p.Handler = middleware.WithConfigContext(o, c, p, p.Handler)
		if p.NoMetrics {
			return p.Handler
		}
		return middleware.Decorate(o.Name, o.OriginType, p.Path, p.Handler)
	}

	for k, p := range o.Paths {
		p.Path = k
		if p2, ok := paths[k]; ok {
			p.Merge(p2)
			continue
		}
		paths[k] = p
	}

	// Ensure the configured health check endpoint starts with "/""
	if !strings.HasPrefix(o.HealthCheckEndpoint, "/") {
		o.HealthCheckEndpoint = "/" + o.HealthCheckEndpoint
	}
	paths[o.HealthCheckEndpoint] = &config.PathConfig{
		Path:        o.HealthCheckEndpoint,
		HandlerName: "health",
		Methods:     []string{http.MethodGet, http.MethodHead},
	}
	orderedPaths = append([]string{o.HealthCheckEndpoint}, orderedPaths...)

	deletes := make([]string, 0, len(paths))
	for _, p := range paths {
		if h, ok := handlers[p.HandlerName]; ok && h != nil {
			p.Handler = h
			if p.Path != "" && ts.IndexOfString(orderedPaths, p.Path) == -1 {
				log.Info("found unexpected path in config", log.Pairs{"originName": o.Name, "path": p.Path})
				orderedPaths = append(orderedPaths, p.Path)
			}
		} else {
			deletes = append(deletes, p.Path)
		}
	}
	for _, p := range deletes {
		delete(paths, p)
	}

	log.Debug("Registering Origin Handlers", log.Pairs{"originType": o.OriginType, "originName": o.Name})
	for _, v := range orderedPaths {
		p, ok := paths[v]
		if !ok {
			continue
		}
		log.Info("Registering Origin Handler Path", log.Pairs{"path": v, "handlerName": p.HandlerName})
		if p.Handler != nil && len(p.Methods) > 0 {
			// Host Header Routing
			routing.Router.Handle(p.Path, decorate(p)).Methods(p.Methods...).Host(o.Name)
			// Path Routing
			routing.Router.Handle("/"+o.Name+p.Path, decorate(p)).Methods(p.Methods...)
		}
	}

	if o.IsDefault {
		for _, v := range orderedPaths {
			p, ok := paths[v]
			if !ok {
				continue
			}
			if p.Handler != nil && len(p.Methods) > 0 {
				routing.Router.Handle(p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}

}
