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

package config

import (
	"errors"
	"net/url"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
)

// Load returns the Application Configuration, starting with a default config,
// then overriding with any provided config file, then env vars, and finally flags
func Load(applicationName string, applicationVersion string, arguments []string) (*Config, *Flags, error) {

	c := NewConfig()
	flags, err := parseFlags(applicationName, arguments) // Parse here to get config file path and version flags
	if err != nil {
		return nil, flags, err
	}
	if flags.PrintVersion {
		return nil, flags, nil
	}
	if err := c.loadFile(flags); err != nil && flags.customPath {
		// a user-provided path couldn't be loaded. return the error for the application to handle
		return nil, flags, err
	}

	c.loadEnvVars()
	c.loadFlags(flags) // load parsed flags to override file and envs

	// set the default origin url from the flags
	if d, ok := c.Backends["default"]; ok {
		if c.providedOriginURL != "" {
			url, err := url.Parse(c.providedOriginURL)
			if err != nil {
				return nil, flags, err
			}
			if c.providedProvider != "" {
				d.Provider = c.providedProvider
			}
			d.OriginURL = c.providedOriginURL
			d.Scheme = url.Scheme
			d.Host = url.Host
			d.PathPrefix = url.Path
		}
		// If the user has configured their own backends, and one of them is not "default"
		// then Trickster will not use the auto-created default backend
		if d.OriginURL == "" {
			delete(c.Backends, "default")
		}

		if c.providedProvider != "" {
			d.Provider = c.providedProvider
		}
	}

	if len(c.Backends) == 0 {
		return nil, flags, errors.New("no valid backends configured")
	}

	ncl, err := negative.ConfigLookup(c.NegativeCacheConfigs).Validate()
	if err != nil {
		return nil, flags, err
	}

	err = bo.Lookup(c.Backends).Validate(ncl)
	if err != nil {
		return nil, flags, err
	}

	for _, c := range c.Caches {
		c.Index.FlushInterval = time.Duration(c.Index.FlushIntervalMS) * time.Millisecond
		c.Index.ReapInterval = time.Duration(c.Index.ReapIntervalMS) * time.Millisecond
	}

	return c, flags, nil
}
