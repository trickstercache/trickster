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

var allHTTPMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete,
	http.MethodConnect, http.MethodOptions, http.MethodTrace, http.MethodPatch}

// RegisterProxyRoutes iterates the Trickster Configuration and registers the routes for the configured origins
func RegisterProxyRoutes() error {

	defaultOrigin := ""
	var ndo *config.OriginConfig // points to the origin config named "default"
	var cdo *config.OriginConfig // points to the origin config with IsDefault set to true

	// This iteration will ensure default origins are handled properly
	for k, o := range config.Origins {

		if !config.IsValidOriginType(o.OriginType) {
			return fmt.Errorf(`unknown origin type in origin config. originName: %s, originType: %s`, k, o.OriginType)
		}

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

	log.Info("registering route paths", log.Pairs{"originName": k, "originType": o.OriginType, "upstreamHost": o.Host})

	switch strings.ToLower(o.OriginType) {
	case "prometheus", "":
		log.Info("registering Prometheus route paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client, err = prometheus.NewClient(k, o, c)
	case "influxdb":
		log.Info("registering Influxdb route paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client, err = influxdb.NewClient(k, o, c)
	case "irondb":
		log.Info("registering IRONdb route paths", log.Pairs{"originName": k, "upstreamHost": o.Host})
		client, err = irondb.NewClient(k, o, c)
	case "rpc", "reverseproxycache":
		client, err = reverseproxycache.NewClient(k, o, c)
	}
	if err != nil {
		return err
	}
	if client != nil {
		ProxyClients[k] = client
		defaultPaths, orderedPaths := client.DefaultPathConfigs(o)
		registerPathRoutes(client.Handlers(), o, c, defaultPaths, orderedPaths)
	}
	return nil
}

// registerPathRoutes will take the provided default paths map,
// merge it with any path data in the provided originconfig, and then register
// the path routes to the appropriate handler from the provided handlers map
func registerPathRoutes(handlers map[string]http.Handler, o *config.OriginConfig, c cache.Cache,
	paths map[string]*config.PathConfig, orderedPaths []string) {

	decorate := func(p *config.PathConfig) http.Handler {
		// Add Origin, Cache, and Path Configs to the HTTP Request's context
		p.Handler = middleware.WithConfigContext(o, c, p, p.Handler)
		if p.NoMetrics {
			return p.Handler
		}
		return middleware.Decorate(o.Name, o.OriginType, p.Path, p.Handler)
	}

	if h, ok := handlers["health"]; ok &&
		o.HealthCheckUpstreamPath != "" && o.HealthCheckVerb != "" {
		hp := "/trickster/health/" + o.Name
		routing.Router.PathPrefix(hp).Handler(middleware.WithConfigContext(o, nil, nil, h)).Methods(http.MethodGet, http.MethodHead, http.MethodPost)
	}

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

	for _, v := range orderedPaths {
		p, ok := paths[v]
		if !ok {
			continue
		}
		log.Debug("registering origin handler path",
			log.Pairs{"originName": o.Name, "path": v, "handlerName": p.HandlerName,
				"originHost": o.Host, "handledPath": "/" + o.Name + p.Path, "matchType": p.MatchType})
		if p.Handler != nil && len(p.Methods) > 0 {

			if p.Methods[0] == "*" {
				p.Methods = allHTTPMethods
			}

			switch p.MatchType {
			case config.PathMatchTypePrefix:
				// Case where we path match by prefix
				// Host Header Routing
				routing.Router.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...).Host(o.Name)
				// Path Routing
				routing.Router.PathPrefix("/" + o.Name + p.Path).Handler(decorate(p)).Methods(p.Methods...)
			default:
				// default to exact match
				// Host Header Routing
				routing.Router.Handle(p.Path, decorate(p)).Methods(p.Methods...).Host(o.Name)
				// Path Routing
				routing.Router.Handle("/"+o.Name+p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}

	if o.IsDefault {
		log.Info("registering default origin handler paths", log.Pairs{"originName": o.Name})
		for _, v := range orderedPaths {
			p, ok := paths[v]
			if !ok {
				continue
			}
			if p.Handler != nil && len(p.Methods) > 0 {
				log.Debug("registering default origin handler paths", log.Pairs{"originName": o.Name, "path": p.Path, "handlerName": p.HandlerName, "matchType": p.MatchType})
				switch p.MatchType {
				case config.PathMatchTypePrefix:
					// Case where we path match by prefix
					routing.Router.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...)
				default:
					// default to exact match
					routing.Router.Handle(p.Path, decorate(p)).Methods(p.Methods...)
				}
				routing.Router.Handle(p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}
}
