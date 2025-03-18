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

package signal

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

var hups = make(chan os.Signal, 1)

func init() {
	signal.Notify(hups, syscall.SIGHUP)
}

type ServeFunc = func(*config.Config, *sync.WaitGroup, logging.Logger,
	map[string]cache.Cache, []string, func()) error

func StartHupMonitor(conf *config.Config, wg *sync.WaitGroup, logger logging.Logger,
	caches map[string]cache.Cache, args []string, f ServeFunc) {
	if conf == nil || conf.Resources == nil || f == nil {
		return
	}
	// assumes all parameters are instantiated
	go func() {
		for {
			select {
			case <-hups:
				conf.Main.ReloaderLock.Lock()
				if conf.IsStale() {
					logger.Warn("configuration reload starting now",
						logging.Pairs{"source": "sighup"})
					err := f(conf, wg, logger, caches, args, nil)
					if err == nil {
						conf.Main.ReloaderLock.Unlock()
						return // runConfig will start a new HupMonitor in place of this one
					}
				}
				conf.Main.ReloaderLock.Unlock()
				logger.Warn("configuration NOT reloaded", nil)
			case <-conf.Resources.QuitChan:
				return
			}
		}
	}()
}
