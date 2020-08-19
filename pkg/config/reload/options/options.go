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

// Package options provides options for configuration reload support
package options

import "github.com/tricksterproxy/trickster/pkg/config/defaults"

// Options is a collection of configurations for in-process config reloading
type Options struct {
	// ListenAddress is IP address from which the Reload API is available at ReloadHandlerPath
	ListenAddress string `toml:"listen_address"`
	// ListenPort is TCP Port from which the Reload API is available at ReloadHandlerPath
	ListenPort int `toml:"listen_port"`
	// ReloadHandlerPath provides the path to register the Config Reload Handler
	HandlerPath string `toml:"handler_path"`
	// DrainTimeoutSecs provides the duration to wait for all sessions to drain before closing
	// old resources following a reload
	DrainTimeoutSecs int `toml:"drain_timeout_secs"`
	// RateLimitSecs limits the # of handled config reload HTTP requests to 1 per CheckRateSecs
	// if multiple HTTP requests are received in the rate limit window, only the first is handled
	// This prevents a bad actor from stating the config file with millions of concurrent requets
	// The rate limit does not apply to SIGHUP-based reload requests
	RateLimitSecs int `toml:"rate_limit_secs"`
}

// New returns a new Options references with Default Values set
func New() *Options {
	return &Options{
		ListenAddress:    defaults.DefaultReloadAddress,
		ListenPort:       defaults.DefaultReloadPort,
		HandlerPath:      defaults.DefaultReloadHandlerPath,
		DrainTimeoutSecs: defaults.DefaultDrainTimeoutSecs,
		RateLimitSecs:    defaults.DefaultRateLimitSecs,
	}
}
