/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Package routing is the Trickster Request Router
package routing

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/methods"
	"github.com/Comcast/trickster/internal/proxy/origins"
	"github.com/Comcast/trickster/internal/proxy/origins/clickhouse"
	"github.com/Comcast/trickster/internal/proxy/origins/influxdb"
	"github.com/Comcast/trickster/internal/proxy/origins/irondb"
	oo "github.com/Comcast/trickster/internal/proxy/origins/options"
	"github.com/Comcast/trickster/internal/proxy/origins/prometheus"
	"github.com/Comcast/trickster/internal/proxy/origins/reverseproxycache"
	"github.com/Comcast/trickster/internal/proxy/origins/rule"
	"github.com/Comcast/trickster/internal/proxy/origins/types"
	"github.com/Comcast/trickster/internal/proxy/paths/matching"
	po "github.com/Comcast/trickster/internal/proxy/paths/options"
	tl "github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/middleware"

	//"github.com/Comcast/trickster/internal/proxy/origins/rule"

	"github.com/gorilla/mux"
)

// proxyClients maintains a list of proxy clients configured for use by Trickster
var proxyClients = make(map[string]origins.Client)

// RegisterProxyRoutes iterates the Trickster Configuration and registers the routes for the configured origins
func RegisterProxyRoutes(conf *config.TricksterConfig, router *mux.Router, caches map[string]cache.Cache, log *tl.TricksterLogger) error {

	defaultOrigin := ""
	var ndo *oo.Options // points to the origin config named "default"
	var cdo *oo.Options // points to the origin config with IsDefault set to true

	// This iteration will ensure default origins are handled properly
	for k, o := range conf.Origins {

		if !types.IsValidOriginType(o.OriginType) {
			return fmt.Errorf(`unknown origin type in origin config. originName: %s, originType: %s`, k, o.OriginType)
		}

		// Ensure only one default origin exists
		if o.IsDefault {
			if cdo != nil {
				return fmt.Errorf("only one origin can be marked as default. Found both %s and %s", defaultOrigin, k)
			}
			log.Debug("default origin identified", tl.Pairs{"name": k})
			defaultOrigin = k
			cdo = o
			continue
		}

		// handle origin named "default" last as it needs special handling based on a full pass over the range
		if k == "default" {
			ndo = o
			continue
		}

		err := registerOriginRoutes(router, conf, k, o, caches, log)
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
			err := registerOriginRoutes(router, conf, "default", ndo, caches, log)
			if err != nil {
				return err
			}
		}
	}

	if cdo != nil {
		return registerOriginRoutes(router, conf, defaultOrigin, cdo, caches, log)
	}

	return nil
}

func registerOriginRoutes(router *mux.Router, conf *config.TricksterConfig, k string, o *oo.Options, caches map[string]cache.Cache, log *tl.TricksterLogger) error {

	var client origins.Client
	var c cache.Cache
	var ok bool
	var err error

	c, ok = caches[o.CacheName]
	if !ok {
		return fmt.Errorf("Could not find Cache named [%s]", o.CacheName)
	}

	log.Info("registering route paths", tl.Pairs{"originName": k, "originType": o.OriginType, "upstreamHost": o.Host})

	switch strings.ToLower(o.OriginType) {
	case "prometheus", "":
		client, err = prometheus.NewClient(k, o, c)
	case "influxdb":
		client, err = influxdb.NewClient(k, o, c)
	case "irondb":
		client, err = irondb.NewClient(k, o, c)
	case "clickhouse":
		client, err = clickhouse.NewClient(k, o, c)
	case "rpc", "reverseproxycache":
		client, err = reverseproxycache.NewClient(k, o, c)
	case "rule":
		client, err = rule.NewClient(k, o, conf.Origins)
	}
	if err != nil {
		return err
	}

	if client != nil {
		o.HTTPClient = client.HTTPClient()
		proxyClients[k] = client
		defaultPaths := client.DefaultPathConfigs(o)
		registerPathRoutes(router, client.Handlers(), client, o, c, defaultPaths, log)
	}
	return nil
}

// registerPathRoutes will take the provided default paths map,
// merge it with any path data in the provided originconfig, and then register
// the path routes to the appropriate handler from the provided handlers map
func registerPathRoutes(router *mux.Router, handlers map[string]http.Handler,
	client origins.Client, oo *oo.Options, c cache.Cache,
	paths map[string]*po.Options, log *tl.TricksterLogger) {

	decorate := func(po *po.Options) http.Handler {
		// add Origin, Cache, and Path Configs to the HTTP Request's context
		h := middleware.WithResourcesContext(client, oo, c, po, log, po.Handler)
		// decorate frontend prometheus metrics
		if !po.NoMetrics {
			h = middleware.Decorate(oo.Name, oo.OriginType, po.Path, h)
		}
		// attach distributed tracer
		if oo != nil &&
			oo.TracingConfig != nil &&
			oo.TracingConfig.Tracer != nil {
			h = middleware.Trace(oo.TracingConfig.Tracer, h)
		}
		return h
	}

	pathsWithVerbs := make(map[string]*po.Options)
	for _, p := range paths {
		if len(p.Methods) == 0 {
			p.Methods = methods.CacheableHTTPMethods()
		}
		pathsWithVerbs[p.Path+"-"+strings.Join(p.Methods, "-")] = p
	}

	for k, p := range oo.Paths {
		if p2, ok := pathsWithVerbs[k]; ok {
			p2.Merge(p)
			continue
		}
		p3 := po.NewOptions()
		p3.Merge(p)
		pathsWithVerbs[k] = p3
	}

	if h, ok := handlers["health"]; ok &&
		oo.HealthCheckUpstreamPath != "" && oo.HealthCheckVerb != "" {
		hp := "/trickster/health/" + oo.Name
		log.Debug("registering health handler path",
			tl.Pairs{"path": hp, "originName": oo.Name,
				"upstreamPath": oo.HealthCheckUpstreamPath, "upstreamVerb": oo.HealthCheckVerb})
		router.PathPrefix(hp).Handler(middleware.WithResourcesContext(client, oo, nil, nil, log, h)).
			Methods(methods.CacheableHTTPMethods()...)
	}

	plist := make([]string, 0, len(pathsWithVerbs))
	deletes := make([]string, 0, len(pathsWithVerbs))
	for k, p := range pathsWithVerbs {
		if h, ok := handlers[p.HandlerName]; ok && h != nil {
			p.Handler = h
			plist = append(plist, k)
		} else {
			log.Info("invalid handler name for path", tl.Pairs{"path": p.Path, "handlerName": p.HandlerName})
			deletes = append(deletes, p.Path)
		}
	}
	for _, p := range deletes {
		delete(pathsWithVerbs, p)
	}

	sort.Sort(ByLen(plist))
	for i := len(plist)/2 - 1; i >= 0; i-- {
		opp := len(plist) - 1 - i
		plist[i], plist[opp] = plist[opp], plist[i]
	}

	or := mux.NewRouter()

	for _, v := range plist {
		p, ok := pathsWithVerbs[v]
		if !ok {
			continue
		}
		log.Debug("registering origin handler path",
			tl.Pairs{"originName": oo.Name, "path": v, "handlerName": p.HandlerName,
				"originHost": oo.Host, "handledPath": "/" + oo.Name + p.Path, "matchType": p.MatchType, "frontendHosts": strings.Join(oo.Hosts, ",")})
		if p.Handler != nil && len(p.Methods) > 0 {

			if p.Methods[0] == "*" {
				p.Methods = methods.AllHTTPMethods()
			}

			routePath := "/" + oo.Name + p.Path

			switch p.MatchType {
			case matching.PathMatchTypePrefix:
				// Case where we path match by prefix
				// Host Header Routing
				for _, h := range oo.Hosts {
					router.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...).Host(h)
				}
				if !oo.PathRoutingDisabled {
					// Path Routing
					router.PathPrefix(routePath).Handler(decorate(p)).Methods(p.Methods...)
				}
				or.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...)
			default:
				// default to exact match
				// Host Header Routing
				for _, h := range oo.Hosts {
					router.Handle(p.Path, decorate(p)).Methods(p.Methods...).Host(h)
				}
				if !oo.PathRoutingDisabled {
					// Path Routing
					router.Handle(routePath, decorate(p)).Methods(p.Methods...)
				}
				or.Handle(p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}

	if oo.IsDefault {
		log.Info("registering default origin handler paths", tl.Pairs{"originName": oo.Name})
		for _, v := range plist {
			p, ok := pathsWithVerbs[v]
			if !ok {
				continue
			}
			if p.Handler != nil && len(p.Methods) > 0 {
				log.Debug("registering default origin handler paths", tl.Pairs{"originName": oo.Name, "path": p.Path, "handlerName": p.HandlerName, "matchType": p.MatchType})
				switch p.MatchType {
				case matching.PathMatchTypePrefix:
					// Case where we path match by prefix
					router.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...)
				default:
					// default to exact match
					router.Handle(p.Path, decorate(p)).Methods(p.Methods...)
				}
				router.Handle(p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}
	oo.Router = or
	oo.Paths = pathsWithVerbs
}

// ByLen allows sorting of a string slice by string length
type ByLen []string

func (a ByLen) Len() int {
	return len(a)
}

func (a ByLen) Less(i, j int) bool {
	return len(a[i]) < len(a[j])
}

func (a ByLen) Swap(i, j int) {
	a[i], a[j] = a[j], a[i]
}
