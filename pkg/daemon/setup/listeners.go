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
	"fmt"
	"net/http"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/pprof"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	ch "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/config"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
)

type desiredListener struct {
	key          string
	listenerName string
	address      string
	port         int
	tls          bool
	options      *listenerconfig.Options
	router       router.Router
}

func applyListenerConfigs(conf, oldConf *config.Config,
	listenerRouters map[string]router.Router, reloadHandler http.Handler,
	metricsRouter router.Router, tracers tracing.Tracers, backends backends.Backends,
	errorFunc func(), lg *listener.Group,
) {
	if conf == nil || len(conf.Listeners) == 0 {
		return
	}

	metricsRouter.RegisterRoute("/metrics", nil, nil, false, metrics.Handler())
	if listenerEnabledOn(conf.MgmtConfig.ConfigHandlerListener, mgmt.ListenerNameMetrics) {
		registerConfigRoutes(conf, metricsRouter)
	}
	if listenerEnabledOn(conf.MgmtConfig.PprofListener, mgmt.ListenerNameMetrics) {
		pprof.RegisterRoutes(mgmt.ListenerNameMetrics, metricsRouter)
	}

	managementRouter := lm.NewRouter()
	if listenerEnabledOn(conf.MgmtConfig.ConfigHandlerListener, mgmt.ListenerNameMgmt) {
		registerConfigRoutes(conf, managementRouter)
	}
	managementRouter.RegisterRoute(conf.MgmtConfig.ReloadHandlerPath, nil, nil,
		false, reloadHandler)
	managementRouter.RegisterRoute(conf.MgmtConfig.PurgeByPathHandlerPath, nil, nil,
		true, http.HandlerFunc(ph.PathHandler(conf.MgmtConfig.PurgeByPathHandlerPath, &backends)))
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
		if ok && !listenerNeedsRestart(old, current) {
			continue
		}
		_ = lg.DrainAndClose(key, drainTimeout)
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
		if existed && !listenerNeedsRestart(old, desired) && lg.Get(key) != nil {
			lg.UpdateRouter(key, desired.router)
			if desired.tls {
				updateListenerCertificates(conf, desired, lg)
			}
			continue
		}

		var tlsConfig *tls.Config
		if desired.tls {
			config, err := conf.TLSCertConfigForListener(desired.listenerName)
			if err != nil {
				logger.Error("unable to start TLS listener", logging.Pairs{
					"listenerName": desired.listenerName, "error": err.Error(),
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
			listenerTracers, errorFunc, drainTimeout, desired.options.ReadHeaderTimeout)
	}
}

func desiredListeners(conf *config.Config, listenerRouters map[string]router.Router,
	managementRouter, metricsRouter router.Router,
) map[string]desiredListener {
	out := make(map[string]desiredListener)
	if conf == nil {
		return out
	}
	for name, options := range conf.Listeners {
		if options == nil || !options.Active {
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
			key := listenerKey(name, false)
			out[key] = desiredListener{
				key: key, listenerName: name,
				address: options.ListenAddress, port: options.ListenPort,
				options: options, router: r,
			}
		}
		if options.ServeTLS && options.TLSListenPort > 0 {
			key := listenerKey(name, true)
			out[key] = desiredListener{
				key: key, listenerName: name,
				address: options.TLSListenAddress, port: options.TLSListenPort,
				tls: true, options: options, router: r,
			}
		}
	}
	return out
}

func listenerKey(listenerName string, tls bool) string {
	scheme := listenerconfig.ProtocolHTTP
	if tls {
		scheme = "https"
	}
	return fmt.Sprintf("listener.%s.%s", listenerName, scheme)
}

func listenerNeedsRestart(old, current desiredListener) bool {
	return old.address != current.address || old.port != current.port || old.tls != current.tls ||
		old.options.ConnectionsLimit != current.options.ConnectionsLimit ||
		old.options.ReadHeaderTimeout != current.options.ReadHeaderTimeout
}

func registerConfigRoutes(conf *config.Config, r router.Router) {
	r.RegisterRoute(conf.MgmtConfig.ConfigHandlerPath, nil, nil,
		false, http.HandlerFunc(ch.HandlerFunc(conf)))
	r.RegisterRoute(ch.SanitizedHandlerPath(conf.MgmtConfig.ConfigHandlerPath), nil, nil,
		false, http.HandlerFunc(ch.SanitizedHandlerFunc(conf)))
}

func updateListenerCertificates(conf *config.Config, desired desiredListener, lg *listener.Group) {
	tlsConfig, err := conf.TLSCertConfigForListener(desired.listenerName)
	if err != nil {
		logger.Error("unable to update TLS listener certificates", logging.Pairs{
			"listenerName": desired.listenerName, "error": err.Error(),
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
