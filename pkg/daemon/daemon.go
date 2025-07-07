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

var mtx sync.Mutex
var wasStarted bool

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

	conf, err := setup.LoadAndValidate(args...)
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
	err = conf.Process()
	if err != nil {
		return err
	}

	// these can't be done until the config is processed
	err = validate.RoutesRulesAndPools(conf)
	if err != nil {
		return err
	}

	si := &instance.ServerInstance{}
	var hupFunc reload.Reloader = func(source string) (bool, error) {
		return Hup(si, source, args...)
	}
	// Serve with Config
	err = setup.ApplyConfig(si, conf, hupFunc, func() { os.Exit(1) })
	if err != nil {
		return err
	}
	skipUnlock = true
	mtx.Unlock()
	signaling.Wait(ctx, hupFunc)
	return nil
}

func Hup(si *instance.ServerInstance, source string, args ...string) (bool, error) {
	mtx.Lock()
	defer mtx.Unlock()
	if si.Config != nil && si.Config.IsStale() {
		conf, err := setup.LoadAndValidate(args...)
		if err != nil {
			return false, err
		}
		if conf == nil || conf.Resources == nil {
			return false, errors.ErrInvalidOptions
		}
		logger.Warn("configuration reload starting now",
			logging.Pairs{"source": source})
		err = conf.Process()
		if err != nil {
			return false, err
		}
		// these can't be done until the config is processed
		err = validate.RoutesRulesAndPools(conf)
		if err != nil {
			return false, err
		}
		var hupFunc reload.Reloader = func(source string) (bool, error) {
			return Hup(si, source, args...)
		}
		err = setup.ApplyConfig(si, conf, hupFunc, nil)
		if err != nil {
			logger.Warn(reload.ConfigNotReloadedText,
				logging.Pairs{"error": err.Error(), "source": source})
			return false, err
		}
		logger.Info(reload.ConfigReloadedText, logging.Pairs{"source": source})
		return true, nil
	}
	logger.Warn(reload.ConfigNotReloadedText, logging.Pairs{"source": source})
	return false, nil
}
