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
	"errors"
	"fmt"
	"net/http"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/usage"
	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/memory"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/registration"
	"github.com/trickstercache/trickster/v2/pkg/config"
	ro "github.com/trickstercache/trickster/v2/pkg/config/reload/options"
	"github.com/trickstercache/trickster/v2/pkg/httpserver/signal"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registration"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers"
	"github.com/trickstercache/trickster/v2/pkg/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

var cfgLock = &sync.Mutex{}
var hc healthcheck.HealthChecker

var _ signal.ServeFunc = Serve

func Serve(oldConf *config.Config, wg *sync.WaitGroup, logger logging.Logger,
	oldCaches map[string]cache.Cache, args []string, errorFunc func()) error {

	metrics.BuildInfo.WithLabelValues(goruntime.Version(),
		appinfo.GitCommitID, appinfo.Version).Set(1)

	cfgLock.Lock()
	defer cfgLock.Unlock()
	var err error

	sargs := make([]string, 0, len(args))
	// this sanitizes the args from -test flags, which can cause issues with unit tests relying on cli args
	for _, v := range args {
		if !strings.HasPrefix(v, "-test.") {
			sargs = append(sargs, v)
		}
	}

	// load the config
	conf, flags, err := config.Load(appinfo.Name, appinfo.Version, sargs)
	if err != nil {
		fmt.Println("\nERROR: Could not load configuration:", err.Error())
		if flags != nil && !flags.ValidateConfig {
			usage.PrintUsage()
		}
		handleStartupIssue("", nil, nil, errorFunc)
		return err
	}

	// if it's a -version command, print version and exit
	if flags.PrintVersion {
		usage.PrintVersion()
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

func applyConfig(conf, oldConf *config.Config, wg *sync.WaitGroup, logger logging.Logger,
	oldCaches map[string]cache.Cache, args []string, errorFunc func()) error {

	if conf == nil {
		return nil
	}

	if conf.Main.ServerName == "" {
		conf.Main.ServerName, _ = os.Hostname()
	}
	appinfo.SetServer(conf.Main.ServerName)

	if conf.ReloadConfig == nil {
		conf.ReloadConfig = ro.New()
	}

	logger = applyLoggingConfig(conf, oldConf, logger)

	for _, w := range conf.LoaderWarnings {
		logger.Warn(w, nil)
	}

	//Register Tracing Configurations
	tracers, err := tr.RegisterAll(conf, logger, false)
	if err != nil {
		handleStartupIssue("tracing registration failed", logging.Pairs{"detail": err.Error()},
			logger, errorFunc)
		return err
	}

	// every config (re)load is a new router
	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(0) // metrics router is exact-match only

	r.RegisterRoute(conf.Main.PingHandlerPath, nil,
		[]string{http.MethodGet, http.MethodHead}, false,
		http.HandlerFunc((handlers.PingHandleFunc(conf))))

	var caches = applyCachingConfig(conf, oldConf, logger, oldCaches)
	rh := handlers.ReloadHandleFunc(Serve, conf, wg, logger, caches, args)

	o, err := routing.RegisterProxyRoutes(conf, r, mr, caches, tracers, logger, false)
	if err != nil {
		handleStartupIssue("route registration failed", logging.Pairs{"detail": err.Error()},
			logger, errorFunc)
		return err
	}

	if !strings.HasSuffix(conf.Main.PurgeKeyHandlerPath, "/") {
		conf.Main.PurgeKeyHandlerPath += "/"
	}
	r.RegisterRoute(conf.Main.PurgeKeyHandlerPath, nil,
		[]string{http.MethodDelete}, true,
		http.HandlerFunc(handlers.PurgeKeyHandleFunc(conf, o)))

	if hc != nil {
		hc.Shutdown()
	}
	hc, err = o.StartHealthChecks(logger)
	if err != nil {
		return err
	}
	alb.StartALBPools(o, hc.Statuses())
	routing.RegisterDefaultBackendRoutes(r, o, logger, tracers)
	routing.RegisterHealthHandler(mr, conf.Main.HealthHandlerPath, hc)
	applyListenerConfigs(conf, oldConf, r, http.HandlerFunc(rh), mr, logger, tracers, o, wg, errorFunc)

	metrics.LastReloadSuccessfulTimestamp.Set(float64(time.Now().Unix()))
	metrics.LastReloadSuccessful.Set(1)
	// add Config Reload HUP Signal Monitor
	if oldConf != nil && oldConf.Resources != nil {
		oldConf.Resources.QuitChan <- true // this signals the old hup monitor goroutine to exit
	}
	signal.StartHupMonitor(conf, wg, logger, caches, args, Serve)

	return nil
}

func applyLoggingConfig(c, o *config.Config, oldLog logging.Logger) logging.Logger {

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
			oldLog.SetLogLevel(level.Level(c.Logging.LogLevel))
			return oldLog
		}
	}

	return initLogger(c)
}

func applyCachingConfig(c, oc *config.Config, logger logging.Logger,
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

func initLogger(c *config.Config) logging.Logger {
	logger := logging.New(c)
	logger.Info("application loaded from configuration",
		logging.Pairs{
			"name":      appinfo.Name,
			"version":   appinfo.Version,
			"goVersion": goruntime.Version(),
			"goArch":    goruntime.GOARCH,
			"goOS":      goruntime.GOOS,
			"commitID":  appinfo.GitCommitID,
			"buildTime": appinfo.BuildTime,
			"logLevel":  c.Logging.LogLevel,
			"config":    c.ConfigFilePath(),
			"pid":       os.Getpid(),
		},
	)
	return logger
}

func delayedLogCloser(logger logging.Logger, delay time.Duration) {
	// we can't immediately close the logger, because some outstanding
	// http requests might still be on the old reference, so this will
	// allow time for those connections to drain
	if logger == nil {
		return
	}
	time.Sleep(delay)
	logger.Close()
}

func handleStartupIssue(event string, detail logging.Pairs, logger logging.Logger, errorFunc func()) {
	metrics.LastReloadSuccessful.Set(0)
	if event != "" {
		if logger != nil {
			if errorFunc != nil {
				logger.Error(event, detail)
				errorFunc()
			}
			logger.Warn(event, detail)
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

	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(0) // metrics router is exact-match only
	logger := logging.ConsoleLogger(level.Info)

	tracers, err := tr.RegisterAll(conf, logger, true)
	if err != nil {
		return err
	}

	_, err = routing.RegisterProxyRoutes(conf, r, mr, caches, tracers, logger, true)
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
