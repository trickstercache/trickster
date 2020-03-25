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

package options

import (
	d "github.com/Comcast/trickster/internal/config/defaults"
)

// Options is a collection of Configurations for storing cached data on the Filesystem
type Options struct {
	// CachePath represents the path on disk where our cache will live
	CachePath string `toml:"cache_path"`
}

func NewOptions() *Options {
	return &Options{CachePath: d.DefaultCachePath}
}
