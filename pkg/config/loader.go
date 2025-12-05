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
	"net/url"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Load returns the Application Configuration, starting with a default config,
// then overriding with any provided config file, then env vars, and finally flags
func Load(args []string) (*Config, error) {
	// this sanitizes the args from -test flags, which can cause issues with unit tests relying on cli args
	sargs := make([]string, 0, len(args))
	for _, v := range args {
		if !strings.HasPrefix(v, "-test.") {
			sargs = append(sargs, v)
		}
	}
	c := NewConfig()
	flags, err := parseFlags(sargs) // Parse here to get config file path and version flags
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.ErrInvalidOptions
	}
	if flags.PrintVersion {
		c.Flags = flags
		return c, nil
	}
	if err := c.loadFile(flags); err != nil && flags.customPath {
		// a user-provided path couldn't be loaded. return the error for the application to handle
		return nil, err
	}

	c.loadEnvVars()
	c.loadFlags(flags) // load parsed flags to override file and envs

	// set the default origin url from the flags
	if d, ok := c.Backends["default"]; ok {
		d.Name = "default"
		if c.providedOriginURL != "" {
			url, err := url.Parse(c.providedOriginURL)
			if err != nil {
				return nil, err
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
		return nil, errors.ErrNoValidBackends
	}

	for k, o := range c.Backends {
		err = o.Initialize(k)
		if err != nil {
			return nil, err
		}
	}

	if c.Frontend != nil {
		if err := c.Frontend.Initialize(); err != nil {
			return nil, err
		}
	}

	if c.Logging != nil {
		if err := c.Logging.Initialize(""); err != nil {
			return nil, err
		}
	}

	if c.Metrics != nil {
		if err := c.Metrics.Initialize(""); err != nil {
			return nil, err
		}
	}

	if len(c.Authenticators) > 0 {
		for _, ao := range c.Authenticators {
			if err := ao.Initialize(); err != nil {
				return nil, err
			}
		}
	}

	if len(c.Caches) > 0 {
		activeCaches := sets.NewStringSet()
		for _, backend := range c.Backends {
			if backend.CacheName != "" {
				activeCaches.Set(backend.CacheName)
			}
		}
		warnings, err := c.Caches.Initialize(activeCaches)
		if err != nil {
			return nil, err
		}
		c.LoaderWarnings = append(c.LoaderWarnings, warnings...)
	}

	return c, nil
}
