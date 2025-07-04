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
	"github.com/trickstercache/trickster/v2/pkg/observability/pprof"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	ch "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/config"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	ttls "github.com/trickstercache/trickster/v2/pkg/proxy/tls"
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

	// No changes in frontend config
	if oldConf != nil && oldConf.Frontend != nil &&
		oldConf.Frontend.Equal(conf.Frontend) {
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
	hasOldRC := oldConf != nil && oldConf.MgmtConfig != nil
	drainTimeout := conf.MgmtConfig.ReloadDrainTimeout
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
				conf.MgmtConfig.ReloadDrainTimeout, conf.Frontend.ReadHeaderTimeout)
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
		metricsRouter.RegisterRoute(conf.MgmtConfig.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		if conf.MgmtConfig.PprofServer == "both" || conf.MgmtConfig.PprofServer == "metrics" {
			pprof.RegisterRoutes("metrics", metricsRouter)
		}
		go lg.StartListener("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, metricsRouter, nil, errorFunc, 0, conf.Frontend.ReadHeaderTimeout)
	} else {
		metricsRouter.RegisterRoute("/metrics", nil, nil,
			false, metrics.Handler())
		metricsRouter.RegisterRoute(conf.MgmtConfig.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		lg.UpdateRouter("metricsListener", metricsRouter)
	}

	mr := lm.NewRouter() // management router
	// if the Management HTTP port is configured, then set up the http listener instance
	if conf.MgmtConfig != nil && conf.MgmtConfig.ListenPort > 0 &&
		(!hasOldRC || (conf.MgmtConfig.ListenAddress != oldConf.MgmtConfig.ListenAddress ||
			conf.MgmtConfig.ListenPort != oldConf.MgmtConfig.ListenPort)) {
		lg.DrainAndClose("mgmtListener", time.Millisecond*500)
		mr.RegisterRoute(conf.MgmtConfig.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		mr.RegisterRoute(conf.MgmtConfig.ReloadHandlerPath, nil, nil,
			false, reloadHandler)
		mr.RegisterRoute(conf.MgmtConfig.PurgeByPathHandlerPath, nil, nil,
			true, http.HandlerFunc(ph.PathHandler(conf.MgmtConfig.PurgeByPathHandlerPath, &o)))
		if conf.MgmtConfig.PprofServer == "both" || conf.MgmtConfig.PprofServer == "mgmt" {
			pprof.RegisterRoutes("mgmt", mr)
		}
		go lg.StartListener("mgmtListener",
			conf.MgmtConfig.ListenAddress, conf.MgmtConfig.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, mr, nil, errorFunc, 0, conf.Frontend.ReadHeaderTimeout)
	} else {
		mr.RegisterRoute(conf.MgmtConfig.ConfigHandlerPath, nil, nil,
			false, http.HandlerFunc(ch.HandlerFunc(conf)))
		mr.RegisterRoute(conf.MgmtConfig.ReloadHandlerPath, nil, nil,
			false, reloadHandler)
		mr.RegisterRoute(conf.MgmtConfig.PurgeByPathHandlerPath, nil, nil,
			true, http.HandlerFunc(ph.PathHandler(conf.MgmtConfig.PurgeByPathHandlerPath, &o)))
		lg.UpdateRouter("mgmtListener", mr)
	}
}
