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
	"net/http"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/appinfo/usage"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index"
	"github.com/trickstercache/trickster/v2/pkg/cache/manager"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	dr "github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	te "github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registry"
	ar "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/registry"
	pnh "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ping"
	ph "github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/purge"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/reload"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router/lm"
	"github.com/trickstercache/trickster/v2/pkg/routing"
)

var mtx sync.Mutex

func LoadAndValidate() (*config.Config, error) {
	mtx.Lock()
	defer mtx.Unlock()
	// Load Config
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		logger.Error("Could not load configuration:", logging.Pairs{"error": err.Error()})
		if cfg != nil && cfg.Flags != nil && cfg.Flags.ValidateConfig {
			usage.PrintUsage()
		}
		return nil, err
	}
	if cfg == nil {
		return nil, te.ErrInvalidOptions
	}
	if cfg.Flags != nil && (cfg.Flags.PrintVersion) {
		return cfg, nil
	}

	for _, w := range cfg.LoaderWarnings {
		logger.Warn(w, nil)
	}

	// Validate Config
	if err = validate.Validate(cfg); err != nil {
		logger.Error(err.Error(), nil)
		return nil, err
	}
	return cfg, nil
}

func ApplyConfig(si *instance.ServerInstance, newConf *config.Config,
	hupFunc dr.Reloader, errorFunc func()) error {
	if si == nil || newConf == nil {
		return nil
	}

	if newConf.Main.ServerName == "" {
		newConf.Main.ServerName, _ = os.Hostname()
	}
	appinfo.SetServer(newConf.Main.ServerName)

	if newConf.MgmtConfig == nil {
		newConf.MgmtConfig = mgmt.New()
	}

	if err := buildAuthenticators(newConf); err != nil {
		return err
	}

	applyLoggingConfig(newConf, si.Config)

	// Register Tracing Configurations
	tracers, err := tr.RegisterAll(newConf, false)
	if err != nil {
		handleStartupIssue("tracing registration failed",
			logging.Pairs{"detail": err.Error()},
			errorFunc)
		return err
	}

	// every config (re)load is a new router
	r := lm.NewRouter()
	mr := lm.NewRouter()
	mr.SetMatchingScheme(router.MatchExactPath)

	r.RegisterRoute(newConf.MgmtConfig.PingHandlerPath, nil,
		[]string{http.MethodGet, http.MethodHead}, false,
		http.HandlerFunc((pnh.HandlerFunc(newConf))))

	var caches = applyCachingConfig(si, newConf)
	rh := reload.HandlerFunc(hupFunc)
	backends, err := routing.RegisterProxyRoutes(newConf, r, mr, caches, tracers, false)
	if err != nil {
		handleStartupIssue("route registration failed",
			logging.Pairs{"detail": err.Error()}, errorFunc)
		return err
	}

	if !strings.HasSuffix(newConf.MgmtConfig.PurgeByKeyHandlerPath, "/") {
		newConf.MgmtConfig.PurgeByKeyHandlerPath += "/"
	}
	r.RegisterRoute(newConf.MgmtConfig.PurgeByKeyHandlerPath, nil,
		[]string{http.MethodDelete}, true,
		http.HandlerFunc(ph.KeyHandler(newConf.MgmtConfig.PurgeByKeyHandlerPath, backends)))

	if si.Backends != nil {
		alb.StopPools(si.Backends)
	}
	if si.HealthChecker != nil {
		si.HealthChecker.Shutdown()
	}
	si.HealthChecker, err = backends.StartHealthChecks()
	if err != nil {
		return err
	}
	alb.StartALBPools(backends, si.HealthChecker.Statuses())
	routing.RegisterDefaultBackendRoutes(r, newConf, backends, tracers)
	routing.RegisterHealthHandler(mr, newConf.MgmtConfig.HealthHandlerPath, si.HealthChecker)
	applyListenerConfigs(newConf, si.Config, r, rh, mr, tracers, backends, errorFunc)

	metrics.LastReloadSuccessfulTimestamp.Set(float64(time.Now().Unix()))
	metrics.LastReloadSuccessful.Set(1)
	si.Config = newConf
	si.Caches = caches
	si.Backends = backends
	return nil
}

func buildAuthenticators(c *config.Config) error {
	if c == nil || len(c.Authenticators) == 0 {
		return nil
	}
	for _, ao := range c.Authenticators {
		ac, err := ar.New(ao.Provider, map[string]any{"options": ao})
		if err != nil {
			return err
		}
		ao.Authenticator = ac
	}
	return nil
}

func applyLoggingConfig(c, o *config.Config) {
	oldLogger := logger.Logger()
	if c == nil || c.Logging == nil {
		return
	}
	isReload := o != nil && c != o
	if c.MgmtConfig == nil {
		c.MgmtConfig = mgmt.New()
	}
	if isReload {
		if c.Logging.LogFile == o.Logging.LogFile &&
			c.Logging.LogLevel == o.Logging.LogLevel {
			// no changes in logging config,
			// so we keep the old logger intact
			return
		}
		if c.Logging.LogFile != o.Logging.LogFile {
			if o.Logging.LogFile != "" {
				// if we're changing from file1 -> console or file1 -> file2, close file1 handle
				// the extra 1s allows HTTP listeners to close first and finish their log writes
				go delayedLogCloser(oldLogger, c.MgmtConfig.ReloadDrainTimeout+(1*time.Millisecond))
			}
			initLogger(c)
			return
		}
		if c.Logging.LogLevel != o.Logging.LogLevel {
			// the only change is the log level, so update it and return the original logger
			oldLogger.SetLogLevel(level.Level(c.Logging.LogLevel))
			return
		}
	}
	initLogger(c)
}

func applyCachingConfig(si *instance.ServerInstance,
	newConf *config.Config) cache.Lookup {

	if si == nil || newConf == nil {
		return nil
	}

	caches := make(cache.Lookup)

	if si.Config == nil || si.Caches == nil {
		for k, v := range newConf.Caches {
			caches[k] = registry.NewCache(k, v)
		}
		return caches
	}

	for k, v := range newConf.Caches {

		if w, ok := si.Caches[k]; ok {

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
				v.ProviderID == providers.Memory {
				// Note: this is only necessary for the memory cache as all other providers will be closed and reopened with the newest config
				if v.Index != nil {
					mc := w.(*manager.Manager).Client.(*index.IndexedClient)
					mc.UpdateOptions(v.Index)
				}
				caches[k] = w
				continue
			}

			// if we got to this point, the cache won't be used, so lets close it
			go func() {
				time.Sleep(newConf.MgmtConfig.ReloadDrainTimeout)
				w.Close()
			}()
		}

		// the newly-named cache is not in the old config or couldn't be reused, so make it anew
		caches[k] = registry.NewCache(k, v)
	}
	return caches
}

func initLogger(c *config.Config) logging.Logger {
	logger.SetLogger(logging.New(c))
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
	return logger.Logger()
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

func handleStartupIssue(event string, detail logging.Pairs, errorFunc func()) {
	metrics.LastReloadSuccessful.Set(0)
	if event != "" {
		if errorFunc != nil {
			logger.Error(event, detail)
			errorFunc()
		}
		logger.Warn(event, detail)
		return
	}
	if errorFunc != nil {
		errorFunc()
	}
}
