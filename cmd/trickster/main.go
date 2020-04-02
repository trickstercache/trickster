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

// Package main is the main package for the Trickster application
package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	_ "net/http/pprof" // Comment to disable. Available on :METRICS_PORT/debug/pprof
	"os"
	"sync"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/registration"
	"github.com/tricksterproxy/trickster/pkg/config"
	th "github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/routing"
	"github.com/tricksterproxy/trickster/pkg/runtime"
	"github.com/tricksterproxy/trickster/pkg/util/log"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/metrics"
	tr "github.com/tricksterproxy/trickster/pkg/util/tracing/registration"

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
	applicationVersion = "1.1.0"
)

var fatalStartupErrors = true

func main() {
	runtime.ApplicationName = applicationName
	runtime.ApplicationVersion = applicationVersion
	wg := &sync.WaitGroup{}
	runTrickster(wg, os.Args[1:], fatalStartupErrors)
	wg.Wait()
}

func runTrickster(wg *sync.WaitGroup, args []string, isStartup bool) {

	var err error

	conf, flags, err := config.Load(runtime.ApplicationName, runtime.ApplicationVersion, args)
	if err != nil {
		fmt.Println("\nERROR: Could not load configuration:", err.Error())
		if flags != nil && !flags.ValidateConfig {
			PrintUsage()
		}
		handleStartupIssue("", nil, nil, isStartup)
		return
	}

	if flags.PrintVersion {
		PrintVersion()
		return
	}

	if flags.ValidateConfig {
		err = validateConfig(conf)
		if err != nil {
			handleStartupIssue("ERROR: Could not load configuration: "+err.Error(), nil, nil, true)
		}
		fmt.Println("Trickster configuration validation succeeded.")
		return
	}

	log := tl.Init(conf)
	defer log.Close()
	log.Info("application start up",
		tl.Pairs{
			"name":      runtime.ApplicationName,
			"version":   runtime.ApplicationVersion,
			"goVersion": applicationGoVersion,
			"goArch":    applicationGoArch,
			"commitID":  applicationGitCommitID,
			"buildTime": applicationBuildTime,
			"logLevel":  conf.Logging.LogLevel,
		},
	)

	for _, w := range conf.LoaderWarnings {
		log.Warn(w, tl.Pairs{})
	}

	// Register Tracing Configurations
	tracerFlushers, err := tr.RegisterAll(conf, log)
	if err != nil {
		handleStartupIssue("tracing registration failed", tl.Pairs{"detail": err.Error()}, log, isStartup)
		return
	}

	if len(tracerFlushers) > 0 {
		for _, f := range tracerFlushers {
			defer f()
		}
	}

	router := mux.NewRouter()
	router.HandleFunc(conf.Main.PingHandlerPath, th.PingHandleFunc(conf)).Methods("GET")
	router.HandleFunc(conf.Main.ConfigHandlerPath, th.ConfigHandleFunc(conf)).Methods("GET")

	var caches = make(map[string]cache.Cache)
	for k, v := range conf.Caches {
		c := registration.NewCache(k, v, log)
		caches[k] = c
	}

	_, err = routing.RegisterProxyRoutes(conf, router, caches, log, false)
	if err != nil {
		handleStartupIssue("route registration failed", tl.Pairs{"detail": err.Error()}, log, isStartup)
		return
	}

	if conf.Frontend.TLSListenPort < 1 && conf.Frontend.ListenPort < 1 {
		handleStartupIssue("no http or https listeners configured", tl.Pairs{}, log, isStartup)
		return
	}

	// if TLS port is configured and at least one origin is mapped to a good tls config,
	// then set up the tls server listener instance
	if conf.Frontend.ServeTLS && conf.Frontend.TLSListenPort > 0 {
		var tlsConfig *tls.Config
		tlsConfig, err = conf.TLSCertConfig()
		if err != nil {
			log.Error("unable to start tls listener due to certificate error", tl.Pairs{"detail": err})
		} else {
			wg.Add(1)
			go startListener("tlsListener",
				conf.Frontend.TLSListenAddress, conf.Frontend.TLSListenPort,
				conf.Frontend.ConnectionsLimit, tlsConfig, router, wg, true, log)
		}
	}

	// if the plaintext HTTP port is configured, then set up the http listener instance
	if conf.Frontend.ListenPort > 0 {
		wg.Add(1)
		go startListener("httpListener",
			conf.Frontend.ListenAddress, conf.Frontend.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, router, wg, true, log)
	}

	// if the Metrics HTTP port is configured, then set up the http listener instance
	if conf.Metrics != nil && conf.Metrics.ListenPort > 0 {
		wg.Add(1)
		go startListenerRouter("metricsListener",
			conf.Metrics.ListenAddress, conf.Metrics.ListenPort,
			conf.Frontend.ConnectionsLimit, nil, "/metrics", metrics.Handler(), wg, true, log)
	}
}

func handleStartupIssue(event string, detail log.Pairs, logger *log.TricksterLogger, exitFatal bool) {
	if event != "" {
		if logger != nil {
			if exitFatal {
				logger.Fatal(1, event, detail)
				return
			}
			logger.Error(event, detail)
			return
		}
		fmt.Println(event)
	}
	if exitFatal {
		os.Exit(1)
	}
}

func validateConfig(conf *config.TricksterConfig) error {

	for _, w := range conf.LoaderWarnings {
		fmt.Println(w)
	}

	// TODO: Tracers w/ Dry Run

	var caches = make(map[string]cache.Cache)
	for k := range conf.Caches {
		caches[k] = nil
	}

	router := mux.NewRouter()
	log := log.ConsoleLogger(conf.Logging.LogLevel)
	_, err := routing.RegisterProxyRoutes(conf, router, caches, log, false)
	if err != nil {
		return err
	}

	if conf.Frontend.TLSListenPort < 1 && conf.Frontend.ListenPort < 1 {
		return errors.New("no http or https listeners configured")
	}

	if conf.Frontend.ServeTLS && conf.Frontend.TLSListenPort > 0 {
		_, err = conf.TLSCertConfig()
		if err != nil {
			return err
		}
	}

	return nil
}
