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
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/memory"
	"github.com/tricksterproxy/trickster/pkg/cache/registration"
	"github.com/tricksterproxy/trickster/pkg/cache/types"
	"github.com/tricksterproxy/trickster/pkg/config"
	ro "github.com/tricksterproxy/trickster/pkg/config/reload/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	th "github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/routing"
	"github.com/tricksterproxy/trickster/pkg/runtime"
	"github.com/tricksterproxy/trickster/pkg/util/log"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
	"github.com/tricksterproxy/trickster/pkg/util/metrics"
	tr "github.com/tricksterproxy/trickster/pkg/util/tracing/registration"
)

var cfgLock = &sync.Mutex{}

func runConfig(oldConf *config.Config, wg *sync.WaitGroup, log *log.Logger,
	oldCaches map[string]cache.Cache, args []string, errorsFatal bool) {

	metrics.BuildInfo.WithLabelValues(applicationGoVersion, applicationGitCommitID, applicationVersion).Set(1)

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

	applyConfig(conf, oldConf, wg, log, oldCaches, args, errorsFatal)

}

func applyConfig(conf, oldConf *config.Config, wg *sync.WaitGroup, log *log.Logger,
	oldCaches map[string]cache.Cache, args []string, errorsFatal bool) {

	if conf == nil {
		return
	}

	if conf.ReloadConfig == nil {
		conf.ReloadConfig = ro.NewOptions()
	}

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
			// TODO: Move elsewhere, these will close the tracer flushers prematurely here
			defer f()
		}
	}

	// every config (re)load is a new router
	router := mux.NewRouter()
	router.HandleFunc(conf.Main.PingHandlerPath, th.PingHandleFunc(conf)).Methods(http.MethodGet)
	router.HandleFunc(conf.Main.ConfigHandlerPath, th.ConfigHandleFunc(conf)).Methods(http.MethodGet)

	var caches = applyCachingConfig(conf, oldConf, log, oldCaches)
	rh := handlers.ReloadHandleFunc(runConfig, conf, wg, log, caches, args)

	_, err = routing.RegisterProxyRoutes(conf, router, caches, log, false)
	if err != nil {
		handleStartupIssue("route registration failed", tl.Pairs{"detail": err.Error()},
			log, errorsFatal)
		return
	}

	applyListenerConfigs(conf, oldConf, router, http.HandlerFunc(rh), log)

	metrics.LastReloadSuccessfulTimestamp.Set(float64(time.Now().Unix()))
	metrics.LastReloadSuccessful.Set(1)
	// add Config Reload HUP Signal Monitor
	if oldConf != nil {
		oldConf.QuitChan <- true // this signals the old hup monitor goroutine to exit
	}
	startHupMonitor(conf, wg, log, caches, args)
}

func applyLoggingConfig(c, oc *config.Config, oldLog *log.Logger) *log.Logger {

	if c == nil || c.Logging == nil {
		return oldLog
	}

	if c.ReloadConfig == nil {
		c.ReloadConfig = ro.NewOptions()
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
				// the extra 1s allows HTTP listeners to close first and finish their log writes
				go delayedLogCloser(oldLog,
					time.Duration(c.ReloadConfig.BleedTimeoutSecs+1)*time.Second)
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

func applyCachingConfig(c, oc *config.Config, logger *log.Logger,
	oldCaches map[string]cache.Cache) map[string]cache.Cache {

	if c == nil {
		return nil
	}

	caches := make(map[string]cache.Cache)

	if oc == nil || oldCaches == nil {
		for k, v := range c.Caches {
			caches[k] = registration.NewCache(k, v, logger)
		}
		return caches
	}

	for k, v := range c.Caches {

		if w, ok := oldCaches[k]; ok {

			ocfg := w.Configuration()

			// if a cache is in both the old and new config, and unchanged, pass the
			// pre-existing object instead of making a new one
			if v.Equal(ocfg) {
				caches[k] = w
				continue
			}

			// if the new and old caches with the same name are the same type, then assume
			// the cache should be preserved between reconfigurations, but only if the Index
			// is the only change. In this case, we'll apply the new index configuration,
			// then add the old cache with the new index config to the new cache map
			if ocfg.CacheTypeID == v.CacheTypeID &&
				ocfg.CacheTypeID == types.CacheTypeMemory {
				if v.Index != nil {
					mc := w.(*memory.Cache)
					mc.Index.UpdateOptions(v.Index)
				}
				caches[k] = w
				continue
			}
		}

		// the newly-named cache is not in the old config or couldn't be reused, so make it anew
		caches[k] = registration.NewCache(k, v, logger)
	}
	return caches
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
			"config":    c.ConfigFilePath(),
		},
	)
	return log
}

func delayedLogCloser(log *log.Logger, delay time.Duration) {
	// we can't immediately close the log, because some outstanding
	// http requests might still be on the old reference, so this will
	// allow time for those connections to bleed off
	if log == nil {
		return
	}
	time.Sleep(delay)
	log.Close()
}

func handleStartupIssue(event string, detail log.Pairs, logger *log.Logger, exitFatal bool) {
	metrics.LastReloadSuccessful.Set(0)
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
