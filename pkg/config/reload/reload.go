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

// Package reload helps with reloading the running Trickster configuration
package reload

import (
	"sync"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/util/log"
)

// ReloaderFunc describes a function that loads and applies a Trickster config at startup,
// or gracefully over an existing running Config
type ReloaderFunc func(oldConf *config.Config, wg *sync.WaitGroup, log *log.Logger,
	caches map[string]cache.Cache, args []string, errorsFatal bool)
