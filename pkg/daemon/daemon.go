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

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/appinfo/usage"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/daemon/setup"
	"github.com/trickstercache/trickster/v2/pkg/daemon/signaling"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

var mtx sync.Mutex
var wasStarted bool

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

	conf, err := setup.LoadAndValidate()
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
		if conf.Flags != nil && conf.Flags.ValidateConfig {
			fmt.Println("Trickster configuration validation succeeded.")
			return nil
		}
	}

	si := &instance.ServerInstance{
		Config: conf,
	}

	var hupFunc reload.ReloadFunc = func() (bool, error) {
		return Hup(si)
	}

	// Serve with Config
	si.Caches, err = setup.ApplyConfig(si, conf, hupFunc, func() { os.Exit(1) })
	if err != nil {
		return err
	}
	wasStarted = true
	skipUnlock = true
	mtx.Unlock()
	signaling.Wait(hupFunc)
	return nil
}

func Hup(si *instance.ServerInstance) (bool, error) {
	mtx.Lock()
	defer mtx.Unlock()
	conf, err := setup.LoadAndValidate()
	if err != nil {
		return false, err
	}
	if conf == nil || conf.Resources == nil {
		return false, errors.ErrInvalidOptions
	}
	if conf.IsStale() {
		logger.Warn("configuration reload starting now",
			logging.Pairs{"source": "sighup"})

		var hupFunc reload.ReloadFunc = func() (bool, error) {
			return Hup(si)
		}
		_, err = setup.ApplyConfig(si, conf, hupFunc, nil)
		if err != nil {
			logger.Warn(reload.ConfigNotReloadedText,
				logging.Pairs{"error": err.Error()})
			return false, err
		}
		logger.Info(reload.ConfigReloadedText, nil)
		return true, nil
	}
	logger.Warn(reload.ConfigNotReloadedText, nil)
	return false, nil

}
