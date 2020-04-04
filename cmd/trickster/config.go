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
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
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
)

var cfgLock = &sync.Mutex{}

func runConfig(oldConf *config.Config, wg *sync.WaitGroup,
	log *log.Logger, args []string, errorsFatal bool) {

	cfgLock.Lock()
	defer cfgLock.Unlock()
	var err error

	// load the config
	conf, flags, err := config.Load(runtime.ApplicationName, runtime.ApplicationVersion, args)
	if err != nil {
		fmt.Println("\nERROR: Could not load configuration:", err.Error())
		if flags != nil && !flags.ValidateConfig {
			PrintUsage()
		}
		handleStartupIssue("", nil, nil, errorsFatal)
		return
	}

	// if it's a -version command, print version and exit
	if flags.PrintVersion {
		PrintVersion()
		os.Exit(0)
	}

	err = validateConfig(conf)
	if err != nil {
		handleStartupIssue("ERROR: Could not load configuration: "+err.Error(), nil, nil, errorsFatal)
	}
	if flags.ValidateConfig {
		fmt.Println("Trickster configuration validation succeeded.")
		os.Exit(0)
	}

	applyConfig(conf, oldConf, wg, log, args, errorsFatal)

}

func applyConfig(conf, oldConf *config.Config, wg *sync.WaitGroup,
	log *log.Logger, args []string, errorsFatal bool) {

	log = applyLoggingConfig(conf, oldConf, log)

	for _, w := range conf.LoaderWarnings {
		log.Warn(w, tl.Pairs{})
	}

	// TODO: move to function and  handle differences
	//Register Tracing Configurations
	tracerFlushers, err := tr.RegisterAll(conf, log)
	if err != nil {
		handleStartupIssue("tracing registration failed", tl.Pairs{"detail": err.Error()},
			log, errorsFatal)
		return
	}

	if len(tracerFlushers) > 0 {
		for _, f := range tracerFlushers {
			// TODO: Move elsewhere
			defer f()
		}
	}

	// every config reload is a new router
	router := mux.NewRouter()
	router.HandleFunc(conf.Main.PingHandlerPath, th.PingHandleFunc(conf)).Methods("GET")
	router.HandleFunc(conf.Main.ConfigHandlerPath, th.ConfigHandleFunc(conf)).Methods("GET")
	// TODO: Add Reload Handler w/ Relevant stuff (incl. this func)

	// TODO: Validate if anything about the caches has changed (e.g., are more than one cache using same filename)
	// has the filename moved from and old to a new config - and if so, can we keep that handle.
	var caches = make(map[string]cache.Cache)
	for k, v := range conf.Caches {
		c := registration.NewCache(k, v, log)
		caches[k] = c
	}

	_, err = routing.RegisterProxyRoutes(conf, router, caches, log, false)
	if err != nil {
		handleStartupIssue("route registration failed", tl.Pairs{"detail": err.Error()},
			log, errorsFatal)
		return
	}

	// TODO: Validate if the ports or addresses have changed and adjust
	//
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

func applyLoggingConfig(c, oc *config.Config, oldLog *log.Logger) *log.Logger {

	if c == nil || c.Logging == nil {
		return oldLog
	}

	if oc != nil && oc.Logging != nil {
		if c.Logging.LogFile == oc.Logging.LogFile &&
			c.Logging.LogLevel == oc.Logging.LogLevel {
			// no changes in logging config,
			// so we keep the old logger intact
			return oldLog
		}
		if c.Logging.LogFile != oc.Logging.LogFile {
			if oc.Logging.LogFile != "" {
				// if we're changing from file1 -> console or file1 -> file2, close file1 handle
				go delayedLogCloser(oldLog)
			}
			return initLogger(c)
		}
		if c.Logging.LogLevel != oc.Logging.LogLevel {
			// the only change is the log level, so update it and return the original logger
			oldLog.SetLogLevel(c.Logging.LogLevel)
			return oldLog
		}
	}

	return initLogger(c)
}

func initLogger(c *config.Config) *log.Logger {
	log := tl.New(c)
	log.Info("application loaded from configuration",
		tl.Pairs{
			"name":      runtime.ApplicationName,
			"version":   runtime.ApplicationVersion,
			"goVersion": applicationGoVersion,
			"goArch":    applicationGoArch,
			"commitID":  applicationGitCommitID,
			"buildTime": applicationBuildTime,
			"logLevel":  c.Logging.LogLevel,
			"config":    c.Main.ConfigFilePath,
		},
	)
	return log
}

func delayedLogCloser(log *log.Logger) {
	// we can't immediately close the log, because some outstanding
	// http requests might still be on the old reference, so this will
	// allow time for those connections to bleed off
	if log == nil {
		return
	}
	time.Sleep(time.Second * 30)
	log.Close()
}

func handleStartupIssue(event string, detail log.Pairs, logger *log.Logger, exitFatal bool) {
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

func validateConfig(conf *config.Config) error {

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
