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
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	encoding "github.com/trickstercache/trickster/v2/pkg/encoding/handler"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/health"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/router"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"
)

// RegisterPprofRoutes will register the Pprof Debugging endpoints to the provided router
func RegisterPprofRoutes(routerName string, r router.Router) {
	logger.Info("registering pprof /debug routes", logging.Pairs{"routerName": routerName})
	r.RegisterRoute("/debug/pprof/", nil, nil,
		false, http.HandlerFunc(pprof.Index))
	r.RegisterRoute("/debug/pprof/cmdline", nil, nil,
		false, http.HandlerFunc(pprof.Cmdline))
	r.RegisterRoute("/debug/pprof/profile", nil, nil,
		false, http.HandlerFunc(pprof.Profile))
	r.RegisterRoute("/debug/pprof/symbol", nil, nil,
		false, http.HandlerFunc(pprof.Symbol))
	r.RegisterRoute("/debug/pprof/trace", nil, nil,
		false, http.HandlerFunc(pprof.Trace))
}

// RegisterProxyRoutes iterates the Trickster Configuration and
// registers the routes for the configured backends
func RegisterProxyRoutes(conf *config.Config, r router.Router,
	metricsRouter router.Router, caches cache.Lookup,
	tracers tracing.Tracers, dryRun bool) (backends.Backends, error) {

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
			logger.Debug("default backend identified", logging.Pairs{"name": k})
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
			k, o, clients, caches, tracers, dryRun)
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
			err = registerBackendRoutes(r, nil, conf, "default", ndo, clients,
				caches, tracers, dryRun)
			if err != nil {
				return nil, err
			}
		}
	}
	if cdo != nil {
		err = registerBackendRoutes(r, metricsRouter, conf,
			defaultBackend, cdo, clients, caches, tracers, dryRun)
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

var noCacheBackends = providers.NonCacheBackends()

// RegisterHealthHandler registers the main health handler
func RegisterHealthHandler(router router.Router, path string,
	hc healthcheck.HealthChecker) {
	router.RegisterRoute(path, nil, nil, false, health.StatusHandler(hc))
}

func registerBackendRoutes(r router.Router, metricsRouter router.Router,
	conf *config.Config, k string, o *bo.Options, clients backends.Backends,
	caches cache.Lookup, tracers tracing.Tracers, dryRun bool) error {

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
		logger.Info("registering route paths", logging.Pairs{"backendName": k,
			"backendProvider": o.Provider, "upstreamHost": o.Host})
	}

	cf := registration.SupportedProviders()
	if f, ok := cf[strings.ToLower(o.Provider)]; ok && f != nil {
		client, err = f(k, o, lm.NewRouter(), c, clients, cf)
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
			tracers)

		// now we'll go ahead and register the health handler
		if h, ok := client.Handlers()["health"]; ok && o.Name != "" && metricsRouter != nil && (o.HealthCheck == nil ||
			o.HealthCheck.Verb != "x") {
			hp := strings.ReplaceAll(conf.Main.HealthHandlerPath+"/"+o.Name, "//", "/")
			logger.Debug("registering health handler path",
				logging.Pairs{"path": hp, "backendName": o.Name,
					"upstreamPath": o.HealthCheck.Path,
					"upstreamVerb": o.HealthCheck.Verb})
			metricsRouter.RegisterRoute(hp, nil, nil, false,
				middleware.WithResourcesContext(client, o, nil,
					nil, nil, h))
		}
	}
	return nil
}

// RegisterPathRoutes will take the provided default paths map,
// merge it with any path data in the provided backend options, and then register
// the path routes to the appropriate handler from the provided handlers map
func RegisterPathRoutes(r router.Router, handlers map[string]http.Handler,
	client backends.Backend, o *bo.Options, c cache.Cache,
	paths map[string]*po.Options, tracers tracing.Tracers) {
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
		h = middleware.WithResourcesContext(client, o, c, po1, tr, h)
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

	// now we will iterate through the configured paths, and overlay them on
	// those default paths. for rule & alb backend providers, only the default
	// paths are used with no overlay or importable config
	if !backends.IsVirtual(o.Provider) {
		for k, p := range o.Paths {
			if p2, ok := paths[k]; ok {
				p2.Merge(p)
				continue
			}
			p3 := po.New()
			p3.Merge(p)
			paths[k] = p3
		}
	}

	plist := make([]string, 0, len(paths))
	deletes := make([]string, 0, len(paths))
	for k, p := range paths {
		if h, ok := handlers[p.HandlerName]; ok && h != nil {
			p.Handler = h
			plist = append(plist, k)
		} else {
			logger.Info("invalid handler name for path",
				logging.Pairs{"path": p.Path, "handlerName": p.HandlerName})
			deletes = append(deletes, p.Path)
		}
	}
	for _, p := range deletes {
		delete(paths, p)
	}
	or := client.Router().(router.Router)

	for _, v := range plist {
		p := paths[v]

		pathPrefix := "/" + o.Name
		handledPath := pathPrefix + p.Path

		logger.Debug("registering backend handler path",
			logging.Pairs{"backendName": o.Name, "path": v, "handlerName": p.HandlerName,
				"backendHost": o.Host, "handledPath": handledPath, "matchType": p.MatchType,
				"frontendHosts": strings.Join(o.Hosts, ",")})
		if p.Handler != nil && len(p.Methods) > 0 {

			if p.Methods[0] == "*" {
				p.Methods = methods.AllHTTPMethods()
			}
			if len(o.Hosts) > 0 {
				r.RegisterRoute(p.Path, o.Hosts, p.Methods,
					p.MatchType == matching.PathMatchTypePrefix, decorate(p))
			}
			if !o.PathRoutingDisabled {
				r.RegisterRoute(handledPath, nil, p.Methods,
					p.MatchType == matching.PathMatchTypePrefix,
					middleware.StripPathPrefix(pathPrefix, decorate(p)))
			}
			or.RegisterRoute(p.Path, nil, p.Methods,
				p.MatchType == matching.PathMatchTypePrefix,
				decorate(p))
		}
	}

	o.Router = or
	o.Paths = paths
}

// RegisterDefaultBackendRoutes will iterate the Backends and register the default routes
func RegisterDefaultBackendRoutes(r router.Router, bknds backends.Backends,
	tracers tracing.Tracers) {

	decorate := func(o *bo.Options, po *po.Options, tr *tracing.Tracer,
		c cache.Cache, client backends.Backend) http.Handler {
		// default base route is the path handler
		h := po.Handler
		// attach distributed tracer
		if tr != nil {
			h = middleware.Trace(tr, h)
		}
		// add Backend, Cache, and Path Configs to the HTTP Request's context
		h = middleware.WithResourcesContext(client, o, c, po, tr, h)
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
			logger.Info("registering default backend handler paths",
				logging.Pairs{"backendName": o.Name})

			for _, p := range o.Paths {
				if p.Handler != nil && len(p.Methods) > 0 {
					logger.Debug(
						"registering default backend handler paths",
						logging.Pairs{"backendName": o.Name, "path": p.Path,
							"handlerName": p.HandlerName,
							"matchType":   p.MatchType})

					if p.MatchType == matching.PathMatchTypePrefix {
						r.RegisterRoute(p.Path, nil, p.Methods,
							true, decorate(o, p, tr, b.Cache(), b))
					}
					r.RegisterRoute(p.Path, nil, p.Methods,
						false, decorate(o, p, tr, b.Cache(), b))
				}
			}
		}
	}

}
