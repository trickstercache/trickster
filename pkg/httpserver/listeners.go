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

package httpserver

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	ttls "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	"github.com/trickstercache/trickster/v2/pkg/router"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

var lg = listener.NewListenerGroup()

func applyListenerConfigs(conf, oldConf *config.Config,
	router, reloadHandler http.Handler, metricsRouter router.Router,
	logger logging.Logger, tracers tracing.Tracers, o backends.Backends,
	wg *sync.WaitGroup, errorFunc func()) {

	var err error
	var tlsConfig *tls.Config

	if conf == nil || conf.Frontend == nil {
		return
	}

	adminRouter := http.NewServeMux()
	adminRouter.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)
	adminRouter.HandleFunc(conf.Main.PurgePathHandlerPath, handlers.PurgePathHandlerFunc(conf, &o))

	// No changes in frontend config
	if oldConf != nil && oldConf.Frontend != nil &&
		oldConf.Frontend.Equal(conf.Frontend) {
		lg.UpdateFrontendRouters(router, adminRouter)
		if ttls.OptionsChanged(conf, oldConf) {
			tlsConfig, _ = conf.TLSCertConfig()
			l := lg.Get("tlsListener")
			if l != nil {
				cs := l.CertSwapper()
				if cs != nil {
					cs.SetCerts(tlsConfig.Certificates)
				}
			}
		}
	}

	if oldConf != nil && oldConf.Frontend.ConnectionsLimit != conf.Frontend.ConnectionsLimit {
		logger.Warn("connections limit change requires a process restart. listeners not updated.",
			logging.Pairs{"oldLimit": oldConf.Frontend.ConnectionsLimit,
				"newLimit": conf.Frontend.ConnectionsLimit})
		return
	}

	hasOldFC := oldConf != nil && oldConf.Frontend != nil
	hasOldMC := oldConf != nil && oldConf.Metrics != nil
	hasOldRC := oldConf != nil && oldConf.ReloadConfig != nil
	drainTimeout := time.Duration(conf.ReloadConfig.DrainTimeoutMS) * time.Millisecond
	var tracerFlusherSet bool

	// if TLS port is configured and at least one origin is mapped to a good tls config,
	// then set up the tls server listener instance
	if conf.Frontend.ServeTLS && conf.Frontend.TLSListenPort > 0 && (!hasOldFC ||
		!oldConf.Frontend.ServeTLS ||
		(oldConf.Frontend.TLSListenAddress != conf.Frontend.TLSListenAddress ||
			oldConf.Frontend.TLSListenPort != conf.Frontend.TLSListenPort)) {
		lg.DrainAndClose("tlsListener", drainTimeout)
		tlsConfig, err = conf.TLSCertConfig()
		if err != nil {
			logger.Error("unable to start tls listener due to certificate error",
				logging.Pairs{"detail": err})
		} else {
			wg.Add(1)
			tracerFlusherSet = true
			go lg.StartListener("tlsListener",
				conf.Frontend.TLSListenAddress, conf.Frontend.TLSListenPort,
				conf.Frontend.ConnectionsLimit, tlsConfig, router, wg, tracers, errorFunc,
				time.Duration(conf.ReloadConfig.DrainTimeoutMS)*time.Millisecond, logger)
		}
	} else if !conf.Frontend.ServeTLS && hasOldFC && oldConf.Frontend.ServeTLS {
		// the TLS configs have been removed between the last config load and this one,
		// the TLS listener port needs to be stopped
		lg.DrainAndClose("tlsListener", drainTimeout)
	} else if conf.Frontend.ServeTLS && ttls.OptionsChanged(conf, oldConf) {
		tlsConfig, err = conf.TLSCertConfig()
		if err != nil {
			logger.Error("unable to update tls config to certificate error",
				logging.Pairs{"detail": err})
			return
		}
		l := lg.Get("tlsListener")
		if l != nil {
			cs := l.CertSwapper()
			if cs != nil {
				cs.SetCerts(tlsConfig.Certificates)
			}
		}
	}

	// if the plaintext HTTP port is configured, then set up the http listener instance
	if conf.Frontend.ListenPort > 0 && (!hasOldFC ||
		(oldConf.Frontend.ListenAddress != conf.Frontend.ListenAddress ||
			oldConf.Frontend.ListenPort != conf.Frontend.ListenPort)) {
		lg.DrainAndClose("httpListener", drainTimeout)
		wg.Add(1)
		var t2 tracing.Tracers
		if !tracerFlusherSet {
			t2 = tracers
		}
		go lg.StartListener("httpListener",
			conf.Frontend.ListenAddress, conf.Frontend.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, router, wg, t2, errorFunc, 0, logger)
	}

	// if the Metrics HTTP port is configured, then set up the http listener instance
	if conf.Metrics != nil && conf.Metrics.ListenPort > 0 &&
		(!hasOldMC || (conf.Metrics.ListenAddress != oldConf.Metrics.ListenAddress ||
			conf.Metrics.ListenPort != oldConf.Metrics.ListenPort)) {
		lg.DrainAndClose("metricsListener", 0)
		metricsRouter.RegisterRoute("/metrics", nil, nil,
			false, metrics.Handler())
		metricsRouter.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(handlers.ConfigHandleFunc(conf)))
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "metrics" {
			routing.RegisterPprofRoutes("metrics", metricsRouter, logger)
		}
		wg.Add(1)
		go lg.StartListener("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, metricsRouter, wg, nil, errorFunc, 0, logger)
	} else {
		metricsRouter.RegisterRoute("/metrics", nil, nil,
			false, metrics.Handler())
		metricsRouter.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(handlers.ConfigHandleFunc(conf)))
		lg.UpdateRouter("metricsListener", metricsRouter)
	}

	rr := lm.NewRouter()    // router for the Reload port
	rr.SetMatchingScheme(0) // reload router is exact-match only

	// if the Reload HTTP port is configured, then set up the http listener instance
	if conf.ReloadConfig != nil && conf.ReloadConfig.ListenPort > 0 &&
		(!hasOldRC || (conf.ReloadConfig.ListenAddress != oldConf.ReloadConfig.ListenAddress ||
			conf.ReloadConfig.ListenPort != oldConf.ReloadConfig.ListenPort)) {
		wg.Add(1)
		lg.DrainAndClose("reloadListener", time.Millisecond*500)
		rr.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(handlers.ConfigHandleFunc(conf)))
		rr.RegisterRoute(conf.ReloadConfig.HandlerPath, nil, nil,
			false, reloadHandler)
		rr.RegisterRoute(conf.Main.PurgePathHandlerPath, nil, nil,
			false, http.HandlerFunc(handlers.PurgePathHandlerFunc(conf, &o)))
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "reload" {
			routing.RegisterPprofRoutes("reload", rr, logger)
		}
		go lg.StartListener("reloadListener",
			conf.ReloadConfig.ListenAddress, conf.ReloadConfig.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, rr, wg, nil, errorFunc, 0, logger)
	} else {
		rr.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(handlers.ConfigHandleFunc(conf)))
		rr.RegisterRoute(conf.ReloadConfig.HandlerPath, nil, nil,
			false, reloadHandler)
		rr.RegisterRoute(conf.Main.PurgePathHandlerPath, nil, nil,
			false, http.HandlerFunc(handlers.PurgePathHandlerFunc(conf, &o)))
		lg.UpdateRouter("reloadListener", rr)
	}
}
