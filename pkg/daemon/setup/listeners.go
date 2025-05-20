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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	ch "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/config"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	ttls "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
	"github.com/trickstercache/trickster/v2/pkg/router"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

var lg = listener.NewGroup()

func applyListenerConfigs(conf, oldConf *config.Config,
	router, reloadHandler http.Handler, metricsRouter router.Router,
	tracers tracing.Tracers, o backends.Backends, errorFunc func()) {

	var err error
	var tlsConfig *tls.Config

	if conf == nil || conf.Frontend == nil {
		return
	}

	adminRouter := http.NewServeMux()
	adminRouter.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)
	adminRouter.HandleFunc(conf.Main.PurgePathHandlerPath, ph.HandlerFunc(conf, &o))

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
	drainTimeout := conf.ReloadConfig.DrainTimeout
	var tracerFlusherSet bool

	// if TLS port is configured and at least one origin is mapped to a good tls config,
	// then set up the tls server listener instance
	switch {
	case conf.Frontend.ServeTLS && conf.Frontend.TLSListenPort > 0 && (!hasOldFC ||
		!oldConf.Frontend.ServeTLS ||
		(oldConf.Frontend.TLSListenAddress != conf.Frontend.TLSListenAddress ||
			oldConf.Frontend.TLSListenPort != conf.Frontend.TLSListenPort)):
		lg.DrainAndClose("tlsListener", drainTimeout)
		tlsConfig, err = conf.TLSCertConfig()
		if err != nil {
			logger.Error("unable to start tls listener due to certificate error",
				logging.Pairs{"detail": err})
		} else {
			tracerFlusherSet = true
			go lg.StartListener("tlsListener",
				conf.Frontend.TLSListenAddress, conf.Frontend.TLSListenPort,
				conf.Frontend.ConnectionsLimit, tlsConfig, router, tracers, errorFunc,
				conf.ReloadConfig.DrainTimeout, conf.Frontend.ReadHeaderTimeout)
		}
	case !conf.Frontend.ServeTLS && hasOldFC && oldConf.Frontend.ServeTLS:
		// the TLS configs have been removed between the last config load and this one,
		// the TLS listener port needs to be stopped
		lg.DrainAndClose("tlsListener", drainTimeout)
	case conf.Frontend.ServeTLS && ttls.OptionsChanged(conf, oldConf):
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
		var t2 tracing.Tracers
		if !tracerFlusherSet {
			t2 = tracers
		}
		go lg.StartListener("httpListener",
			conf.Frontend.ListenAddress, conf.Frontend.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, router, t2, errorFunc, 0, conf.Frontend.ReadHeaderTimeout)
	}

	// if the Metrics HTTP port is configured, then set up the http listener instance
	if conf.Metrics != nil && conf.Metrics.ListenPort > 0 &&
		(!hasOldMC || (conf.Metrics.ListenAddress != oldConf.Metrics.ListenAddress ||
			conf.Metrics.ListenPort != oldConf.Metrics.ListenPort)) {
		lg.DrainAndClose("metricsListener", 0)
		metricsRouter.RegisterRoute("/metrics", nil, nil,
			false, metrics.Handler())
		metricsRouter.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "metrics" {
			routing.RegisterPprofRoutes("metrics", metricsRouter)
		}
		go lg.StartListener("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, metricsRouter, nil, errorFunc, 0, conf.Frontend.ReadHeaderTimeout)
	} else {
		metricsRouter.RegisterRoute("/metrics", nil, nil,
			false, metrics.Handler())
		metricsRouter.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		lg.UpdateRouter("metricsListener", metricsRouter)
	}

	rr := lm.NewRouter()    // router for the Reload port
	rr.SetMatchingScheme(0) // reload router is exact-match only

	// if the Reload HTTP port is configured, then set up the http listener instance
	if conf.ReloadConfig != nil && conf.ReloadConfig.ListenPort > 0 &&
		(!hasOldRC || (conf.ReloadConfig.ListenAddress != oldConf.ReloadConfig.ListenAddress ||
			conf.ReloadConfig.ListenPort != oldConf.ReloadConfig.ListenPort)) {
		lg.DrainAndClose("reloadListener", time.Millisecond*500)
		rr.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		rr.RegisterRoute(conf.ReloadConfig.HandlerPath, nil, nil,
			false, reloadHandler)
		rr.RegisterRoute(conf.Main.PurgePathHandlerPath, nil, nil,
			false, http.HandlerFunc(ph.HandlerFunc(conf, &o)))
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "reload" {
			routing.RegisterPprofRoutes("reload", rr)
		}
		go lg.StartListener("reloadListener",
			conf.ReloadConfig.ListenAddress, conf.ReloadConfig.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, rr, nil, errorFunc, 0, conf.Frontend.ReadHeaderTimeout)
	} else {
		rr.RegisterRoute(conf.Main.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		rr.RegisterRoute(conf.ReloadConfig.HandlerPath, nil, nil,
			false, reloadHandler)
		rr.RegisterRoute(conf.Main.PurgePathHandlerPath, nil, nil,
			false, http.HandlerFunc(ph.HandlerFunc(conf, &o)))
		lg.UpdateRouter("reloadListener", rr)
	}
}
