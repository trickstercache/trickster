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

package mgmt

import (
	"errors"
	"time"
)

// Options is a collection of configurations for trickster management features
type Options struct {
	// ListenAddress is IP address from which the Reload API is available at ReloadHandlerPath
	ListenAddress string `yaml:"listen_address,omitempty"`
	// ListenPort is TCP Port from which the Reload API is available at ReloadHandlerPath
	ListenPort int `yaml:"listen_port,omitempty"`
	// ConfigHandlerPath provides the path to register the Config Handler for outputting the running configuration
	ConfigHandlerPath string `yaml:"config_handler_path,omitempty"`
	// PingHandlerPath provides the path to register the Ping Handler for checking that Trickster is running
	PingHandlerPath string `yaml:"ping_handler_path,omitempty"`
	// HealthHandlerPath provides the base Health Check Handler path
	HealthHandlerPath string `yaml:"health_handler_path,omitempty"`
	// PurgeByKeyHandlerPath provides the base Cache Purge-by-Key Handler path
	PurgeByKeyHandlerPath string `yaml:"purge_by_key_path,omitempty"`
	// PurgeByKeyHandlerPath provides the base Cache Purge-by-Path Handler path
	PurgeByPathHandlerPath string `yaml:"purge_by_path_path,omitempty"`
	// PprofServer provides the name of the http listener that will host the pprof debugging routes
	// Options are: "metrics", "mgmt", "both", or "off"; default is both
	PprofServer string `yaml:"pprof_server,omitempty"`
	//
	// ReloadHandlerPath provides the path to register the Config Reload Handler
	ReloadHandlerPath string `yaml:"handler_path,omitempty"`
	// ReloadDrainTimeout provides the duration to wait for all sessions to drain before closing
	// old resources following a reload
	ReloadDrainTimeout time.Duration `yaml:"drain_timeout,omitempty"`
	// ReloadRateLimit limits the # of handled config reload HTTP requests to 1 per CheckRateMS
	// if multiple HTTP requests are received in the rate limit window, only the first is handled
	// This prevents a bad actor from stating the config file with millions of concurrent requests
	// The rate limit does not apply to SIGHUP-based reload requests
	ReloadRateLimit time.Duration `yaml:"rate_limit,omitempty"`
}

// ErrInvalidPprofServerName returns an error for invalid pprof server name
var ErrInvalidPprofServerName = errors.New("invalid pprof server name")

// New returns a new Options references with Default Values set
func New() *Options {
	return &Options{
		ListenPort:             DefaultPort,
		ListenAddress:          DefaultAddress,
		ConfigHandlerPath:      DefaultConfigHandlerPath,
		PingHandlerPath:        DefaultPingHandlerPath,
		HealthHandlerPath:      DefaultHealthHandlerPath,
		PurgeByKeyHandlerPath:  DefaultPurgeByKeyHandlerPath,
		PurgeByPathHandlerPath: DefaultPurgeByPathHandlerPath,
		PprofServer:            DefaultPprofServerName,
		ReloadHandlerPath:      DefaultReloadHandlerPath,
		ReloadDrainTimeout:     DefaultDrainTimeout,
		ReloadRateLimit:        DefaultRateLimit,
	}
}

func (o *Options) Validate() error {
	switch o.PprofServer {
	case "metrics", "management", "off", "both":
		return nil
	case "":
		o.PprofServer = DefaultPprofServerName
		return nil
	}
	return ErrInvalidPprofServerName
}

func (o *Options) Clone() *Options {
	return &Options{
		ListenAddress:          o.ListenAddress,
		ListenPort:             o.ListenPort,
		ConfigHandlerPath:      o.ConfigHandlerPath,
		PingHandlerPath:        o.PingHandlerPath,
		HealthHandlerPath:      o.HealthHandlerPath,
		PurgeByKeyHandlerPath:  o.PurgeByKeyHandlerPath,
		PurgeByPathHandlerPath: o.PurgeByPathHandlerPath,
		PprofServer:            o.PprofServer,
	}
}
