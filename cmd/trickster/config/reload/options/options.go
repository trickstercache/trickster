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

// Package options provides options for configuration reload support
package options

// Options is a collection of configurations for in-process config reloading
type Options struct {
	// ListenAddress is IP address from which the Reload API is available at ReloadHandlerPath
	ListenAddress string `yaml:"listen_address,omitempty"`
	// ListenPort is TCP Port from which the Reload API is available at ReloadHandlerPath
	ListenPort int `yaml:"listen_port,omitempty"`
	// ReloadHandlerPath provides the path to register the Config Reload Handler
	HandlerPath string `yaml:"handler_path,omitempty"`
	// DrainTimeoutMS provides the duration to wait for all sessions to drain before closing
	// old resources following a reload
	DrainTimeoutMS int `yaml:"drain_timeout_ms,omitempty"`
	// RateLimitMS limits the # of handled config reload HTTP requests to 1 per CheckRateMS
	// if multiple HTTP requests are received in the rate limit window, only the first is handled
	// This prevents a bad actor from stating the config file with millions of concurrent requests
	// The rate limit does not apply to SIGHUP-based reload requests
	RateLimitMS int `yaml:"rate_limit_ms,omitempty"`
}

// New returns a new Options references with Default Values set
func New() *Options {
	return &Options{
		ListenAddress:  DefaultReloadAddress,
		ListenPort:     DefaultReloadPort,
		HandlerPath:    DefaultReloadHandlerPath,
		DrainTimeoutMS: DefaultDrainTimeoutMS,
		RateLimitMS:    DefaultRateLimitMS,
	}
}
