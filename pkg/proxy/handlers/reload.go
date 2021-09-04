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

package handlers

import (
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/cmd/trickster/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// ReloadHandleFunc will reload the running configuration if it has changed
func ReloadHandleFunc(f reload.ReloaderFunc, conf *config.Config, wg *sync.WaitGroup,
	log *tl.Logger, caches map[string]cache.Cache,
	args []string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if conf != nil {
			conf.Main.ReloaderLock.Lock()
			defer conf.Main.ReloaderLock.Unlock()
			if conf.IsStale() {
				tl.Warn(log,
					"configuration reload starting now", tl.Pairs{"source": "reloadEndpoint"})
				err := f(conf, wg, log, caches, args, nil)
				if err == nil {
					w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
					w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
					w.WriteHeader(http.StatusOK)
					w.Write([]byte("configuration reloaded"))
					return
				}
			}
		}
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("configuration NOT reloaded"))
	}
}
