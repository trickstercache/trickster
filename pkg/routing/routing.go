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
	"net/http/pprof"
	"sort"
	"strings"

	"github.com/tricksterproxy/trickster/pkg/backends"
	"github.com/tricksterproxy/trickster/pkg/backends/clickhouse"
	modelch "github.com/tricksterproxy/trickster/pkg/backends/clickhouse/model"
	"github.com/tricksterproxy/trickster/pkg/backends/influxdb"
	modelflux "github.com/tricksterproxy/trickster/pkg/backends/influxdb/model"
	"github.com/tricksterproxy/trickster/pkg/backends/irondb"
	modeliron "github.com/tricksterproxy/trickster/pkg/backends/irondb/model"
	oo "github.com/tricksterproxy/trickster/pkg/backends/options"
	"github.com/tricksterproxy/trickster/pkg/backends/prometheus"
	modelprom "github.com/tricksterproxy/trickster/pkg/backends/prometheus/model"
	"github.com/tricksterproxy/trickster/pkg/backends/providers"
	"github.com/tricksterproxy/trickster/pkg/backends/reverseproxycache"
	"github.com/tricksterproxy/trickster/pkg/backends/rule"
	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/config"
	tl "github.com/tricksterproxy/trickster/pkg/logging"
	"github.com/tricksterproxy/trickster/pkg/proxy/methods"
	"github.com/tricksterproxy/trickster/pkg/proxy/paths/matching"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/request/rewriter"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	"github.com/tricksterproxy/trickster/pkg/util/middleware"

	"github.com/gorilla/mux"
)

// RegisterPprofRoutes will register the Pprof Debugging endpoints to the provided router
func RegisterPprofRoutes(routerName string, h *http.ServeMux, logger interface{}) {
	tl.Info(logger,
		"registering pprof /debug routes", tl.Pairs{"routerName": routerName})
	h.HandleFunc("/debug/pprof/", pprof.Index)
	h.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	h.HandleFunc("/debug/pprof/profile", pprof.Profile)
	h.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	h.HandleFunc("/debug/pprof/trace", pprof.Trace)
}

// RegisterProxyRoutes iterates the Trickster Configuration and
// registers the routes for the configured backends
func RegisterProxyRoutes(conf *config.Config, router *mux.Router,
	caches map[string]cache.Cache, tracers tracing.Tracers,
	logger interface{}, dryRun bool) (backends.Backends, error) {

	// a fake "top-level" backend representing the main frontend, so rules can route
	// to it via the clients map
	tlo, _ := reverseproxycache.NewClient("frontend", &oo.Options{}, router, nil)

	// proxyClients maintains a list of proxy clients configured for use by Trickster
	var clients = backends.Backends{"frontend": tlo}
	var err error

	defaultBackend := ""
	var ndo *oo.Options // points to the backend options named "default"
	var cdo *oo.Options // points to the backend options with IsDefault set to true

	// This iteration will ensure default backends are handled properly
	for k, o := range conf.Backends {
		if !providers.IsValidProvider(o.Provider) {
			return nil,
				fmt.Errorf(`unknown backend provider in backend options. backendName: %s, backendProvider: %s`,
					k, o.Provider)
		}
		// Ensure only one default backend exists
		if o.IsDefault {
			if cdo != nil {
				return nil,
					fmt.Errorf("only one backend can be marked as default. Found both %s and %s",
						defaultBackend, k)
			}
			tl.Debug(logger, "default backend identified", tl.Pairs{"name": k})
			defaultBackend = k
			cdo = o
			continue
		}
		// handle backend named "default" last as it needs special
		// handling based on a full pass over the range
		if k == "default" {
			ndo = o
			continue
		}
		err = registerBackendRoutes(router, conf, k, o, clients, caches, tracers, logger, dryRun)
		if err != nil {
			return nil, err
		}
	}
	if ndo != nil {
		if cdo == nil {
			ndo.IsDefault = true
			cdo = ndo
			defaultBackend = "default"
		} else {
			err = registerBackendRoutes(router, conf, "default", ndo, clients, caches, tracers, logger, dryRun)
			if err != nil {
				return nil, err
			}
		}
	}
	if cdo != nil {
		err = registerBackendRoutes(router, conf, defaultBackend, cdo, clients, caches, tracers, logger, dryRun)
		if err != nil {
			return nil, err
		}
	}
	err = rule.ValidateOptions(clients, conf.CompiledRewriters)
	if err != nil {
		return nil, err
	}

	return clients, nil
}

func registerBackendRoutes(router *mux.Router, conf *config.Config, k string,
	o *oo.Options, clients backends.Backends, caches map[string]cache.Cache,
	tracers tracing.Tracers, logger interface{}, dryRun bool) error {

	var client backends.Client
	var c cache.Cache
	var ok bool
	var err error

	c, ok = caches[o.CacheName]
	if !ok {
		return fmt.Errorf("could not find cache named [%s]", o.CacheName)
	}

	if !dryRun {
		tl.Info(logger, "registering route paths", tl.Pairs{"backendName": k,
			"backendProvider": o.Provider, "upstreamHost": o.Host})
	}

	switch strings.ToLower(o.Provider) {
	case "prometheus":
		client, err = prometheus.NewClient(k, o, mux.NewRouter(), c, modelprom.NewModeler())
	case "influxdb":
		client, err = influxdb.NewClient(k, o, mux.NewRouter(), c, modelflux.NewModeler())
	case "irondb":
		client, err = irondb.NewClient(k, o, mux.NewRouter(), c, modeliron.NewModeler())
	case "clickhouse":
		client, err = clickhouse.NewClient(k, o, mux.NewRouter(), c, modelch.NewModeler())
	case "rpc", "reverseproxycache":
		client, err = reverseproxycache.NewClient(k, o, mux.NewRouter(), c)
	case "rule":
		client, err = rule.NewClient(k, o, mux.NewRouter(), clients)
	}
	if err != nil {
		return err
	}

	if client != nil && !dryRun {
		o.HTTPClient = client.HTTPClient()
		clients[k] = client
		defaultPaths := client.DefaultPathConfigs(o)
		RegisterPathRoutes(router, client.Handlers(), client, o, c, defaultPaths,
			tracers, conf.Main.HealthHandlerPath, logger)
	}
	return nil
}

// RegisterPathRoutes will take the provided default paths map,
// merge it with any path data in the provided backend options, and then register
// the path routes to the appropriate handler from the provided handlers map
func RegisterPathRoutes(router *mux.Router, handlers map[string]http.Handler,
	client backends.Client, oo *oo.Options, c cache.Cache,
	defaultPaths map[string]*po.Options, tracers tracing.Tracers,
	healthHandlerPath string, logger interface{}) {

	if oo == nil {
		return
	}

	// get the distributed tracer if configured
	var tr *tracing.Tracer
	if oo != nil {
		if t, ok := tracers[oo.TracingConfigName]; ok {
			tr = t
		}
	}

	decorate := func(po *po.Options) http.Handler {
		// default base route is the path handler
		h := po.Handler
		// attach distributed tracer
		if tr != nil {
			h = middleware.Trace(tr, h)
		}
		// add Origin, Cache, and Path Configs to the HTTP Request's context
		h = middleware.WithResourcesContext(client, oo, c, po, tr, logger, h)
		// attach any request rewriters
		if len(oo.ReqRewriter) > 0 {
			h = rewriter.Rewrite(oo.ReqRewriter, h)
		}
		if len(po.ReqRewriter) > 0 {
			h = rewriter.Rewrite(po.ReqRewriter, h)
		}
		// decorate frontend prometheus metrics
		if !po.NoMetrics {
			h = middleware.Decorate(oo.Name, oo.Provider, po.Path, h)
		}
		return h
	}

	// now we'll go ahead and register the health handler
	if h, ok := handlers["health"]; ok &&
		oo.HealthCheckUpstreamPath != "" && oo.HealthCheckVerb != "" && healthHandlerPath != "" {
		hp := strings.Replace(healthHandlerPath+"/"+oo.Name, "//", "/", -1)
		tl.Debug(logger, "registering health handler path",
			tl.Pairs{"path": hp, "backendName": oo.Name,
				"upstreamPath": oo.HealthCheckUpstreamPath,
				"upstreamVerb": oo.HealthCheckVerb})
		router.PathPrefix(hp).
			Handler(middleware.WithResourcesContext(client, oo, nil, nil, tr, logger, h)).
			Methods(methods.CacheableHTTPMethods()...)
	}

	// This takes the default paths, named like '/api/v1/query' and morphs the name
	// into what the router wants, with methods like '/api/v1/query-GET-HEAD', to help
	// route sorting
	pathsWithVerbs := make(map[string]*po.Options)
	for _, p := range defaultPaths {
		if len(p.Methods) == 0 {
			p.Methods = methods.CacheableHTTPMethods()
		}
		pathsWithVerbs[p.Path+"-"+strings.Join(p.Methods, "-")] = p
	}

	// now we will iterate through the configured paths, and overlay them on those default paths.
	// for a rule backend provider, only the default paths are used with no overlay or importable config
	if oo.Provider != "rule" {
		for k, p := range oo.Paths {
			if p2, ok := pathsWithVerbs[k]; ok {
				p2.Merge(p)
				continue
			}
			p3 := po.New()
			p3.Merge(p)
			pathsWithVerbs[k] = p3
		}
	}

	plist := make([]string, 0, len(pathsWithVerbs))
	deletes := make([]string, 0, len(pathsWithVerbs))
	for k, p := range pathsWithVerbs {
		if h, ok := handlers[p.HandlerName]; ok && h != nil {
			p.Handler = h
			plist = append(plist, k)
		} else {
			tl.Info(logger, "invalid handler name for path",
				tl.Pairs{"path": p.Path, "handlerName": p.HandlerName})
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

	or := client.Router().(*mux.Router)

	for _, v := range plist {
		p := pathsWithVerbs[v]

		pathPrefix := "/" + oo.Name
		handledPath := pathPrefix + p.Path

		tl.Debug(logger, "registering backend handler path",
			tl.Pairs{"backendName": oo.Name, "path": v, "handlerName": p.HandlerName,
				"originHost": oo.Host, "handledPath": handledPath, "matchType": p.MatchType,
				"frontendHosts": strings.Join(oo.Hosts, ",")})
		if p.Handler != nil && len(p.Methods) > 0 {

			if p.Methods[0] == "*" {
				p.Methods = methods.AllHTTPMethods()
			}

			switch p.MatchType {
			case matching.PathMatchTypePrefix:
				// Case where we path match by prefix
				// Host Header Routing
				for _, h := range oo.Hosts {
					router.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...).Host(h)
				}
				if !oo.PathRoutingDisabled {
					// Path Routing
					router.PathPrefix(handledPath).Handler(middleware.StripPathPrefix(pathPrefix,
						decorate(p))).Methods(p.Methods...)
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
					router.Handle(handledPath, middleware.StripPathPrefix(pathPrefix,
						decorate(p))).Methods(p.Methods...)
				}
				or.Handle(p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}

	if oo.IsDefault {
		tl.Info(logger,
			"registering default backend handler paths", tl.Pairs{"backendName": oo.Name})
		for _, v := range plist {
			p := pathsWithVerbs[v]
			if p.Handler != nil && len(p.Methods) > 0 {
				tl.Debug(logger, "registering default backend handler paths",
					tl.Pairs{"backendName": oo.Name, "path": p.Path, "handlerName": p.HandlerName,
						"matchType": p.MatchType})
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
