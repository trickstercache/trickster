/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package main

import (
	"crypto/tls"
	"fmt"
	_ "net/http/pprof" // Comment to disable. Available on :METRICS_PORT/debug/pprof
	"os"
	"sync"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	th "github.com/Comcast/trickster/internal/proxy/handlers"
	rr "github.com/Comcast/trickster/internal/routing/registration"
	"github.com/Comcast/trickster/internal/runtime"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
	tr "github.com/Comcast/trickster/internal/util/tracing/registration"

	"github.com/gorilla/mux"
)

var (
	applicationGitCommitID string
	applicationBuildTime   string
	applicationGoVersion   string
	applicationGoArch      string
)

const (
	applicationName    = "trickster"
	applicationVersion = "1.0.1"
)

// Package main is the main package for the Trickster application
func main() {

	var err error

	runtime.ApplicationName = applicationName
	runtime.ApplicationVersion = applicationVersion

	conf, flags, err := config.Load(runtime.ApplicationName, runtime.ApplicationVersion, os.Args[1:])
	if err != nil {
		fmt.Println("\nERROR: Could not load configuration:", err.Error())
		printUsage()
		os.Exit(1)
	}

	if flags.PrintVersion {
		printVersion()
		os.Exit(0)
	}

	log.Init(conf)
	defer log.Logger.Close()
	log.Info("application start up",
		log.Pairs{
			"name":      runtime.ApplicationName,
			"version":   runtime.ApplicationVersion,
			"goVersion": applicationGoVersion,
			"goArch":    applicationGoArch,
			"commitID":  applicationGitCommitID,
			"buildTime": applicationBuildTime,
			"logLevel":  conf.Logging.LogLevel,
		},
	)

	for _, w := range config.LoaderWarnings {
		log.Warn(w, log.Pairs{})
	}

	// Register Tracing Configurations
	tracerFlushers, err := tr.RegisterAll(conf)
	if err != nil {
		log.Fatal(1, "tracing registration failed", log.Pairs{"detail": err.Error()})
	}

	if len(tracerFlushers) > 0 {
		for _, f := range tracerFlushers {
			defer f()
		}
	}

	router := mux.NewRouter()
	router.HandleFunc(conf.Main.PingHandlerPath, th.PingHandleFunc(conf)).Methods("GET")
	router.HandleFunc(conf.Main.ConfigHandlerPath, th.ConfigHandleFunc(conf)).Methods("GET")

	var logUpstreamRequest bool
	if conf.Logging.LogLevel == "debug" || conf.Logging.LogLevel == "trace" {
		logUpstreamRequest = true
	}

	var caches = make(map[string]cache.Cache)
	for k, v := range conf.Caches {
		c := registration.NewCache(k, v)
		caches[k] = c
	}

	err = rr.RegisterProxyRoutes(conf, router, caches, logUpstreamRequest)
	if err != nil {
		log.Fatal(1, "route registration failed", log.Pairs{"detail": err.Error()})
	}

	if conf.Frontend.TLSListenPort < 1 && conf.Frontend.ListenPort < 1 {
		log.Fatal(1, "no http or https listeners configured", log.Pairs{})
	}

	wg := &sync.WaitGroup{}

	// if TLS port is configured and at least one origin is mapped to a good tls config,
	// then set up the tls server listener instance
	if conf.Frontend.ServeTLS && conf.Frontend.TLSListenPort > 0 {
		var tlsConfig *tls.Config
		tlsConfig, err = conf.TLSCertConfig()
		if err != nil {
			log.Error("unable to start tls listener due to certificate error", log.Pairs{"detail": err})
		} else {
			wg.Add(1)
			go startListener("tlsListener",
				conf.Frontend.TLSListenAddress, conf.Frontend.TLSListenPort,
				conf.Frontend.ConnectionsLimit, tlsConfig, router, wg)
		}
	}

	// if the plaintext HTTP port is configured, then set up the http listener instance
	if conf.Frontend.ListenPort > 0 {
		wg.Add(1)
		go startListener("httpListener",
			conf.Frontend.ListenAddress, conf.Frontend.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, router, wg)
	}

	// if the Metrics HTTP port is configured, then set up the http listener instance
	if conf.Metrics != nil && conf.Metrics.ListenPort > 0 {
		wg.Add(1)
		go startListenerRouter("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, "/metrics", metrics.Handler(), wg)
	}

	wg.Wait()
}
