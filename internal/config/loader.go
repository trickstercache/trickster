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

package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Load returns the Application Configuration, starting with a default config,
// then overriding with any provided config file, then env vars, and finally flags
func Load(applicationName string, applicationVersion string, arguments []string) (*TricksterConfig, *TricksterFlags, error) {

	providedOriginURL = ""
	providedOriginType = ""

	LoaderWarnings = make([]string, 0)

	c := NewConfig()
	flags, err := parseFlags(applicationName, arguments) // Parse here to get config file path and version flags
	if err != nil {
		return nil, nil, err
	}
	if flags.PrintVersion {
		return nil, flags, nil
	}
	if err := c.loadFile(flags); err != nil && flags.customPath {
		// a user-provided path couldn't be loaded. return the error for the application to handle
		return nil, nil, err
	}

	c.loadEnvVars()
	c.loadFlags(flags) // load parsed flags to override file and envs

	// set the default origin url from the flags
	if d, ok := c.Origins["default"]; ok {
		if providedOriginURL != "" {
			url, err := url.Parse(providedOriginURL)
			if err != nil {
				return nil, nil, err
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

		if providedOriginType != "" {
			d.OriginType = providedOriginType
		}
	}

	if len(c.Origins) == 0 {
		return nil, nil, errors.New("no valid origins configured")
	}

	for k, n := range c.NegativeCacheConfigs {
		for c := range n {
			ci, err := strconv.Atoi(c)
			if err != nil {
				return nil, nil, fmt.Errorf(`invalid negative cache config in %s: %s is not a valid status code`, k, c)
			}
			if ci < 400 || ci >= 600 {
				return nil, nil, fmt.Errorf(`invalid negative cache config in %s: %s is not a valid status code`, k, c)
			}
		}
	}

	for k, o := range c.Origins {

		if o.OriginType == "" {
			return nil, nil, fmt.Errorf(`missing origin-type for origin "%s"`, k)
		}

		if o.OriginType == "rule" && o.RuleName == "" {
			return nil, nil, fmt.Errorf(`missing rule-name for origin "%s"`, k)
		}

		if o.OriginType != "rule" && o.OriginURL == "" {
			return nil, nil, fmt.Errorf(`missing origin-url for origin "%s"`, k)
		}

		url, err := url.Parse(o.OriginURL)
		if err != nil {
			return nil, nil, err
		}

		if strings.HasSuffix(url.Path, "/") {
			url.Path = url.Path[0 : len(url.Path)-1]
		}

		o.Name = k
		o.Scheme = url.Scheme
		o.Host = url.Host
		o.PathPrefix = url.Path
		o.Timeout = time.Duration(o.TimeoutSecs) * time.Second
		o.BackfillTolerance = time.Duration(o.BackfillToleranceSecs) * time.Second
		o.TimeseriesRetention = time.Duration(o.TimeseriesRetentionFactor)
		o.TimeseriesTTL = time.Duration(o.TimeseriesTTLSecs) * time.Second
		o.FastForwardTTL = time.Duration(o.FastForwardTTLSecs) * time.Second
		o.MaxTTL = time.Duration(o.MaxTTLSecs) * time.Second

		if o.CompressableTypeList != nil {
			o.CompressableTypes = make(map[string]bool)
			for _, v := range o.CompressableTypeList {
				o.CompressableTypes[v] = true
			}
		}

		if o.CacheKeyPrefix == "" {
			o.CacheKeyPrefix = o.Host
		}

		nc, ok := c.NegativeCacheConfigs[o.NegativeCacheName]
		if !ok {
			return nil, nil, fmt.Errorf(`invalid negative cache name: %s`, o.NegativeCacheName)
		}

		nc2 := map[int]time.Duration{}
		for c, s := range nc {
			ci, _ := strconv.Atoi(c)
			nc2[ci] = time.Duration(s) * time.Second
		}
		o.NegativeCache = nc2

		tc, ok := c.TracingConfigs[o.TracingConfigName]
		if !ok {
			return c, flags, fmt.Errorf(`invalid tracing config name: %s`, o.TracingConfigName)
		}
		o.TracingConfig = tc

		// enforce MaxTTL
		if o.TimeseriesTTLSecs > o.MaxTTLSecs {
			o.TimeseriesTTLSecs = o.MaxTTLSecs
			o.TimeseriesTTL = o.MaxTTL
		}

		// unlikely but why not spend a few nanoseconds to check it at startup
		if o.FastForwardTTLSecs > o.MaxTTLSecs {
			o.FastForwardTTLSecs = o.MaxTTLSecs
			o.FastForwardTTL = o.MaxTTL
		}
	}

	for _, c := range c.Caches {
		c.Index.FlushInterval = time.Duration(c.Index.FlushIntervalSecs) * time.Second
		c.Index.ReapInterval = time.Duration(c.Index.ReapIntervalSecs) * time.Second
	}

	return c, flags, nil
}
