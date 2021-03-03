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

package main

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/tricksterproxy/trickster/cmd/trickster/config"
	tl "github.com/tricksterproxy/trickster/pkg/observability/logging"
	ph "github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/proxy/listener"
	ttls "github.com/tricksterproxy/trickster/pkg/proxy/tls"
	"github.com/tricksterproxy/trickster/pkg/routing"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	"github.com/tricksterproxy/trickster/pkg/util/metrics"
)

var lg = listener.NewListenerGroup()

func applyListenerConfigs(conf, oldConf *config.Config,
	router, reloadHandler http.Handler, log *tl.Logger,
	tracers tracing.Tracers) {

	var err error
	var tlsConfig *tls.Config

	if conf == nil || conf.Frontend == nil {
		return
	}

	adminRouter := http.NewServeMux()
	adminRouter.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)

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
		tl.Warn(log, "connections limit change requires a process restart. listeners not updated.",
			tl.Pairs{"oldLimit": oldConf.Frontend.ConnectionsLimit,
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
			tl.Error(log, "unable to start tls listener due to certificate error", tl.Pairs{"detail": err})
		} else {
			wg.Add(1)
			tracerFlusherSet = true
			go lg.StartListener("tlsListener",
				conf.Frontend.TLSListenAddress, conf.Frontend.TLSListenPort,
				conf.Frontend.ConnectionsLimit, tlsConfig, router, wg, tracers, true,
				time.Duration(conf.ReloadConfig.DrainTimeoutMS)*time.Millisecond, log)
		}
	} else if !conf.Frontend.ServeTLS && hasOldFC && oldConf.Frontend.ServeTLS {
		// the TLS configs have been removed between the last config load and this one,
		// the TLS listener port needs to be stopped
		lg.DrainAndClose("tlsListener", drainTimeout)
	} else if conf.Frontend.ServeTLS && ttls.OptionsChanged(conf, oldConf) {
		tlsConfig, _ = conf.TLSCertConfig()
		if err != nil {
			tl.Error(log, "unable to update tls config to certificate error", tl.Pairs{"detail": err})
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
			conf.Frontend.ConnectionsLimit, nil, router, wg, t2, true, 0, log)
	}

	// if the Metrics HTTP port is configured, then set up the http listener instance
	if conf.Metrics != nil && conf.Metrics.ListenPort > 0 &&
		(!hasOldMC || (conf.Metrics.ListenAddress != oldConf.Metrics.ListenAddress ||
			conf.Metrics.ListenPort != oldConf.Metrics.ListenPort)) {
		lg.DrainAndClose("metricsListener", 0)
		mr := http.NewServeMux()
		mr.Handle("/metrics", metrics.Handler())
		mr.HandleFunc(conf.Main.ConfigHandlerPath, ph.ConfigHandleFunc(conf))
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "metrics" {
			routing.RegisterPprofRoutes("metrics", mr, log)
		}
		wg.Add(1)
		go lg.StartListener("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, mr, wg, nil, true, 0, log)
	} else {
		mr := http.NewServeMux()
		mr.Handle("/metrics", metrics.Handler())
		mr.HandleFunc(conf.Main.ConfigHandlerPath, ph.ConfigHandleFunc(conf))
		lg.UpdateRouter("metricsListener", mr)
	}

	// if the Reload HTTP port is configured, then set up the http listener instance
	if conf.ReloadConfig != nil && conf.ReloadConfig.ListenPort > 0 &&
		(!hasOldRC || (conf.ReloadConfig.ListenAddress != oldConf.ReloadConfig.ListenAddress ||
			conf.ReloadConfig.ListenPort != oldConf.ReloadConfig.ListenPort)) {
		wg.Add(1)
		lg.DrainAndClose("reloadListener", time.Millisecond*500)
		mr := http.NewServeMux()
		mr.HandleFunc(conf.Main.ConfigHandlerPath, ph.ConfigHandleFunc(conf))
		mr.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "reload" {
			routing.RegisterPprofRoutes("reload", mr, log)
		}
		go lg.StartListener("reloadListener",
			conf.ReloadConfig.ListenAddress, conf.ReloadConfig.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, mr, wg, nil, true, 0, log)
	} else {
		mr := http.NewServeMux()
		mr.HandleFunc(conf.Main.ConfigHandlerPath, ph.ConfigHandleFunc(conf))
		mr.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)
		lg.UpdateRouter("reloadListener", mr)
	}
}
