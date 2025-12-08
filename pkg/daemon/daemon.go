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

// package server runs the Trickster process as an HTTP(S) Listener
// based on the provided configuration
package daemon

import (
	"fmt"
	"os"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/appinfo/usage"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/config/validate"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/daemon/setup"
	"github.com/trickstercache/trickster/v2/pkg/daemon/signaling"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

var (
	mtx        sync.Mutex
	wasStarted bool
)

func Start() error {
	var skipUnlock bool
	unlock := func() {
		if !skipUnlock {
			mtx.Unlock()
		}
	}
	mtx.Lock()
	defer unlock()
	if wasStarted {
		return errors.ErrServerAlreadyStarted
	}
	metrics.BuildInfo.WithLabelValues(goruntime.Version(),
		appinfo.GitCommitID, appinfo.Version).Set(1)

	conf, clients, err := setup.BootstrapConfig()
	if err != nil {
		return err
	}
	if conf == nil {
		return errors.ErrInvalidOptions
	}
	if conf.Flags != nil {
		// if it's a -version command, print version and exit
		if conf.Flags.PrintVersion {
			usage.PrintVersion()
			return nil
		}
		// if it's a -validate command, print validation result
		if conf.Flags.ValidateConfig {
			fmt.Println("Trickster configuration validation succeeded.")
			return nil
		}
	}

	si := &instance.ServerInstance{}
	var hupFunc reload.Reloader = func(source string) (bool, error) {
		return Hup(si, source)
	}
	// Serve with Config
	err = setup.ApplyConfig(si, conf, clients, hupFunc, func() { os.Exit(1) })
	if err != nil {
		return err
	}

	if si.Listeners != nil {
		readinessTimeout := 30 * time.Second
		if conf.MgmtConfig != nil && conf.MgmtConfig.ReloadDrainTimeout > 0 {
			readinessTimeout = conf.MgmtConfig.ReloadDrainTimeout * 2
		}
		if err := si.Listeners.WaitForReady(readinessTimeout); err != nil {
			logger.Warn("startup completed but some listeners not ready",
				logging.Pairs{"error": err.Error()})
		} else {
			logger.Info("all listeners ready", nil)
		}
	}

	wasStarted = true
	skipUnlock = true
	mtx.Unlock()
	signaling.Wait(hupFunc)
	return nil
}

func Hup(si *instance.ServerInstance, source string) (bool, error) {
	mtx.Lock()
	defer mtx.Unlock()

	startTime := time.Now()
	metrics.ReloadAttemptsTotal.Inc()

	if si.Config == nil {
		logger.Warn(reload.ConfigNotReloadedText,
			logging.Pairs{"source": source, "reason": "no existing config to reload"})
		metrics.ReloadFailuresTotal.Inc()
		metrics.LastReloadSuccessful.Set(0)
		return false, nil
	}

	if !si.Config.CheckAndMarkReloadInProgress() {
		logger.Debug("configuration not stale, skipping reload",
			logging.Pairs{"source": source})
		return false, nil
	}

	logger.Warn("configuration reload starting now",
		logging.Pairs{"source": source})

	newConf, newClients, err := setup.BootstrapConfig()
	if err != nil {
		logger.Error("reload failed: could not load new config",
			logging.Pairs{"error": err.Error(), "source": source})
		metrics.ReloadFailuresTotal.Inc()
		metrics.LastReloadSuccessful.Set(0)
		metrics.ReloadDurationSeconds.Observe(time.Since(startTime).Seconds())
		return false, err
	}

	if err := validate.Validate(newConf); err != nil {
		logger.Error("reload failed: new configuration is invalid",
			logging.Pairs{"error": err.Error(), "source": source})
		metrics.ReloadFailuresTotal.Inc()
		metrics.LastReloadSuccessful.Set(0)
		metrics.ReloadDurationSeconds.Observe(time.Since(startTime).Seconds())
		return false, err
	}

	oldConfig := si.Config
	oldClients := si.Backends
	oldCaches := si.Caches
	oldHealthChecker := si.HealthChecker
	oldListeners := si.Listeners

	hupFunc := func(source string) (bool, error) {
		return Hup(si, source)
	}

	err = setup.ApplyConfig(si, newConf, newClients, hupFunc, nil)
	if err != nil {
		logger.Error("reload failed, rolling back to previous configuration",
			logging.Pairs{"error": err.Error(), "source": source})
		si.Config = oldConfig
		si.Backends = oldClients
		si.Caches = oldCaches
		si.HealthChecker = oldHealthChecker
		si.Listeners = oldListeners
		metrics.ReloadFailuresTotal.Inc()
		metrics.LastReloadSuccessful.Set(0)
		metrics.ReloadDurationSeconds.Observe(time.Since(startTime).Seconds())
		return false, err
	}

	if si.Listeners != nil {
		readinessTimeout := 30 * time.Second
		if newConf.MgmtConfig != nil && newConf.MgmtConfig.ReloadDrainTimeout > 0 {
			readinessTimeout = newConf.MgmtConfig.ReloadDrainTimeout * 2
		}
		if err := si.Listeners.WaitForReady(readinessTimeout); err != nil {
			logger.Warn("reload completed but some listeners not ready",
				logging.Pairs{"error": err.Error(), "source": source})
		}
	}

	if oldListeners != nil && oldListeners != si.Listeners {
		drainTimeout := 30 * time.Second
		if newConf.MgmtConfig != nil && newConf.MgmtConfig.ReloadDrainTimeout > 0 {
			drainTimeout = newConf.MgmtConfig.ReloadDrainTimeout
		}
		go func() {
			if err := oldListeners.Shutdown(drainTimeout); err != nil {
				logger.Warn("error shutting down old listeners",
					logging.Pairs{"error": err.Error(), "source": source})
			}
		}()
	}

	metrics.ReloadSuccessesTotal.Inc()
	metrics.LastReloadSuccessful.Set(1)
	metrics.LastReloadSuccessfulTimestamp.Set(float64(time.Now().Unix()))
	metrics.ReloadDurationSeconds.Observe(time.Since(startTime).Seconds())

	logger.Info(reload.ConfigReloadedText, logging.Pairs{"source": source})
	return true, nil
}
