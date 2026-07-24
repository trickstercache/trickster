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

	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Options is a collection of configurations for trickster management features
type Options struct {
	// ListenAddress (DEPRECATED) is IP address from which the Reload API is available at ReloadHandlerPath
	// This is now auto-defined in the 'listeners' with defaults and can be overridden in the yaml config
	ListenAddress string `yaml:"listen_address,omitempty"`
	// ListenPort (DEPRECATED) is TCP Port from which the Reload API is available at ReloadHandlerPath
	// This is now auto-defined in the 'listeners' with defaults and can be overridden in the yaml config
	ListenPort int `yaml:"listen_port,omitempty"`
	//
	// ConfigHandlerPath provides the path to register the Config Handler for outputting the running configuration
	ConfigHandlerPath string `yaml:"config_handler_path,omitempty"`
	// ConfigHandlerListener provides the name of the HTTP listener that will host the config routes
	// Options are: "metrics", "mgmt", "both", or "off"; default is mgmt
	ConfigHandlerListener string `yaml:"config_handler_listener,omitempty"`
	// PingHandlerPath provides the path to register the Ping Handler for checking that Trickster is running
	PingHandlerPath string `yaml:"ping_handler_path,omitempty"`
	// HealthHandlerPath provides the base Health Check Handler path
	HealthHandlerPath string `yaml:"health_handler_path,omitempty"`
	// PurgeByKeyHandlerPath provides the base Cache Purge-by-Key Handler path
	PurgeByKeyHandlerPath string `yaml:"purge_by_key_path,omitempty"`
	// PurgeByKeyHandlerPath provides the base Cache Purge-by-Path Handler path
	PurgeByPathHandlerPath string `yaml:"purge_by_path_path,omitempty"`
	// PprofListener provides the name of the http listener that will host the pprof debugging routes
	// Options are: "metrics", "mgmt", "both", or "off"; default is both
	PprofListener string `yaml:"pprof_listener,omitempty"`
	//
	// ReloadHandlerPath provides the path to register the Config Reload Handler
	ReloadHandlerPath string `yaml:"reload_handler_path,omitempty"`
	// ReloadDrainTimeout provides the duration to wait for all sessions to drain before closing
	// old resources following a reload
	ReloadDrainTimeout time.Duration `yaml:"reload_drain_timeout,omitempty"`
	// ReloadRateLimit limits the # of handled config reload HTTP requests to 1 per CheckRateMS
	// if multiple HTTP requests are received in the rate limit window, only the first is handled
	// This prevents a bad actor from stating the config file with millions of concurrent requests
	// The rate limit does not apply to SIGHUP-based reload requests
	ReloadRateLimit time.Duration `yaml:"reload_rate_limit,omitempty"`
}

// ErrInvalidPprofListenerName returns an error for invalid pprof listener name
var ErrInvalidPprofListenerName = errors.New("invalid pprof listener name")

// ErrInvalidConfigHandlerListenerName returns an error for an invalid config handler listener name
var ErrInvalidConfigHandlerListenerName = errors.New("invalid config handler listener name")

// New returns a new Options references with Default Values set
func New() *Options {
	return &Options{
		ListenPort:             DefaultPort,
		ListenAddress:          DefaultAddress,
		ConfigHandlerPath:      DefaultConfigHandlerPath,
		ConfigHandlerListener:  DefaultConfigHandlerListenerName,
		PingHandlerPath:        DefaultPingHandlerPath,
		HealthHandlerPath:      DefaultHealthHandlerPath,
		PurgeByKeyHandlerPath:  DefaultPurgeByKeyHandlerPath,
		PurgeByPathHandlerPath: DefaultPurgeByPathHandlerPath,
		PprofListener:          DefaultPprofListenerName,
		ReloadHandlerPath:      DefaultReloadHandlerPath,
		ReloadDrainTimeout:     DefaultDrainTimeout,
		ReloadRateLimit:        DefaultRateLimit,
	}
}

func (o *Options) Validate() error {
	switch o.ConfigHandlerListener {
	case ListenerNameMetrics, ListenerNameMgmt, ListenerNameOff, ListenerNameBoth:
	case "":
		o.ConfigHandlerListener = DefaultConfigHandlerListenerName
	default:
		return ErrInvalidConfigHandlerListenerName
	}

	switch o.PprofListener {
	case ListenerNameMetrics, ListenerNameMgmt, ListenerNameOff, ListenerNameBoth:
		return nil
	case "":
		o.PprofListener = DefaultPprofListenerName
		return nil
	}
	return ErrInvalidPprofListenerName
}

func (o *Options) Clone() *Options {
	return pointers.Clone(o)
}
