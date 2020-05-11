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
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/config"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
)

var hups = make(chan os.Signal, 1)

func init() {
	signal.Notify(hups, syscall.SIGHUP)
}

func startHupMonitor(conf *config.Config, wg *sync.WaitGroup, log *tl.Logger,
	caches map[string]cache.Cache, args []string) {
	if conf == nil || conf.Resources == nil {
		return
	}
	// assumes all parameters are instantiated
	go func() {
		for {
			select {
			case <-hups:
				if conf.IsStale() {
					log.Warn("configuration reload starting now", tl.Pairs{"source": "sighup"})
					err := runConfig(conf, wg, log, caches, args, false)
					if err == nil {
						return // runConfig will start a new HupMonitor in place of this one
					}
				}
				log.Warn("configuration NOT reloaded", tl.Pairs{})
			case <-conf.Resources.QuitChan:
				return
			}
		}
	}()
}
