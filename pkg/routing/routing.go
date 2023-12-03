/*
 * Copyright 2018 The Trickster Authors
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

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	encoding "github.com/trickstercache/trickster/v2/pkg/encoding/handler"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/health"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/router"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"
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
func RegisterProxyRoutes(conf *config.Config, r router.Router, metricsRouter *http.ServeMux,
	caches map[string]cache.Cache, tracers tracing.Tracers,
	logger interface{}, dryRun bool) (backends.Backends, error) {

	// a fake "top-level" backend representing the main frontend, so rules can route
	// to it via the clients map
	tlo, _ := reverseproxycache.NewClient("frontend", &bo.Options{}, r, nil, nil, nil)

	// proxyClients maintains a list of proxy clients configured for use by Trickster
	var clients = backends.Backends{"frontend": tlo}
	var err error

	defaultBackend := ""
	var ndo *bo.Options // points to the backend options named "default"
	var cdo *bo.Options // points to the backend options with IsDefault set to true

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
		err = registerBackendRoutes(r, metricsRouter, conf,
			k, o, clients, caches, tracers, logger, dryRun)
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
			err = registerBackendRoutes(r, nil, conf, "default", ndo, clients, caches, tracers, logger, dryRun)
			if err != nil {
				return nil, err
			}
		}
	}
	if cdo != nil {
		err = registerBackendRoutes(r, metricsRouter, conf,
			defaultBackend, cdo, clients, caches, tracers, logger, dryRun)
		if err != nil {
			return nil, err
		}
	}
	err = rule.ValidateOptions(clients, conf.CompiledRewriters)
	if err != nil {
		return nil, err
	}

	err = alb.ValidatePools(clients)
	if err != nil {
		return nil, err
	}
	return clients, nil
}

var noCacheBackends = map[string]interface{}{
	"alb":          nil,
	"rp":           nil,
	"reverseproxy": nil,
	"proxy":        nil,
	"rule":         nil,
}

// RegisterHealthHandler registers the main health handler
func RegisterHealthHandler(router *http.ServeMux, path string, hc healthcheck.HealthChecker) {
	router.Handle(path, health.StatusHandler(hc))
}

func registerBackendRoutes(r router.Router, metricsRouter *http.ServeMux, conf *config.Config, k string,
	o *bo.Options, clients backends.Backends, caches map[string]cache.Cache,
	tracers tracing.Tracers, logger interface{}, dryRun bool) error {

	var client backends.Backend
	var c cache.Cache
	var ok bool
	var err error

	if _, ok = noCacheBackends[o.Provider]; !ok {
		if c, ok = caches[o.CacheName]; !ok {
			return fmt.Errorf("could not find cache named [%s]", o.CacheName)
		}
	}

	if !dryRun {
		tl.Info(logger, "registering route paths", tl.Pairs{"backendName": k,
			"backendProvider": o.Provider, "upstreamHost": o.Host})
	}

	cf := registration.SupportedProviders()
	if f, ok := cf[strings.ToLower(o.Provider)]; ok && f != nil {
		client, err = f(k, o, router.NewRouter(), c, clients, cf)
	}
	if err != nil {
		return err
	}

	if client != nil && !dryRun {
		o.HTTPClient = client.HTTPClient()
		clients[k] = client
		defaultPaths := client.DefaultPathConfigs(o)

		h := client.Handlers()

		RegisterPathRoutes(r, h, client, o, c, defaultPaths,
			tracers, conf.Main.HealthHandlerPath, logger)

		// now we'll go ahead and register the health handler
		if h, ok := client.Handlers()["health"]; ok && o.Name != "" && metricsRouter != nil && (o.HealthCheck == nil ||
			o.HealthCheck.Verb != "x") {
			hp := strings.Replace(conf.Main.HealthHandlerPath+"/"+o.Name, "//", "/", -1)
			tl.Debug(logger, "registering health handler path",
				tl.Pairs{"path": hp, "backendName": o.Name,
					"upstreamPath": o.HealthCheck.Path,
					"upstreamVerb": o.HealthCheck.Verb})
			metricsRouter.Handle(hp, http.Handler(middleware.WithResourcesContext(client, o, nil, nil, nil, logger, h)))
		}
	}
	return nil
}

// RegisterPathRoutes will take the provided default paths map,
// merge it with any path data in the provided backend options, and then register
// the path routes to the appropriate handler from the provided handlers map
func RegisterPathRoutes(r router.Router, handlers map[string]http.Handler,
	client backends.Backend, o *bo.Options, c cache.Cache,
	defaultPaths map[string]*po.Options, tracers tracing.Tracers,
	healthHandlerPath string, logger interface{}) {

	if o == nil {
		return
	}

	// get the distributed tracer if configured
	var tr *tracing.Tracer
	if o != nil {
		if t, ok := tracers[o.TracingConfigName]; ok {
			tr = t
		}
	}

	decorate := func(po1 *po.Options) http.Handler {
		// default base route is the path handler
		h := po1.Handler
		// attach distributed tracer
		if tr != nil {
			h = middleware.Trace(tr, h)
		}
		// attach compression handler
		h = encoding.HandleCompression(h, o.CompressibleTypes)
		// add Backend, Cache, and Path Configs to the HTTP Request's context
		h = middleware.WithResourcesContext(client, o, c, po1, tr, logger, h)
		// attach any request rewriters
		if len(o.ReqRewriter) > 0 {
			h = rewriter.Rewrite(o.ReqRewriter, h)
		}
		if len(po1.ReqRewriter) > 0 {
			h = rewriter.Rewrite(po1.ReqRewriter, h)
		}
		// decorate frontend prometheus metrics
		if !po1.NoMetrics {
			h = middleware.Decorate(o.Name, o.Provider, po1.Path, h)
		}
		return h
	}

	// This takes the default paths, named like '/api/v1/query' and morphs the name
	// into what the router wants, with methods like '/api/v1/query-0000011001', to help
	// route sorting. the bitmap provides unique names multiple path entries of the same
	// path but with different methods, without impacting true path sorting
	pathsWithVerbs := make(map[string]*po.Options)
	for _, p := range defaultPaths {
		if len(p.Methods) == 0 {
			p.Methods = methods.CacheableHTTPMethods()
		}
		pathsWithVerbs[p.Path+"-"+fmt.Sprintf("%010b", methods.MethodMask(p.Methods...))] = p
	}

	// now we will iterate through the configured paths, and overlay them on those default paths.
	// for rule & alb backend providers, only the default paths are used with no overlay or importable config
	if !backends.IsVirtual(o.Provider) {
		for k, p := range o.Paths {
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

	or := client.Router().(router.Router)

	for _, v := range plist {
		p := pathsWithVerbs[v]

		pathPrefix := "/" + o.Name
		handledPath := pathPrefix + p.Path

		tl.Debug(logger, "registering backend handler path",
			tl.Pairs{"backendName": o.Name, "path": v, "handlerName": p.HandlerName,
				"backendHost": o.Host, "handledPath": handledPath, "matchType": p.MatchType,
				"frontendHosts": strings.Join(o.Hosts, ",")})
		if p.Handler != nil && len(p.Methods) > 0 {

			if p.Methods[0] == "*" {
				p.Methods = methods.AllHTTPMethods()
			}

			switch p.MatchType {
			case matching.PathMatchTypePrefix:
				// Case where we path match by prefix
				// Host Header Routing
				for _, h := range o.Hosts {
					r.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...).Host(h)
				}
				if !o.PathRoutingDisabled {
					// Path Routing
					r.PathPrefix(handledPath).Handler(middleware.StripPathPrefix(pathPrefix,
						decorate(p))).Methods(p.Methods...)
				}
				or.PathPrefix(p.Path).Handler(decorate(p)).Methods(p.Methods...)
			default:
				// default to exact match
				// Host Header Routing
				for _, h := range o.Hosts {
					r.Handle(p.Path, decorate(p)).Methods(p.Methods...).Host(h)
				}
				if !o.PathRoutingDisabled {
					// Path Routing
					r.Handle(handledPath, middleware.StripPathPrefix(pathPrefix,
						decorate(p))).Methods(p.Methods...)
				}
				or.Handle(p.Path, decorate(p)).Methods(p.Methods...)
			}
		}
	}

	o.Router = or
	o.Paths = pathsWithVerbs
}

// RegisterDefaultBackendRoutes will iterate the Backends and register the default routes
func RegisterDefaultBackendRoutes(router router.Router, bknds backends.Backends,
	logger interface{}, tracers tracing.Tracers) {

	decorate := func(o *bo.Options, po *po.Options, tr *tracing.Tracer,
		c cache.Cache, client backends.Backend) http.Handler {
		// default base route is the path handler
		h := po.Handler
		// attach distributed tracer
		if tr != nil {
			h = middleware.Trace(tr, h)
		}
		// add Backend, Cache, and Path Configs to the HTTP Request's context
		h = middleware.WithResourcesContext(client, o, c, po, tr, logger, h)
		// attach any request rewriters
		if len(o.ReqRewriter) > 0 {
			h = rewriter.Rewrite(o.ReqRewriter, h)
		}
		if len(po.ReqRewriter) > 0 {
			h = rewriter.Rewrite(po.ReqRewriter, h)
		}
		// decorate frontend prometheus metrics
		if !po.NoMetrics {
			h = middleware.Decorate(o.Name, o.Provider, po.Path, h)
		}
		return h
	}

	for _, b := range bknds {
		o := b.Configuration()
		if o.IsDefault {
			var tr *tracing.Tracer
			if t, ok := tracers[o.TracingConfigName]; ok {
				tr = t
			}
			tl.Info(logger,
				"registering default backend handler paths", tl.Pairs{"backendName": o.Name})

			// Sort by key length(Path length) to ensure /api/v1/query_range appear before /api/v1 or / path in regex path matching
			keylist := make([]string, 0, len(o.Paths))
			for key := range o.Paths {
				keylist = append(keylist, key)
			}
			sort.Sort(ByLen(keylist))
			for i := len(keylist)/2 - 1; i >= 0; i-- {
				opp := len(keylist) - 1 - i
				keylist[i], keylist[opp] = keylist[opp], keylist[i]
			}

			for _, k := range keylist {
				var p = o.Paths[k]
				if p.Handler != nil && len(p.Methods) > 0 {
					tl.Debug(logger, "registering default backend handler paths",
						tl.Pairs{"backendName": o.Name, "path": p.Path, "handlerName": p.HandlerName,
							"matchType": p.MatchType})
					switch p.MatchType {
					case matching.PathMatchTypePrefix:
						// Case where we path match by prefix
						router.PathPrefix(p.Path).Handler(decorate(o, p, tr, b.Cache(), b)).Methods(p.Methods...)
					default:
						// default to exact match
						router.Handle(p.Path, decorate(o, p, tr, b.Cache(), b)).Methods(p.Methods...)
					}
					router.Handle(p.Path, decorate(o, p, tr, b.Cache(), b)).Methods(p.Methods...)
				}
			}
		}
	}

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
