/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"net/url"
	"time"
)

// Load returns the Application Configuration, starting with a default config,
// then overriding with any provided config file, then env vars, and finally flags
func Load(applicationName string, applicationVersion string, arguments []string) error {

	ApplicationName = applicationName
	ApplicationVersion = applicationVersion

	c := NewConfig()
	c.parseFlags(applicationName, arguments) // Parse here to get config file path and version flags
	if Flags.PrintVersion {
		return nil
	}
	if err := c.loadFile(); err != nil && Flags.customPath {
		// a user-provided path couldn't be loaded. return the error for the application to handle
		return err
	}

	c.loadEnvVars()
	c.loadFlags() // load parsed flags to override file and envs

	// set the default origin url from the flags
	if defaultOriginURL != "" {

		url, err := url.Parse(defaultOriginURL)
		if err != nil {
			return err
		}

		if d, ok := c.Origins["default"]; ok {

			if defaultOriginType != "" {
				d.Type = defaultOriginType
			}

			d.Scheme = url.Scheme
			d.Host = url.Host
			d.PathPrefix = url.Path
			c.Origins["default"] = d
		}
	}

	Config = c
	Main = &c.Main
	Origins = c.Origins
	Caches = c.Caches
	ProxyServer = &c.ProxyServer
	Logging = &c.Logging
	Metrics = &c.Metrics

	for k, o := range Origins {
		o.Timeout = time.Duration(o.TimeoutSecs) * time.Second
		o.BackfillTolerance = time.Duration(o.BackfillToleranceSecs) * time.Second
		o.ValueRetention = time.Duration(o.ValueRetentionFactor)
		Origins[k] = o
	}

	for k, c := range Caches {
		c.TimeseriesTTL = time.Duration(c.TimeseriesTTLSecs) * time.Second
		c.ObjectTTL = time.Duration(c.ObjectTTLSecs) * time.Second
		c.FastForwardTTL = time.Duration(c.FastForwardTTLSecs) * time.Second

		c.Index.FlushInterval = time.Duration(c.Index.FlushIntervalSecs) * time.Second
		c.Index.ReapInterval = time.Duration(c.Index.ReapIntervalSecs) * time.Second

		Caches[k] = c
	}

	return nil
}
