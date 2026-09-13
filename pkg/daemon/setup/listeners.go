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

package setup

import (
	"crypto/tls"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/pprof"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	certs "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/certificates"
	ch "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/config"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	listenerhttp3 "github.com/trickstercache/trickster/v2/pkg/proxy/listener/http3"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	tr "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

type desiredListener struct {
	key          string
	listenerName string
	address      string
	port         int
	tls          bool
	options      *listenerconfig.Options
	router       http.Handler
	// origin identifies native protocol configuration for restart detection.
	origin string
	native native.Adapter
	// http3 marks a QUIC/UDP endpoint that mirrors this listener's TLS routes.
	http3          bool
	advertisedPort int
	// stream marks a tcp, tls or udp relay, whose routing table is swapped on reload.
	stream bool
}

// mgmtRoute is a reserved exact-path route: registered on the management
// router and served ahead of every proxy listener's router.
type mgmtRoute struct {
	path    string
	handler http.Handler
}

// guardReservedRoutes serves reserved paths before next sees the request, so
// backend routes on any host can never replace them.
func guardReservedRoutes(routes []mgmtRoute, next http.Handler) http.Handler {
	if len(routes) == 0 || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, route := range routes {
			if r.URL.Path == route.path {
				route.handler.ServeHTTP(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func applyListenerConfigs(conf, oldConf *config.Config,
	listenerRouters map[string]router.Router, reloadHandler http.Handler,
	metricsRouter router.Router, tracers tracing.Tracers, clients backends.Backends,
	errorFunc func(), lg *listener.Group, mgmtRoutes ...mgmtRoute,
) {
	if conf == nil || len(conf.Listeners) == 0 {
		return
	}

	metricsRouter.RegisterRoute("/metrics", nil, nil,
		matching.PathMatchTypeExact, metrics.Handler())
	if listenerEnabledOn(conf.MgmtConfig.ConfigHandlerListener, mgmt.ListenerNameMetrics) {
		registerConfigRoutes(conf, metricsRouter, lg)
	}
	if listenerEnabledOn(conf.MgmtConfig.PprofListener, mgmt.ListenerNameMetrics) {
		pprof.RegisterRoutes(mgmt.ListenerNameMetrics, metricsRouter)
	}

	managementRouter := lm.NewRouter()
	if listenerEnabledOn(conf.MgmtConfig.ConfigHandlerListener, mgmt.ListenerNameMgmt) {
		registerConfigRoutes(conf, managementRouter, lg)
	}
	managementRouter.RegisterRoute(conf.MgmtConfig.ReloadHandlerPath, nil, nil,
		matching.PathMatchTypeExact, reloadHandler)
	managementRouter.RegisterRoute(conf.MgmtConfig.PurgeByPathHandlerPath, nil, nil,
		matching.PathMatchTypePrefix,
		http.HandlerFunc(ph.PathHandler(conf.MgmtConfig.PurgeByPathHandlerPath, &clients)))
	if listenerEnabledOn(conf.MgmtConfig.PprofListener, mgmt.ListenerNameMgmt) {
		pprof.RegisterRoutes(mgmt.ListenerNameMgmt, managementRouter)
	}
	mgmtRoutes = slices.DeleteFunc(mgmtRoutes, func(route mgmtRoute) bool {
		return route.path == "" || route.handler == nil
	})
	for _, route := range mgmtRoutes {
		managementRouter.RegisterRoute(route.path, nil, nil,
			matching.PathMatchTypeExact, route.handler)
	}

	// requests that miss every backend route are logged by the default
	// access log at the router level, on every listener but metrics
	routerLogger := routing.RouterAccessLogger(conf)
	newListeners := desiredListeners(conf, listenerRouters, managementRouter, metricsRouter,
		routerLogger, mgmtRoutes)
	oldListeners := desiredListeners(oldConf, nil, nil, nil, nil, nil)
	drainTimeout := conf.MgmtConfig.ReloadDrainTimeout

	// Stop removed or network-changed endpoints first. This permits safe port
	// swaps while leaving every unchanged endpoint serving on its existing socket.
	for key, old := range oldListeners {
		current, ok := newListeners[key]
		if ok && !runtimeListenerNeedsRestart(lg, key, old, current) {
			continue
		}
		_ = lg.DrainAndClose(key, time.Duration(drainTimeout))
	}

	names := make([]string, 0, len(newListeners))
	for key := range newListeners {
		names = append(names, key)
	}
	slices.Sort(names)
	tracersAssigned := false
	for _, key := range names {
		desired := newListeners[key]
		old, existed := oldListeners[key]
		if existed && !runtimeListenerNeedsRestart(lg, key, old, desired) && lg.Get(key) != nil {
			if desired.stream {
				updateStreamListener(lg, key, streamConfig(conf, desired, clients))
				continue
			}
			lg.UpdateRouter(key, desired.router)
			if desired.native != nil {
				request := nativeBuildRequest(conf, desired, tracers, clients)
				if adapter, ok := desired.native.(native.HTTPHandlerAdapter); ok {
					h, err := adapter.Handler(request)
					if err != nil {
						logger.Error("unable to update native handler", logging.Pairs{keys.ListenerName: desired.listenerName, keys.Error: err.Error()})
					} else {
						lg.UpdateProtocolHandler(key, h)
					}
				}

				if resolver := desired.native.RouteResolver(request); resolver != nil {
					lg.UpdateProtocolRouteResolver(key, resolver)
				}
				if tlsConfig, err := conf.TLSCertConfigForListener(desired.listenerName); err == nil {
					lg.UpdateProtocolTLSConfig(key, tlsConfig)
				} else {
					logger.Error("unable to rotate native listener TLS", logging.Pairs{
						keys.ListenerName: desired.listenerName, keys.Error: err.Error(),
					})
				}
			}
			if desired.tls {
				updateListenerCertificates(conf, desired, lg)
			}
			continue
		}

		if desired.stream {
			startStreamListener(lg, desired, streamConfig(conf, desired, clients), errorFunc)
			continue
		}

		if desired.native != nil {
			svr, err := desired.native.Build(nativeBuildRequest(conf, desired, tracers, clients))
			if err != nil {
				logger.Error("unable to configure native protocol server", logging.Pairs{
					keys.ListenerName: desired.listenerName, "protocol": desired.options.Protocol,
					keys.Error: err.Error(),
				})
				continue
			}
			go lg.StartProtocolListener(key, desired.options.Protocol,
				desired.address, desired.port, desired.options.ConnectionsLimit,
				svr, errorFunc, proxyProtocolOptions(desired.options))
			continue
		}

		if desired.http3 {
			tlsConfig, err := conf.TLSCertConfigForListener(desired.listenerName)
			if err != nil {
				logger.Error("unable to start HTTP/3 listener", logging.Pairs{
					keys.ListenerName: desired.listenerName, keys.Error: err.Error(),
				})
				continue
			}
			readHeaderTimeout := time.Duration(desired.options.ReadHeaderTimeout)
			advertised := desired.advertisedPort
			go lg.StartPacketListener(desired.key, listenerconfig.ProtocolHTTP3,
				desired.address, desired.port, tlsConfig, desired.router,
				func(h http.Handler, tc *tls.Config) listener.PacketServer {
					return listenerhttp3.NewServer(h, tc, advertised, readHeaderTimeout)
				}, errorFunc)
			continue
		}

		var tlsConfig *tls.Config
		if desired.tls {
			config, err := conf.TLSCertConfigForListener(desired.listenerName)
			if err != nil {
				logger.Error("unable to start TLS listener", logging.Pairs{
					keys.ListenerName: desired.listenerName, keys.Error: err.Error(),
				})
				continue
			}
			tlsConfig = config
		}
		var listenerTracers tracing.Tracers
		if !tracersAssigned && desired.listenerName != mgmt.ListenerNameMgmt &&
			desired.listenerName != mgmt.ListenerNameMetrics {
			listenerTracers = tracers
			tracersAssigned = true
		}
		go lg.StartListener(key, desired.address, desired.port,
			desired.options.ConnectionsLimit, tlsConfig, desired.router,
			listenerTracers, errorFunc, time.Duration(desired.options.ReadHeaderTimeout),
			proxyProtocolOptions(desired.options))
	}
}

func desiredListeners(conf *config.Config, listenerRouters map[string]router.Router,
	managementRouter, metricsRouter router.Router, routerLogger *accesslog.Logger,
	reserved []mgmtRoute,
) map[string]desiredListener {
	out := make(map[string]desiredListener)
	if conf == nil {
		return out
	}
	nativeListeners := providerregistry.NativeListeners()
	for name, options := range conf.Listeners {
		if options == nil || !options.Active {
			continue
		}
		if options.IsStream() {
			if options.ListenPort > 0 {
				key := listenerKey(name, options.Protocol, false)
				out[key] = desiredListener{
					key: key, listenerName: name,
					address: options.ListenAddress, port: options.ListenPort,
					options: options, stream: true,
				}
			}
			continue
		}
		if adapter := nativeListeners.Get(options.Protocol); adapter != nil {
			descriptor, err := adapter.Describe(conf, name)
			if err != nil {
				logger.Error("native listener has no usable backend configuration",
					logging.Pairs{
						keys.ListenerName: name, "protocol": options.Protocol,
						keys.Detail: err.Error(),
					})
				continue
			}
			if options.ListenPort > 0 {
				key := listenerKey(name, options.Protocol, false)
				out[key] = desiredListener{
					key: key, listenerName: name,
					address: options.ListenAddress, port: options.ListenPort,
					options: options, origin: descriptor.RestartKey, native: adapter,
				}
			}
			continue
		}
		var r http.Handler
		switch name {
		case mgmt.ListenerNameMgmt:
			r = accesslog.RouterMiddleware(routerLogger, managementRouter)
		case mgmt.ListenerNameMetrics:
			r = metricsRouter
		default:
			r = accesslog.RouterMiddleware(routerLogger,
				guardReservedRoutes(reserved, listenerRouters[name]))
		}
		// the client IP is resolved outside the access log so unmatched
		// requests are attributed to the real client too
		r = clientip.Middleware(trustedProxies(options), r)
		if options.ListenPort > 0 {
			key := listenerKey(name, options.Protocol, false)
			out[key] = desiredListener{
				key: key, listenerName: name,
				address: options.ListenAddress, port: options.ListenPort,
				options: options, router: r,
			}
		}
		if options.ServeTLS && options.TLSListenPort > 0 {
			key := listenerKey(name, options.Protocol, true)
			tlsRouter := r
			if h3Address, h3Port, advertised := options.HTTP3Endpoint(); h3Port > 0 {
				// the TLS endpoint advertises the alternative service, which is
				// how clients discover they may switch to HTTP/3
				tlsRouter = listenerhttp3.AltSvcAdvertiser(r, advertised)
				h3Key := listenerKey(name, listenerconfig.ProtocolHTTP3, false)
				out[h3Key] = desiredListener{
					key: h3Key, listenerName: name,
					address: h3Address, port: h3Port, advertisedPort: advertised,
					tls: true, http3: true, options: options, router: r,
				}
			}
			out[key] = desiredListener{
				key: key, listenerName: name,
				address: options.TLSListenAddress, port: options.TLSListenPort,
				tls: true, options: options, router: tlsRouter,
			}
		}
	}
	return out
}

// streamConfig builds a stream listener's routing table from the backends mapped to it: a tls
// listener routes by each backend's hosts, and a tcp or udp listener relays to its one backend
func streamConfig(conf *config.Config, desired desiredListener, clients backends.Backends) *l4.Config {
	// a pool member carries the listener name too, but is reached through its pool
	members := conf.Backends.PoolMembers()
	table := l4.NewTable()
	for _, backendName := range slices.Sorted(maps.Keys(conf.Backends)) {
		o := conf.Backends[backendName]
		if o == nil || o.IsTemplate || members.Contains(backendName) ||
			!o.UsesListener(desired.listenerName) {
			continue
		}
		up := l4.FromBackend(clients.Get(backendName))
		if up == nil {
			logger.Error("stream listener backend has no dialable origin", logging.Pairs{
				keys.ListenerName: desired.listenerName, keys.BackendName: backendName,
			})
			continue
		}
		hosts := o.Hosts
		if desired.options.Protocol != listenerconfig.ProtocolTLS || len(hosts) == 0 {
			hosts = []string{""}
		}
		for _, h := range hosts {
			if err := table.Add(h, up); err != nil {
				// validation refused the duplicates, so this names a bug rather than a config
				logger.Error("stream listener route not added", logging.Pairs{
					keys.ListenerName: desired.listenerName, keys.BackendName: backendName,
					keys.Error: err.Error(),
				})
			}
		}
	}
	// the relay bounds its own connections and sessions, keeping the accepted connection's
	// half-close reachable rather than wrapping it in the limiting listener
	return &l4.Config{
		Table: table, Options: desired.options.Stream,
		MaxConnections: desired.options.ConnectionsLimit,
	}
}

func startStreamListener(lg *listener.Group, desired desiredListener, cfg *l4.Config, errorFunc func()) {
	protocol := desired.options.Protocol
	if protocol == listenerconfig.ProtocolUDP {
		svr := l4.NewPacketServer(desired.listenerName, cfg)
		go lg.StartDatagramListener(desired.key, protocol, desired.address, desired.port, svr, errorFunc)
		return
	}
	svr := l4.NewServer(desired.listenerName, protocol, cfg)
	go lg.StartProtocolListener(desired.key, protocol, desired.address, desired.port,
		0, svr, errorFunc, proxyProtocolOptions(desired.options))
}

func updateStreamListener(lg *listener.Group, key string, cfg *l4.Config) {
	if svr, ok := listener.ProtocolServerAs[*l4.Server](lg, key); ok {
		svr.Update(cfg)
		return
	}
	if svr, ok := listener.ProtocolServerAs[*l4.PacketServer](lg, key); ok {
		svr.Update(cfg)
	}
}

func nativeBuildRequest(conf *config.Config, desired desiredListener, tracers tracing.Tracers,
	clients backends.Backends,
) native.BuildRequest {
	return native.BuildRequest{
		Config: conf, ListenerName: desired.listenerName, Listener: desired.options,
		Tracers: tracers, BackendClients: clients,
	}
}

func listenerKey(listenerName, protocol string, tls bool) string {
	return listener.GroupKey(listenerName, protocol, tls)
}

func listenerNeedsRestart(old, current desiredListener) bool {
	return old.address != current.address || old.port != current.port || old.tls != current.tls ||
		old.origin != current.origin || old.advertisedPort != current.advertisedPort ||
		old.options.ConnectionsLimit != current.options.ConnectionsLimit ||
		old.options.ReadHeaderTimeout != current.options.ReadHeaderTimeout ||
		old.options.ProxyProtocol != current.options.ProxyProtocol ||
		(old.options.ProxyProtocol && !slices.Equal(old.options.TrustedProxies, current.options.TrustedProxies))
}

func trustedProxies(options *listenerconfig.Options) clientip.Trusted {
	// validation has already checked the list, so a parse failure here trusts no proxy
	trusted, err := clientip.ParseTrusted(options.TrustedProxies)
	if err != nil {
		return nil
	}
	return trusted
}

func proxyProtocolOptions(options *listenerconfig.Options) *listener.ProxyProtocolOptions {
	return listener.NewProxyProtocolOptions(options.ProxyProtocol, trustedProxies(options))
}

func runtimeListenerNeedsRestart(lg *listener.Group, key string, old, current desiredListener) bool {
	if listenerNeedsRestart(old, current) {
		return true
	}
	if current.native != nil {
		if runningKey, ok := lg.ProtocolRestartKey(key); ok {
			return runningKey != current.origin
		}
	}
	return false
}

func registerConfigRoutes(conf *config.Config, r router.Router, lg *listener.Group) {
	r.RegisterRoute(conf.MgmtConfig.ConfigHandlerPath, nil, nil,
		matching.PathMatchTypeExact, http.HandlerFunc(ch.HandlerFunc(conf)))
	r.RegisterRoute(ch.SanitizedHandlerPath(conf.MgmtConfig.ConfigHandlerPath), nil, nil,
		matching.PathMatchTypeExact, http.HandlerFunc(ch.SanitizedHandlerFunc(conf)))
	if conf.MgmtConfig.CertificatesHandlerPath != "" {
		r.RegisterRoute(conf.MgmtConfig.CertificatesHandlerPath, nil, nil,
			matching.PathMatchTypeExact, http.HandlerFunc(certs.HandlerFunc(lg)))
	}
}

func updateListenerCertificates(conf *config.Config, desired desiredListener, lg *listener.Group) {
	tlsConfig, err := conf.TLSCertConfigForListener(desired.listenerName)
	if err != nil {
		logger.Error("unable to update TLS listener certificates", logging.Pairs{
			keys.ListenerName: desired.listenerName, keys.Error: err.Error(),
		})
		return
	}
	if tlsConfig == nil {
		return
	}
	l := lg.Get(desired.key)
	if l == nil || l.CertSwapper() == nil {
		return
	}
	store, ok := l.CertSwapper().(tr.CertStore)
	if !ok {
		l.CertSwapper().SetCerts(tlsConfig.Certificates)
		return
	}
	// replace only config-sourced entries so certificates supplied at runtime
	// survive the reload
	store.ReplaceKinds(tr.NewConfigEntries(tlsConfig.Certificates), tr.SourceKindConfig)
}

func listenerEnabledOn(configuredListener, listenerName string) bool {
	return configuredListener == mgmt.ListenerNameBoth || configuredListener == listenerName
}
