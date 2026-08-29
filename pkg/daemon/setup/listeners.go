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
	"net/http"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/pprof"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	certs "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/certificates"
	ch "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/config"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

const (
	logKeyError        = "error"
	logKeyDetail       = "detail"
	logKeyListenerName = "listenerName"
)

type desiredListener struct {
	key          string
	listenerName string
	address      string
	port         int
	tls          bool
	options      *listenerconfig.Options
	router       router.Router
	// origin identifies native protocol configuration for restart detection.
	origin string
	native native.Adapter
}

func applyListenerConfigs(conf, oldConf *config.Config,
	listenerRouters map[string]router.Router, reloadHandler http.Handler,
	metricsRouter router.Router, tracers tracing.Tracers, clients backends.Backends,
	errorFunc func(), lg *listener.Group,
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

	newListeners := desiredListeners(conf, listenerRouters, managementRouter, metricsRouter)
	oldListeners := desiredListeners(oldConf, nil, nil, nil)
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
			lg.UpdateRouter(key, desired.router)
			if desired.native != nil {
				request := nativeBuildRequest(conf, desired, tracers, clients)
				if adapter, ok := desired.native.(native.HTTPHandlerAdapter); ok {
					h, err := adapter.Handler(request)
					if err != nil {
						logger.Error("unable to update native handler", logging.Pairs{logKeyListenerName: desired.listenerName, logKeyError: err.Error()})
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
						logKeyListenerName: desired.listenerName, logKeyError: err.Error(),
					})
				}
			}
			if desired.tls {
				updateListenerCertificates(conf, desired, lg)
			}
			continue
		}

		if desired.native != nil {
			svr, err := desired.native.Build(nativeBuildRequest(conf, desired, tracers, clients))
			if err != nil {
				logger.Error("unable to configure native protocol server", logging.Pairs{
					logKeyListenerName: desired.listenerName, "protocol": desired.options.Protocol,
					logKeyError: err.Error(),
				})
				continue
			}
			go lg.StartProtocolListener(key, desired.options.Protocol,
				desired.address, desired.port, desired.options.ConnectionsLimit,
				svr, errorFunc, time.Duration(drainTimeout))
			continue
		}

		var tlsConfig *tls.Config
		if desired.tls {
			config, err := conf.TLSCertConfigForListener(desired.listenerName)
			if err != nil {
				logger.Error("unable to start TLS listener", logging.Pairs{
					logKeyListenerName: desired.listenerName, logKeyError: err.Error(),
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
			listenerTracers, errorFunc, time.Duration(drainTimeout), time.Duration(desired.options.ReadHeaderTimeout))
	}
}

func desiredListeners(conf *config.Config, listenerRouters map[string]router.Router,
	managementRouter, metricsRouter router.Router,
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
		if adapter := nativeListeners.Get(options.Protocol); adapter != nil {
			descriptor, err := adapter.Describe(conf, name)
			if err != nil {
				logger.Error("native listener has no usable backend configuration",
					logging.Pairs{
						logKeyListenerName: name, "protocol": options.Protocol,
						logKeyDetail: err.Error(),
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
		var r router.Router
		switch name {
		case mgmt.ListenerNameMgmt:
			r = managementRouter
		case mgmt.ListenerNameMetrics:
			r = metricsRouter
		default:
			r = listenerRouters[name]
		}
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
			out[key] = desiredListener{
				key: key, listenerName: name,
				address: options.TLSListenAddress, port: options.TLSListenPort,
				tls: true, options: options, router: r,
			}
		}
	}
	return out
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
		old.origin != current.origin ||
		old.options.ConnectionsLimit != current.options.ConnectionsLimit ||
		old.options.ReadHeaderTimeout != current.options.ReadHeaderTimeout
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
			logKeyListenerName: desired.listenerName, logKeyError: err.Error(),
		})
		return
	}
	if tlsConfig == nil || len(tlsConfig.Certificates) == 0 {
		return
	}
	if l := lg.Get(desired.key); l != nil && l.CertSwapper() != nil {
		l.CertSwapper().SetCerts(tlsConfig.Certificates)
	}
}

func listenerEnabledOn(configuredListener, listenerName string) bool {
	return configuredListener == mgmt.ListenerNameBoth || configuredListener == listenerName
}
