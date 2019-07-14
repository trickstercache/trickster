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
	"fmt"
	"net/url"
	"time"
)

// Load returns the Application Configuration, starting with a default config,
// then overriding with any provided config file, then env vars, and finally flags
func Load(applicationName string, applicationVersion string, arguments []string) error {

	providedOriginURL = ""
	providedOriginType = ""

	ApplicationName = applicationName
	ApplicationVersion = applicationVersion
	LoaderWarnings = make([]string, 0, 0)

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
	if d, ok := c.Origins["default"]; ok {
		if providedOriginURL != "" {
			url, err := url.Parse(providedOriginURL)
			if err != nil {
				return err
			}
			if providedOriginType != "" {
				d.OriginType = providedOriginType
			}
			d.OriginURL = providedOriginURL
			d.Scheme = url.Scheme
			d.Host = url.Host
			d.PathPrefix = url.Path
		}
		// If the user has configured their own origins, and one of them is not "default"
		// then Trickster will not use the auto-created default origin
		if d.OriginURL == "" {
			delete(c.Origins, "default")
		}
	}

	if len(c.Origins) == 0 {
		return fmt.Errorf("no valid origins configured%s", "")
	}

	Config = c
	Main = c.Main
	Origins = c.Origins
	Caches = c.Caches
	ProxyServer = c.ProxyServer
	Logging = c.Logging
	Metrics = c.Metrics

	for k, o := range c.Origins {
		if o.OriginURL == "" {
			return fmt.Errorf(`missing origin-url for origin "%s"`, k)
		}

		url, err := url.Parse(o.OriginURL)
		if err != nil {
			return err
		}

		if o.OriginType == "" {
			return fmt.Errorf(`missing origin-type for origin "%s"`, k)
		}

		for _, p := range o.Paths {
			if p.Path != "" {
				o.PathsLookup[p.Path] = p
			}
		}

		o.Name = k
		o.Scheme = url.Scheme
		o.Host = url.Host
		o.PathPrefix = url.Path
		o.Timeout = time.Duration(o.TimeoutSecs) * time.Second
		o.BackfillTolerance = time.Duration(o.BackfillToleranceSecs) * time.Second
		o.ValueRetention = time.Duration(o.ValueRetentionFactor)
		Origins[k] = o
	}

	for _, c := range Caches {
		c.TimeseriesTTL = time.Duration(c.TimeseriesTTLSecs) * time.Second
		c.ObjectTTL = time.Duration(c.ObjectTTLSecs) * time.Second
		c.FastForwardTTL = time.Duration(c.FastForwardTTLSecs) * time.Second
		c.Index.FlushInterval = time.Duration(c.Index.FlushIntervalSecs) * time.Second
		c.Index.ReapInterval = time.Duration(c.Index.ReapIntervalSecs) * time.Second
	}

	return nil
}
