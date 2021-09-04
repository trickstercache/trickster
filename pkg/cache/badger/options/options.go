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

package options

import (
	d "github.com/trickstercache/trickster/v2/pkg/cache/options/defaults"
)

// Options is a collection of Configurations for storing cached data on the Filesystem in a Badger key-value store
type Options struct {
	// Directory represents the path on disk where the Badger database should store data
	Directory string `yaml:"directory,omitempty"`
	// ValueDirectory represents the path on disk where the Badger database will store its value log.
	ValueDirectory string `yaml:"value_directory,omitempty"`
}

// New returns a reference to a new Badger Options
func New() *Options {
	return &Options{Directory: d.DefaultCachePath, ValueDirectory: d.DefaultCachePath}
}
