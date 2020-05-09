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
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/proxy"
	ph "github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	sw "github.com/tricksterproxy/trickster/pkg/proxy/tls"
	"github.com/tricksterproxy/trickster/pkg/routing"
	"github.com/tricksterproxy/trickster/pkg/tracing"
	"github.com/tricksterproxy/trickster/pkg/util/log"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/metrics"

	"github.com/gorilla/handlers"
)

var listenersLock = &sync.Mutex{}
var listeners = make(map[string]*listenerGroup)

type listenerGroup struct {
	listener     net.Listener
	tlsConfig    *tls.Config
	tlsSwapper   *sw.CertSwapper
	routeSwapper *ph.SwitchHandler
	exitOnError  bool
}

func startListener(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, router http.Handler, wg *sync.WaitGroup, tracers tracing.Tracers,
	exitOnError bool, log *tl.Logger) error {
	if wg != nil {
		defer wg.Done()
	}

	lg := &listenerGroup{routeSwapper: ph.NewSwitchHandler(router), exitOnError: exitOnError}
	if tlsConfig != nil && len(tlsConfig.Certificates) > 0 {
		lg.tlsConfig = tlsConfig
		lg.tlsSwapper = sw.NewSwapper(tlsConfig.Certificates)

		// Replace the normal GetCertificate function in the TLS config with lg.tlsSwapper's,
		// so users swap certs in the config later without restarting the entire process
		tlsConfig.GetCertificate = lg.tlsSwapper.GetCert
		tlsConfig.Certificates = nil
	}

	l, err := proxy.NewListener(address, port, connectionsLimit, tlsConfig, log)
	if err != nil {
		log.Error("http listener startup failed", tl.Pairs{"name": listenerName, "detail": err})
		if exitOnError {
			os.Exit(1)
		}
		return err
	}
	log.Info("http listener starting",
		tl.Pairs{"name": listenerName, "port": port, "address": address})

	lg.listener = l
	listenersLock.Lock()
	listeners[listenerName] = lg
	listenersLock.Unlock()

	// defer the tracer flush here where the listener connection ends
	if tracers != nil {
		for _, v := range tracers {
			if v != nil && v.Flusher != nil {
				defer v.Flusher()
			}
		}
	}

	if tlsConfig != nil {
		svr := &http.Server{
			Handler:   handlers.CompressHandler(lg.routeSwapper),
			TLSConfig: tlsConfig,
		}
		err = svr.Serve(l)
		if err != nil {
			log.Error("https listener stopping", tl.Pairs{"name": listenerName, "detail": err})
			if lg.exitOnError {
				os.Exit(1)
			}
		}
		return err
	}

	err = http.Serve(l, handlers.CompressHandler(lg.routeSwapper))
	if err != nil {
		log.Error("http listener stopping", tl.Pairs{"name": listenerName, "detail": err})
		if lg.exitOnError {
			os.Exit(1)
		}
	}
	return err
}

func startListenerRouter(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, path string, handler http.Handler, wg *sync.WaitGroup,
	tracers tracing.Tracers, exitOnError bool, log *tl.Logger) error {
	router := http.NewServeMux()
	router.Handle(path, handler)
	return startListener(listenerName, address, port, connectionsLimit,
		tlsConfig, router, wg, tracers, exitOnError, log)
}

func applyListenerConfigs(conf, oldConf *config.Config,
	router, reloadHandler http.Handler, log *log.Logger,
	tracers tracing.Tracers) {

	var err error
	var routerRefreshed bool
	var tlsConfig *tls.Config

	if conf == nil || conf.Frontend == nil {
		return
	}

	adminRouter := http.NewServeMux()
	adminRouter.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)

	// No changes in frontend config
	if conf.Frontend != nil && oldConf != nil &&
		oldConf.Frontend != nil && oldConf.Frontend.Equal(conf.Frontend) {
		updateRouters(router, adminRouter)
		if TLSOptionsChanged(conf, oldConf) {
			tlsConfig, _ = conf.TLSCertConfig()
			listenersLock.Lock()
			if lg, ok := listeners["tlsListener"]; ok && lg != nil && lg.tlsSwapper != nil {
				lg.tlsSwapper.SetCerts(tlsConfig.Certificates)
			}
			listenersLock.Unlock()
		}
		return
	}

	if oldConf != nil && oldConf.Frontend.ConnectionsLimit != conf.Frontend.ConnectionsLimit {
		log.Warn("connections limit change requires a process restart. listeners not updated.",
			tl.Pairs{"oldLimit": oldConf.Frontend.ConnectionsLimit,
				"newLimit": conf.Frontend.ConnectionsLimit})
		return
	}

	hasOldFC := oldConf != nil && oldConf.Frontend != nil
	hasOldMC := oldConf != nil && oldConf.Metrics != nil
	hasOldRC := oldConf != nil && oldConf.ReloadConfig != nil

	drainTime := time.Duration(conf.ReloadConfig.DrainTimeoutSecs) * time.Second

	var tracerFlusherSet bool

	// if TLS port is configured and at least one origin is mapped to a good tls config,
	// then set up the tls server listener instance
	if conf.Frontend.ServeTLS && conf.Frontend.TLSListenPort > 0 && (!hasOldFC ||
		!oldConf.Frontend.ServeTLS ||
		(oldConf.Frontend.TLSListenAddress != conf.Frontend.TLSListenAddress ||
			oldConf.Frontend.TLSListenPort != conf.Frontend.TLSListenPort)) {

		spinDownListener("tlsListener", drainTime)

		tlsConfig, err = conf.TLSCertConfig()
		if err != nil {
			log.Error("unable to start tls listener due to certificate error", tl.Pairs{"detail": err})
		} else {
			wg.Add(1)
			routerRefreshed = true
			tracerFlusherSet = true
			go startListener("tlsListener",
				conf.Frontend.TLSListenAddress, conf.Frontend.TLSListenPort,
				conf.Frontend.ConnectionsLimit, tlsConfig, router, wg, tracers, true, log)
		}

	} else if !conf.Frontend.ServeTLS && hasOldFC && oldConf.Frontend.ServeTLS {
		// the TLS configs have been removed between the last config load and this one,
		// the TLS listener port needs to be stopped
		spinDownListener("tlsListener", drainTime)
	} else if conf.Frontend.ServeTLS && TLSOptionsChanged(conf, oldConf) {
		tlsConfig, _ = conf.TLSCertConfig()
		if err != nil {
			log.Error("unable to update tls config to certificate error", tl.Pairs{"detail": err})
			return
		}
		listenersLock.Lock()
		if lg, ok := listeners["tlsListener"]; ok && lg != nil && lg.tlsSwapper != nil {
			lg.tlsSwapper.SetCerts(tlsConfig.Certificates)
		}
		listenersLock.Unlock()
	}

	// if the plaintext HTTP port is configured, then set up the http listener instance
	if conf.Frontend.ListenPort > 0 && (!hasOldFC ||
		(oldConf.Frontend.ListenAddress != conf.Frontend.ListenAddress &&
			oldConf.Frontend.ListenPort != conf.Frontend.ListenPort)) {

		spinDownListener("httpListener", drainTime)
		wg.Add(1)
		routerRefreshed = true

		var t2 tracing.Tracers
		if !tracerFlusherSet {
			t2 = tracers
		}

		go startListener("httpListener",
			conf.Frontend.ListenAddress, conf.Frontend.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, router, wg, t2, true, log)

	}

	// if the Metrics HTTP port is configured, then set up the http listener instance
	if conf.Metrics != nil && conf.Metrics.ListenPort > 0 &&
		(!hasOldMC || (conf.Metrics.ListenAddress != oldConf.Metrics.ListenAddress ||
			conf.Metrics.ListenPort != oldConf.Metrics.ListenPort)) {
		spinDownListener("metricsListener", 0)
		mr := http.NewServeMux()
		mr.Handle("/metrics", metrics.Handler())
		mr.HandleFunc(conf.Main.ConfigHandlerPath, ph.ConfigHandleFunc(conf))
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "metrics" {
			routing.RegisterPprofRoutes("metrics", mr, log)
		}
		wg.Add(1)
		go startListener("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, mr, wg, nil, true, log)
	}

	// if the Reload HTTP port is configured, then set up the http listener instance
	if conf.ReloadConfig != nil && conf.ReloadConfig.ListenPort > 0 &&
		(!hasOldRC || (conf.ReloadConfig.ListenAddress != oldConf.ReloadConfig.ListenAddress ||
			conf.ReloadConfig.ListenPort != oldConf.ReloadConfig.ListenPort)) {
		wg.Add(1)
		spinDownListener("reloadListener", time.Millisecond*500)
		mr := http.NewServeMux()
		mr.HandleFunc(conf.Main.ConfigHandlerPath, ph.ConfigHandleFunc(conf))
		mr.Handle(conf.ReloadConfig.HandlerPath, reloadHandler)
		if conf.Main.PprofServer == "both" || conf.Main.PprofServer == "reload" {
			routing.RegisterPprofRoutes("reload", mr, log)
		}

		go startListener("reloadListener",
			conf.ReloadConfig.ListenAddress, conf.ReloadConfig.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, mr, wg, nil, true, log)
	}

	if routerRefreshed {
		return
	}

}

func updateRouters(mainRouter http.Handler, adminRouter http.Handler) {
	if mainRouter != nil {
		for k, v := range listeners {
			if k == "httpListener" || k == "tlsListener" {
				v.routeSwapper.Update(mainRouter)
				break
			}
		}
	}
	listenersLock.Lock()
	if v, ok := listeners["reloadListener"]; ok && adminRouter != nil {
		v.routeSwapper.Update(adminRouter)
	}
	listenersLock.Unlock()
}

func spinDownListener(listenerName string, drainWait time.Duration) {
	listenersLock.Lock()
	if lg, ok := listeners[listenerName]; ok {
		lg.exitOnError = false
		delete(listeners, listenerName)
		if lg == nil || lg.listener == nil {
			return
		}
		go func() {
			time.Sleep(drainWait)
			lg.listener.Close()
		}()
	}
	listenersLock.Unlock()
}

// TLSOptionsChanged will return true if the TLS options for any origin
// is different between configs
func TLSOptionsChanged(conf, oldConf *config.Config) bool {

	if conf == nil {
		return false
	}
	if oldConf == nil {
		return true
	}

	for k, v := range oldConf.Origins {
		if v.TLS != nil && v.TLS.ServeTLS {
			if o, ok := conf.Origins[k]; !ok ||
				o.TLS == nil || !o.TLS.ServeTLS ||
				!o.TLS.Equal(v.TLS) {
				return true
			}
		}
	}

	for k, v := range conf.Origins {
		if v.TLS != nil && v.TLS.ServeTLS {
			if o, ok := oldConf.Origins[k]; !ok ||
				o.TLS == nil || !o.TLS.ServeTLS ||
				!o.TLS.Equal(v.TLS) {
				return true
			}
		}
	}

	return false
}
