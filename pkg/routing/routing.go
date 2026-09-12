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
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	encoding "github.com/trickstercache/trickster/v2/pkg/encoding/handler"
	fopt "github.com/trickstercache/trickster/v2/pkg/frontend/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/handler"
	"github.com/trickstercache/trickster/v2/pkg/proxy/engines"
	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/health"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/route"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware"
	"github.com/trickstercache/trickster/v2/pkg/util/middleware/bodyfilter"
)

var noCacheBackends = providers.NonCacheBackends()

func attachAuthenticator(h http.Handler, pathOptions *po.Options, backendOptions *bo.Options) http.Handler {
	if pathOptions.AuthOptions != nil && pathOptions.AuthOptions.Authenticator != nil {
		h = handler.NamedMiddleware(pathOptions.AuthOptions.Name,
			pathOptions.AuthOptions.Authenticator, h)
	} else if pathOptions.AuthenticatorName != "none" && backendOptions.AuthOptions != nil &&
		backendOptions.AuthOptions.Authenticator != nil {
		h = handler.NamedMiddleware(backendOptions.AuthOptions.Name,
			backendOptions.AuthOptions.Authenticator, h)
	}
	return h
}

func hasAuthenticator(pathOptions *po.Options, backendOptions *bo.Options) bool {
	return pathOptions.AuthOptions != nil && pathOptions.AuthOptions.Authenticator != nil ||
		pathOptions.AuthenticatorName != "none" && backendOptions.AuthOptions != nil &&
			backendOptions.AuthOptions.Authenticator != nil
}

func shouldCaptureAuth(pathOptions *po.Options, backendOptions *bo.Options) bool {
	return hasAuthenticator(pathOptions, backendOptions) || backends.IsVirtual(backendOptions.Provider)
}

// isPassthroughPath reports whether a path proxies without caching, which is
// what routes it to the ReverseProxy-backed lane. Derived from existing config
// rather than a dedicated setting: the `proxy` handler is by definition the
// non-caching one, and it is what the reverseproxy provider registers. A
// handler assigned directly rather than resolved from the registry is left
// alone, because the name no longer describes what it does.
func isPassthroughPath(pathOpts *po.Options) bool {
	return pathOpts != nil && pathOpts.HandlerFromRegistry &&
		pathOpts.HandlerName == providers.Proxy
}

// routeLogging carries a backend's resolved access logger and whether routes
// without one must still be marked handled for the default access log.
type routeLogging struct {
	logger      *accesslog.Logger
	markHandled bool
}

// newRouteLogging resolves the backend's logger; when the backend opted out
// of an enabled default access log its routes are marked handled instead.
func newRouteLogging(conf *config.Config, o *bo.Options) routeLogging {
	al := newAccessLogger(conf, o)
	return routeLogging{
		logger:      al,
		markHandled: al == nil && conf != nil && conf.AccessLog.IsEnabled(),
	}
}

// withCaptures wraps h so regex submatches reach the rewriters that use them;
// routes without token-consuming rewriters are returned untouched.
func withCaptures(o *bo.Options, p *po.Options, re *regexp.Regexp, h http.Handler) http.Handler {
	if p.MatchType != matching.PathMatchTypeRegex || re == nil ||
		(!p.ReqRewriter.HasTokens() && !o.ReqRewriter.HasTokens()) {
		return h
	}
	return rewriter.WithPathCaptures(re, h)
}

func applyMiddleware(o *bo.Options, pathOpts *po.Options, tr *tracing.Tracer,
	c cache.Cache, client backends.Backend, rl routeLogging, frontend *fopt.Options,
	mirrors []mirrorTarget,
) http.Handler {
	var passthrough http.Handler
	if client != nil {
		passthrough = engines.NewPassthroughHandler(client)
	}
	isPassthrough := passthrough != nil && isPassthroughPath(pathOpts)

	var h http.Handler
	if isPassthrough {
		h = passthrough
		if pathOpts.CollapsedForwardingType == forwarding.CFTypeProgressive {
			h = engines.CollapsedPassthrough(passthrough)
		}
	} else {
		h = middleware.LimitQueryRange(pathOpts.Handler)
	}
	for _, m := range mirrors {
		h = middleware.Mirror(o.Name, m.options, m.backend, h)
	}
	if frontend != nil {
		maxBodySizeBytes, truncateOnly := getSizeLimits(frontend)
		h = bodyfilter.Handler(maxBodySizeBytes, truncateOnly, h)
	}
	if !isPassthrough && !handlers.IsLocal(pathOpts.HandlerName) {
		// a handler that answers from configuration has no upstream for an
		// upgrade to be tunneled to; diverting one would open a connection
		// a local path promised never to make
		h = middleware.UpgradeSwitch(passthrough, h)
	}
	h = middleware.MaxForwards(h)
	if tr != nil {
		h = middleware.Trace(tr, h)
	}
	// the access log needs the route's resources to read a user it authenticated or a result
	// header it withholds from the client
	withResources := shouldCaptureAuth(pathOpts, o) || pathOpts.HideResultHeader ||
		rl.logger.NeedsResources()
	h = attachAuthenticator(h, pathOpts, o)
	h = encoding.HandleCompression(h, o.CompressibleTypes)
	// WithResourcesContext must wrap outer than LimitQueryRange
	h = middleware.WithResourcesContext(client, o, c, pathOpts, tr, h)
	if len(o.ReqRewriter) > 0 {
		h = rewriter.Rewrite(o.ReqRewriter, h)
	}
	if len(pathOpts.ReqRewriter) > 0 {
		h = rewriter.Rewrite(pathOpts.ReqRewriter, h)
	}
	if !pathOpts.NoMetrics {
		h = middleware.Decorate(o.Name, o.Provider, pathOpts.Path, h)
	}
	if rl.markHandled {
		return accesslog.Handled(h)
	}
	return accesslog.Middleware(rl.logger, pathOpts.Path, withResources, h)
}

type listenerRoute struct {
	router   router.Router
	frontend *fopt.Options
}

// RegisterProxyRoutes iterates the Trickster Configuration and
// registers the routes for the configured backends
func RegisterProxyRoutes(conf *config.Config, clients backends.Backends,
	r router.Router, metricsRouter router.Router, caches cache.Lookup,
	tracers tracing.Tracers, dryRun bool,
) error {
	return registerProxyRoutes(conf, clients, r, func(*bo.Options) []listenerRoute { return []listenerRoute{{r, frontendOptions(conf, "")}} },
		metricsRouter, caches, tracers, dryRun)
}

// RegisterProxyRoutesForListeners registers each backend on its configured listener router.
func RegisterProxyRoutesForListeners(conf *config.Config, clients backends.Backends,
	listenerRouters map[string]router.Router, metricsRouter router.Router, caches cache.Lookup,
	tracers tracing.Tracers, dryRun bool,
) error {
	defaultRouter := listenerRouters[listener.DefaultFrontendName]
	if defaultRouter == nil {
		for _, r := range listenerRouters {
			defaultRouter = r
			break
		}
	}
	return registerProxyRoutes(conf, clients, defaultRouter, func(o *bo.Options) []listenerRoute {
		if o == nil {
			return nil
		}
		routes := make([]listenerRoute, 0, len(o.ListenerNames))
		for _, name := range o.ListenerNames {
			if options := conf.Listeners[name]; options != nil && options.Protocol != "" && options.Protocol != listener.ProtocolHTTP {
				continue
			}
			r := listenerRouters[name]
			if r == nil {
				return nil
			}
			routes = append(routes, listenerRoute{r, frontendOptions(conf, name)})
		}
		if len(o.ListenerNames) == 0 && registry.NativeListeners().GetByProvider(strings.ToLower(o.Provider)) == nil {
			return nil
		}
		return routes
	}, metricsRouter, caches, tracers, dryRun)
}

func registerProxyRoutes(conf *config.Config, clients backends.Backends,
	defaultRouter router.Router, routerFor func(*bo.Options) []listenerRoute,
	metricsRouter router.Router, caches cache.Lookup, tracers tracing.Tracers, dryRun bool,
) error {
	// a fake "top-level" backend representing the main frontend, so rules can route
	// to it via the clients map
	var err error
	clients["frontend"], err = reverseproxycache.NewClient("frontend", &bo.Options{}, defaultRouter, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create frontend client: %w", err)
	}

	var defaultBackend string
	var ndo *bo.Options // points to the backend options named "default"
	var cdo *bo.Options // points to the backend options with IsDefault set to true

	// backends register in name order, so routes ranked alike on one path
	// resolve the same way on every load
	for _, k := range slices.Sorted(maps.Keys(conf.Backends)) {
		o := conf.Backends[k]
		if !providers.IsValidProvider(o.Provider) {
			return fmt.Errorf(`unknown backend provider in backend options. backendName: %s, backendProvider: %s`,
				k, o.Provider)
		}
		// template backends are cloned per discovered ALB pool member and
		// are never routed directly
		if o.IsTemplate {
			continue
		}
		// Ensure only one default backend exists
		if o.IsDefault {
			if cdo != nil {
				return fmt.Errorf("only one backend can be marked as default. Found both %s and %s",
					defaultBackend, k)
			}
			if !dryRun {
				logger.Debug("default backend identified", logging.Pairs{keys.Name: k})
			}
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
		r := routerFor(o)
		if r == nil {
			return fmt.Errorf("no router configured for listeners %q", o.ListenerNames)
		}
		if err := registerBackendRoutes(r, metricsRouter, conf,
			k, o, clients, caches, tracers, dryRun); err != nil {
			return err
		}
	}
	if ndo != nil {
		r := routerFor(ndo)
		if r == nil {
			return fmt.Errorf("no router configured for listeners %q", ndo.ListenerNames)
		}
		if cdo == nil {
			ndo.IsDefault = true
			cdo = ndo
			defaultBackend = "default"
		} else {
			if err := registerBackendRoutes(r, nil, conf, "default", ndo, clients,
				caches, tracers, dryRun); err != nil {
				return err
			}
		}
	}
	if cdo != nil {
		r := routerFor(cdo)
		if r == nil {
			return fmt.Errorf("no router configured for listeners %q", cdo.ListenerNames)
		}
		if err := registerBackendRoutes(r, metricsRouter, conf,
			defaultBackend, cdo, clients, caches, tracers, dryRun); err != nil {
			return err
		}
	}
	return nil
}

// RegisterHealthHandler registers the main health handler
func RegisterHealthHandler(router router.Router, path string,
	hc healthcheck.HealthChecker, backends backends.Backends,
) {
	router.RegisterRoute(path, nil, nil, matching.PathMatchTypeExact,
		health.StatusHandler(nil, hc, backends))
}

func registerBackendRoutes(r []listenerRoute, metricsRouter router.Router,
	conf *config.Config, k string, o *bo.Options, clients backends.Backends,
	caches cache.Lookup, tracers tracing.Tracers, dryRun bool,
) error {
	var c cache.Cache

	if _, ok := noCacheBackends[o.Provider]; !ok {
		if c, ok = caches[o.CacheName]; !ok {
			return fmt.Errorf("could not find cache named [%s]", o.CacheName)
		}
	}

	if dryRun {
		cf := registry.SupportedProviders()
		if f, ok := cf[strings.ToLower(o.Provider)]; ok && f != nil {
			client, err := f(k, o, lm.NewRouter(), c, clients, cf)
			if err != nil {
				return err
			}
			clients[k] = client
			o.HTTPClient = client.HTTPClient()
		}
	} else {
		client, ok := clients[k]
		if !ok || client == nil {
			return fmt.Errorf("could not find backend client named [%s]", k)
		}
		if c != nil {
			client.SetCache(c)
		}
		logger.Info("registering route paths", logging.Pairs{
			keys.BackendName:     k,
			keys.BackendProvider: o.Provider,
			"upstreamHost":       o.Host,
		})

		if !o.PathDefaultsDisabled {
			o.Paths = client.DefaultPathConfigs(o).Overlay(o.Paths)
		}

		h := client.Handlers()

		registerPathRoutes(r, conf, h, client, o, c, tracers, clients)

		// now we'll go ahead and register the health handler
		if h, ok := client.Handlers()["health"]; ok && o.Name != "" && metricsRouter != nil && (o.HealthCheck != nil &&
			o.HealthCheck.Verb != "x") {
			hp := strings.ReplaceAll(conf.MgmtConfig.HealthHandlerPath+"/"+o.Name, "//", "/")
			logger.Debug("registering health handler path",
				logging.Pairs{
					keys.BackendName: o.Name,
					keys.Path:        hp,
					"upstreamPath":   o.HealthCheck.Path,
					"upstreamVerb":   o.HealthCheck.Verb,
				})
			metricsRouter.RegisterRoute(hp, nil, nil, matching.PathMatchTypeExact,
				middleware.WithResourcesContext(client, o, nil,
					nil, nil, h))
		}
	}
	return nil
}

// RegisterPathRoutes will take the provided default paths map,
// merge it with any path data in the provided backend options, and then register
// the path routes to the appropriate handler from the provided handlers map
func RegisterPathRoutes(r router.Router, conf *config.Config, handlers handlers.Lookup,
	client backends.Backend, o *bo.Options, c cache.Cache, tracers tracing.Tracers,
) {
	registerPathRoutes([]listenerRoute{{r, frontendOptions(conf, "")}}, conf, handlers, client, o, c, tracers, nil)
}

// registerPathRoutes registers the backend's paths; clients resolves the
// backends the paths mirror to, and may be nil where none is reachable.
func registerPathRoutes(routes []listenerRoute, conf *config.Config, handlers handlers.Lookup,
	client backends.Backend, o *bo.Options, c cache.Cache, tracers tracing.Tracers,
	clients backends.Backends,
) {
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

	rl := newRouteLogging(conf, o)

	or := client.Router().(router.Router)

	if o.Paths.RegexShadowedByCatchAll() {
		logger.Warn("regex paths are unreachable behind a catch-all prefix path;"+
			" convert the catch-all to a regex (e.g., ^/.*) to make them reachable",
			logging.Pairs{keys.BackendName: o.Name})
	}

	for _, p := range o.Paths {
		if p.Handler == nil && p.HandlerName != "" {
			if h, ok := handlers[p.HandlerName]; ok && h != nil {
				p.Handler = h
				p.HandlerFromRegistry = true
			}
		}

		pathPrefix := "/" + o.Name
		var handledPath string
		if p.MatchType == matching.PathMatchTypeRegex {
			// splice the backend name between the pattern's leading ^ anchor
			// (guaranteed by path Options Initialize) and the remainder, so
			// ^/[^/]+/results becomes ^/backendName/[^/]+/results; this works
			// for the escaped ^\/ form too, and StripPathPrefix is unaffected
			// because the literal request path begins with /backendName
			handledPath = "^/" + o.Name + strings.TrimPrefix(p.Path, "^")
		} else {
			handledPath = pathPrefix + p.Path
		}

		logger.Debug("registering backend handler path",
			logging.Pairs{
				keys.BackendName: o.Name,
				keys.Path:        p.Path,
				keys.Methods:     p.Methods,
				keys.HandlerName: p.HandlerName,
				"backendHost":    o.Host,
				"handledPath":    handledPath,
				keys.MatchType:   p.MatchType,
				"frontendHosts":  strings.Join(o.Hosts, ","),
			})
		if p.Handler != nil && len(p.Methods) > 0 {
			if p.Methods[0] == "*" {
				p.Methods = methods.AllHTTPMethods()
			}
			mirrors := mirrorBackends(p, o, clients)
			// in path-routing mode captures come from the un-stripped path, so
			// the spliced pattern is compiled and applied outside the strip
			var handledRegexp *regexp.Regexp
			if p.MatchType == matching.PathMatchTypeRegex && !o.PathRoutingDisabled {
				handledRegexp, _ = regexp.Compile(handledPath)
			}
			// a dispatch-only path is reached through an ALB pool or a rule's
			// next_route, so it lives on the backend's own router and on no listener
			for _, route := range routes {
				if p.DispatchOnly {
					break
				}
				if len(o.Hosts) > 0 || o.AnyHostRouting {
					registerRoute(route.router, p, p.Path, o.Hosts, p.MatchType,
						withCaptures(o, p, p.Regexp,
							applyMiddleware(o, p, tr, c, client, rl, route.frontend, mirrors)))
				}
				if !o.PathRoutingDisabled {
					registerRoute(route.router, p, handledPath, nil, p.MatchType,
						withCaptures(o, p, handledRegexp, middleware.StripPathPrefix(pathPrefix,
							applyMiddleware(o, p, tr, c, client, rl, route.frontend, mirrors))))
				}
			}
			registerRoute(or, p, p.Path, nil, p.MatchType,
				withCaptures(o, p, p.Regexp, applyMiddleware(o, p, tr, c, client, rl, nil, mirrors)))
		}
	}

	o.Router = or
}

// mirrorTarget is one mirror of a path with the backend that receives its copies
type mirrorTarget struct {
	options *po.MirrorOptions
	backend backends.Backend
}

func mirrorBackends(p *po.Options, o *bo.Options, clients backends.Backends) []mirrorTarget {
	// a mirror whose backend cannot be resolved is logged and left out; the path is still served
	var out []mirrorTarget
	for _, m := range p.Mirrors {
		if m == nil {
			continue
		}
		target, ok := clients[m.BackendName]
		if !ok || target == nil {
			logger.Warn("mirror backend is not available; the path is served without it",
				logging.Pairs{
					keys.BackendName: o.Name, keys.Path: p.Path,
					"mirrorBackend": m.BackendName,
				})
			continue
		}
		out = append(out, mirrorTarget{options: m, backend: target})
	}
	return out
}

// registerRoute registers a path's handler, conditioned on the path's header
// and query predicates when it declares any; within a backend, paths are
// registered in declaration order, and backends in name order
func registerRoute(rtr router.Router, p *po.Options, path string, hosts []string,
	mt matching.PathMatchType, h http.Handler,
) {
	if p.Predicates != nil || p.MatchOrder != 0 {
		rtr.RegisterRouteSpec(route.Spec{
			Path: path, Hosts: hosts, Methods: p.Methods, MatchType: mt,
			Predicates: p.Predicates, Order: p.MatchOrder, Handler: h,
		})
		return
	}
	rtr.RegisterRoute(path, hosts, p.Methods, mt, h)
}

// RegisterDefaultBackendRoutes will iterate the Backends and register the default routes
func RegisterDefaultBackendRoutes(r router.Router, conf *config.Config,
	bknds backends.Backends, tracers tracing.Tracers,
) {
	registerDefaultBackendRoutes(func(*bo.Options) []listenerRoute { return []listenerRoute{{r, frontendOptions(conf, "")}} }, conf, bknds, tracers)
}

// RegisterDefaultBackendRoutesForListeners registers default routes on each backend's listener.
func RegisterDefaultBackendRoutesForListeners(listenerRouters map[string]router.Router,
	conf *config.Config, bknds backends.Backends, tracers tracing.Tracers,
) {
	registerDefaultBackendRoutes(func(o *bo.Options) []listenerRoute {
		if o == nil {
			return nil
		}
		routes := make([]listenerRoute, 0, len(o.ListenerNames))
		for _, name := range o.ListenerNames {
			if options := conf.Listeners[name]; options != nil && options.Protocol != "" && options.Protocol != listener.ProtocolHTTP {
				continue
			}
			if r := listenerRouters[name]; r != nil {
				routes = append(routes, listenerRoute{r, frontendOptions(conf, name)})
			}
		}
		return routes
	}, conf, bknds, tracers)
}

func registerDefaultBackendRoutes(routerFor func(*bo.Options) []listenerRoute, conf *config.Config,
	bknds backends.Backends, tracers tracing.Tracers,
) {
	for _, b := range bknds {
		o := b.Configuration()
		if o.IsDefault {
			routes := routerFor(o)
			if len(routes) == 0 {
				continue
			}
			var tr *tracing.Tracer
			if t, ok := tracers[o.TracingConfigName]; ok {
				tr = t
			}
			logger.Info("registering default backend handler paths",
				logging.Pairs{keys.BackendName: o.Name})

			rl := newRouteLogging(conf, o)

			for _, route := range routes {
				for _, p := range o.Paths {
					if p.Handler != nil && len(p.Methods) > 0 {
						logger.Debug(
							"registering default backend handler path",
							logging.Pairs{
								keys.BackendName: o.Name,
								keys.Path:        p.Path,
								keys.HandlerName: p.HandlerName,
								keys.MatchType:   p.MatchType,
							})

						// a prefix path is also registered for exact matching so
						// requests to the exact path route without a prefix scan
						mirrors := mirrorBackends(p, o, bknds)
						mt := p.MatchType
						if mt == matching.PathMatchTypePrefix || mt == matching.PathMatchTypeSegment {
							registerRoute(route.router, p, p.Path, nil, mt,
								applyMiddleware(o, p, tr, b.Cache(), b, rl, route.frontend, mirrors))
							mt = matching.PathMatchTypeExact
						}
						registerRoute(route.router, p, p.Path, nil, mt,
							withCaptures(o, p, p.Regexp,
								applyMiddleware(o, p, tr, b.Cache(), b, rl, route.frontend, mirrors)))
					}
				}
			}
		}
	}
}

// newAccessLogger returns the backend's access logger. A backend with its own
// access_log block uses it exclusively, even when that block disables logging;
// a backend without one inherits the top-level default.
func newAccessLogger(conf *config.Config, o *bo.Options) *accesslog.Logger {
	if o == nil {
		return nil
	}
	opts := o.AccessLog
	if opts == nil && conf != nil {
		opts = conf.AccessLog
	}
	if !opts.IsEnabled() {
		return nil
	}
	al, err := accesslog.NewLogger(opts, instanceID(conf), o.Name, o.Provider)
	if err != nil {
		logger.Error("access logger creation failed; access logging disabled",
			logging.Pairs{keys.BackendName: o.Name, keys.Error: err.Error()})
		return nil
	}
	return al
}

// RouterAccessLogger returns a logger for requests no backend route handles,
// from the top-level access_log, or nil when none is configured.
func RouterAccessLogger(conf *config.Config) *accesslog.Logger {
	if conf == nil || !conf.AccessLog.IsEnabled() {
		return nil
	}
	al, err := accesslog.NewLogger(conf.AccessLog, instanceID(conf),
		accesslog.UnmatchedName, accesslog.UnmatchedName)
	if err != nil {
		logger.Error("default access logger creation failed; unmatched requests are not logged",
			logging.Pairs{keys.Error: err.Error()})
		return nil
	}
	return al
}

func instanceID(conf *config.Config) int {
	if conf != nil && conf.Main != nil {
		return conf.Main.InstanceID
	}
	return 0
}

func getSizeLimits(opt *fopt.Options) (int64, bool) {
	maxBodySizeBytes := fopt.DefaultMaxRequestBodySizeBytes
	var truncateOnly bool
	if opt != nil && opt.MaxRequestBodySizeBytes != nil {
		maxBodySizeBytes = *opt.MaxRequestBodySizeBytes
		truncateOnly = opt.TruncateRequestBodyTooLarge
	}
	return maxBodySizeBytes, truncateOnly
}

func frontendOptions(conf *config.Config, name string) *fopt.Options {
	if conf != nil {
		if options := conf.FrontendOptionsForListener(name); options != nil {
			return options
		}
		if conf.Frontend != nil {
			return conf.Frontend
		}
	}
	return fopt.New()
}
