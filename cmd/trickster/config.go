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

	"github.com/tricksterproxy/trickster/cmd/trickster/config"
	ro "github.com/tricksterproxy/trickster/cmd/trickster/config/reload/options"
	"github.com/tricksterproxy/trickster/pkg/backends/alb"
	"github.com/tricksterproxy/trickster/pkg/backends/healthcheck"
	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/cache/memory"
	"github.com/tricksterproxy/trickster/pkg/cache/providers"
	"github.com/tricksterproxy/trickster/pkg/cache/registration"
	tl "github.com/tricksterproxy/trickster/pkg/observability/logging"
	"github.com/tricksterproxy/trickster/pkg/observability/metrics"
	"github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	th "github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/routing"
	"github.com/tricksterproxy/trickster/pkg/runtime"
	tr "github.com/tricksterproxy/trickster/pkg/tracing/registration"

	"github.com/gorilla/mux"
)

var cfgLock = &sync.Mutex{}
var hc healthcheck.HealthChecker

func runConfig(oldConf *config.Config, wg *sync.WaitGroup, logger *tl.Logger,
	oldCaches map[string]cache.Cache, args []string, errorFunc func()) error {

	metrics.BuildInfo.WithLabelValues(applicationGoVersion,
		applicationGitCommitID, applicationVersion).Set(1)

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
		handleStartupIssue("", nil, nil, errorFunc)
		return err
	}

	// if it's a -version command, print version and exit
	if flags.PrintVersion {
		PrintVersion()
		return nil
	}

	err = validateConfig(conf)
	if err != nil {
		handleStartupIssue("ERROR: Could not load configuration: "+err.Error(),
			nil, nil, errorFunc)
	}
	if flags.ValidateConfig {
		fmt.Println("Trickster configuration validation succeeded.")
		return nil
	}

	return applyConfig(conf, oldConf, wg, logger, oldCaches, args, errorFunc)

}

func applyConfig(conf, oldConf *config.Config, wg *sync.WaitGroup, logger *tl.Logger,
	oldCaches map[string]cache.Cache, args []string, errorFunc func()) error {

	if conf == nil {
		return nil
	}

	if conf.Main.ServerName == "" {
		conf.Main.ServerName, _ = os.Hostname()
	}
	runtime.Server = conf.Main.ServerName

	if conf.ReloadConfig == nil {
		conf.ReloadConfig = ro.New()
	}

	logger = applyLoggingConfig(conf, oldConf, logger)

	for _, w := range conf.LoaderWarnings {
		tl.Warn(logger, w, tl.Pairs{})
	}

	//Register Tracing Configurations
	tracers, err := tr.RegisterAll(conf, logger, false)
	if err != nil {
		handleStartupIssue("tracing registration failed", tl.Pairs{"detail": err.Error()},
			logger, errorFunc)
		return err
	}

	// every config (re)load is a new router
	router := mux.NewRouter()
	router.HandleFunc(conf.Main.PingHandlerPath, th.PingHandleFunc(conf)).Methods(http.MethodGet)

	var caches = applyCachingConfig(conf, oldConf, logger, oldCaches)
	rh := handlers.ReloadHandleFunc(runConfig, conf, wg, logger, caches, args)

	o, err := routing.RegisterProxyRoutes(conf, router, caches, tracers, logger, false)
	if err != nil {
		handleStartupIssue("route registration failed", tl.Pairs{"detail": err.Error()},
			logger, errorFunc)
		return err
	}

	if hc != nil {
		hc.Shutdown()
	}
	hc, err = o.StartHealthChecks(logger)
	if err != nil {
		return err
	}
	alb.StartALBPools(o, hc.Statuses())
	routing.RegisterHealthHandler(router, conf.Main.HealthHandlerPath, hc)

	applyListenerConfigs(conf, oldConf, router, http.HandlerFunc(rh), logger, tracers)

	metrics.LastReloadSuccessfulTimestamp.Set(float64(time.Now().Unix()))
	metrics.LastReloadSuccessful.Set(1)
	// add Config Reload HUP Signal Monitor
	if oldConf != nil && oldConf.Resources != nil {
		oldConf.Resources.QuitChan <- true // this signals the old hup monitor goroutine to exit
	}
	startHupMonitor(conf, wg, logger, caches, args)

	return nil
}

func applyLoggingConfig(c, o *config.Config, oldLog *tl.Logger) *tl.Logger {

	if c == nil || c.Logging == nil {
		return oldLog
	}

	if c.ReloadConfig == nil {
		c.ReloadConfig = ro.New()
	}

	if o != nil && o.Logging != nil {
		if c.Logging.LogFile == o.Logging.LogFile &&
			c.Logging.LogLevel == o.Logging.LogLevel {
			// no changes in logging config,
			// so we keep the old logger intact
			return oldLog
		}
		if c.Logging.LogFile != o.Logging.LogFile {
			if o.Logging.LogFile != "" {
				// if we're changing from file1 -> console or file1 -> file2, close file1 handle
				// the extra 1s allows HTTP listeners to close first and finish their log writes
				go delayedLogCloser(oldLog,
					time.Duration(c.ReloadConfig.DrainTimeoutMS+1000)*time.Millisecond)
			}
			return initLogger(c)
		}
		if c.Logging.LogLevel != o.Logging.LogLevel {
			// the only change is the log level, so update it and return the original logger
			oldLog.SetLogLevel(c.Logging.LogLevel)
			return oldLog
		}
	}

	return initLogger(c)
}

func applyCachingConfig(c, oc *config.Config, logger *tl.Logger,
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
			if ocfg.ProviderID == v.ProviderID &&
				ocfg.ProviderID == providers.Memory {
				if v.Index != nil {
					mc := w.(*memory.Cache)
					mc.Index.UpdateOptions(v.Index)
				}
				caches[k] = w
				continue
			}

			// if we got to this point, the cache won't be used, so lets close it
			go func() {
				time.Sleep(time.Millisecond * time.Duration(c.ReloadConfig.DrainTimeoutMS))
				w.Close()
			}()
		}

		// the newly-named cache is not in the old config or couldn't be reused, so make it anew
		caches[k] = registration.NewCache(k, v, logger)
	}
	return caches
}

func initLogger(c *config.Config) *tl.Logger {
	logger := tl.New(c)
	tl.Info(logger, "application loaded from configuration",
		tl.Pairs{
			"name":      runtime.ApplicationName,
			"version":   runtime.ApplicationVersion,
			"goVersion": applicationGoVersion,
			"goArch":    applicationGoArch,
			"commitID":  applicationGitCommitID,
			"buildTime": applicationBuildTime,
			"logLevel":  c.Logging.LogLevel,
			"config":    c.ConfigFilePath(),
			"pid":       os.Getpid(),
		},
	)
	return logger
}

func delayedLogCloser(logger *tl.Logger, delay time.Duration) {
	// we can't immediately close the logger, because some outstanding
	// http requests might still be on the old reference, so this will
	// allow time for those connections to drain
	if logger == nil {
		return
	}
	time.Sleep(delay)
	logger.Close()
}

func handleStartupIssue(event string, detail tl.Pairs, logger *tl.Logger, errorFunc func()) {
	metrics.LastReloadSuccessful.Set(0)
	if event != "" {
		if logger != nil {
			if errorFunc != nil {
				tl.Error(logger, event, detail)
				errorFunc()
			}
			tl.Warn(logger, event, detail)
			return
		}
		fmt.Println(event)
	}
	if errorFunc != nil {
		errorFunc()
	}
}

func validateConfig(conf *config.Config) error {

	for _, w := range conf.LoaderWarnings {
		fmt.Println(w)
	}

	var caches = make(map[string]cache.Cache)
	for k := range conf.Caches {
		caches[k] = nil
	}

	router := mux.NewRouter()
	logger := tl.ConsoleLogger(conf.Logging.LogLevel)

	tracers, err := tr.RegisterAll(conf, logger, true)
	if err != nil {
		return err
	}

	_, err = routing.RegisterProxyRoutes(conf, router, caches, tracers, logger, true)
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
