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
	"context"
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
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

var mtx sync.Mutex

// hupDelegate is the function newHupFunc forwards to. Indirected through a
// package var so tests can swap it out without invoking the full reload path.
// Initialized in init() to break the Hup -> newHupFunc -> hupDelegate -> Hup
// initialization cycle.
var hupDelegate func(si *instance.ServerInstance, source string, args ...string) (bool, error)

func init() {
	hupDelegate = Hup
}

// newHupFunc returns a reload.Reloader closed over args, so subsequent reloads
// continue reading from the original -config path. Used for both the initial
// registration and the re-registration performed after a successful reload.
func newHupFunc(si *instance.ServerInstance, args []string) reload.Reloader {
	return func(source string) (bool, error) {
		return hupDelegate(si, source, args...)
	}
}

func Start(ctx context.Context, args ...string) error {
	var skipUnlock bool
	unlock := func() {
		if !skipUnlock {
			mtx.Unlock()
		}
	}
	mtx.Lock()
	defer unlock()
	metrics.BuildInfo.WithLabelValues(goruntime.Version(),
		appinfo.GitCommitID, appinfo.Version).Set(1)

	conf, clients, err := setup.BootstrapConfig(args...)
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

	si := &instance.ServerInstance{
		Listeners: listener.NewGroup(),
	}
	hupFunc := newHupFunc(si, args)
	// Serve with Config
	err = setup.ApplyConfig(si, conf, clients, hupFunc, func() { os.Exit(1) }, si.Listeners)
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

	skipUnlock = true
	mtx.Unlock()
	signaling.Wait(ctx, hupFunc)
	if si.Listeners != nil {
		si.Listeners.DrainAndClose("httpListener", 0)
		si.Listeners.DrainAndClose("tlsListener", 0)
		si.Listeners.DrainAndClose("metricsListener", 0)
		si.Listeners.DrainAndClose("mgmtListener", 0)
	}
	return nil
}

func Hup(si *instance.ServerInstance, source string, args ...string) (bool, error) {
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

	// handleReloadFailure handles common reload failure logging and metrics
	handleReloadFailure := func(message string, err error) (bool, error) {
		logger.Error(message,
			logging.Pairs{"error": err.Error(), "source": source})
		metrics.ReloadFailuresTotal.Inc()
		metrics.LastReloadSuccessful.Set(0)
		metrics.ReloadDurationSeconds.Observe(time.Since(startTime).Seconds())
		return false, err
	}

	newConf, newClients, err := setup.BootstrapConfig(args...)
	if err != nil {
		return handleReloadFailure("reload failed: could not load new config", err)
	}

	if err := validate.Validate(newConf); err != nil {
		return handleReloadFailure("reload failed: new configuration is invalid", err)
	}

	oldConfig := si.Config
	oldClients := si.Backends
	oldCaches := si.Caches
	oldHealthChecker := si.HealthChecker
	oldListeners := si.Listeners

	hupFunc := newHupFunc(si, args)

	err = setup.ApplyConfig(si, newConf, newClients, hupFunc, nil, si.Listeners)
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
		safego.Go(reloadGoroutinePanic("oldListeners.Shutdown", source), func() {
			if err := oldListeners.Shutdown(drainTimeout); err != nil {
				logger.Warn("error shutting down old listeners",
					logging.Pairs{"error": err.Error(), "source": source})
			}
		})
	}

	if oldClients != nil {
		// close idle now, then again after drain so conns released by
		// in-flight requests post-rotation also get reaped before the
		// per-transport IdleConnTimeout (default 2m) elapses.
		oldClients.CloseIdleConnections()
		drainTimeout := 30 * time.Second
		if newConf.MgmtConfig != nil && newConf.MgmtConfig.ReloadDrainTimeout > 0 {
			drainTimeout = newConf.MgmtConfig.ReloadDrainTimeout
		}
		safego.Go(reloadGoroutinePanic("oldClients.CloseIdleConnections", source), func() {
			time.Sleep(drainTimeout)
			oldClients.CloseIdleConnections()
		})
	}

	metrics.ReloadSuccessesTotal.Inc()
	metrics.LastReloadSuccessful.Set(1)
	metrics.LastReloadSuccessfulTimestamp.Set(float64(time.Now().Unix()))
	metrics.ReloadDurationSeconds.Observe(time.Since(startTime).Seconds())

	logger.Info(reload.ConfigReloadedText, logging.Pairs{"source": source})
	return true, nil
}

func reloadGoroutinePanic(site, source string) safego.PanicHandler {
	return func(r any, stack []byte) {
		logger.Error("reload background goroutine panic", logging.Pairs{
			"site":   site,
			"source": source,
			"panic":  r,
			"stack":  string(stack),
		})
	}
}
