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
	"flag"
)

const (
	// Command-line flags
	cfConfig      = "config"
	cfVersion     = "version"
	cfValidate    = "validate-config"
	cfLogLevel    = "log-level"
	cfInstanceID  = "instance-id"
	cfOrigin      = "origin-url"
	cfProvider    = "provider"
	cfProxyPort   = "proxy-port"
	cfMetricsPort = "metrics-port"
)

// Flags holds the values for whitelisted flags
type Flags struct {
	PrintVersion      bool
	ValidateConfig    bool
	customPath        bool
	ProxyListenPort   int
	MetricsListenPort int
	InstanceID        int
	ConfigPath        string
	Origin            string
	Provider          string
	LogLevel          string
}

func parseFlags(applicationName string, arguments []string) (*Flags, error) {

	flags := &Flags{}
	flagSet := flag.NewFlagSet("trickster", flag.ContinueOnError)

	flagSet.BoolVar(&flags.PrintVersion, cfVersion, false,
		"Prints the Trickster version")
	flagSet.BoolVar(&flags.ValidateConfig, cfValidate, false,
		"Validates a Trickster config and exits without running the server")
	flagSet.StringVar(&flags.ConfigPath, cfConfig, "",
		"Path to Trickster Config File")
	flagSet.StringVar(&flags.LogLevel, cfLogLevel, "",
		"Level of Logging to use (debug, info, warn, error)")
	flagSet.IntVar(&flags.InstanceID, cfInstanceID, 0,
		"Instance ID is for running multiple Trickster processes"+
			" from the same config while logging to their own files")
	flagSet.StringVar(&flags.Origin, cfOrigin, "",
		"URL to the Origin. Enter it like you would in grafana, e.g., http://prometheus:9090")
	flagSet.StringVar(&flags.Provider, cfProvider, "",
		"Name of the backend provider (prometheus, influxdb, clickhouse, rpc, etc.)")
	flagSet.IntVar(&flags.ProxyListenPort, cfProxyPort, 0,
		"Port that the primary Proxy server will listen on")
	flagSet.IntVar(&flags.MetricsListenPort, cfMetricsPort, 0,
		"Port that the /metrics endpoint will listen on")

	err := flagSet.Parse(arguments)
	if err != nil {
		return nil, err
	}
	if flags.ConfigPath != "" {
		flags.customPath = true
	} else {
		flags.ConfigPath = DefaultConfigPath
	}
	return flags, nil
}

// loadFlags loads configuration from command line flags.
func (c *Config) loadFlags(flags *Flags) {
	if len(flags.Origin) > 0 {
		c.providedOriginURL = flags.Origin
	}
	if len(flags.Provider) > 0 {
		c.providedProvider = flags.Provider
	}
	if flags.ProxyListenPort > 0 {
		c.Frontend.ListenPort = flags.ProxyListenPort
	}
	if flags.MetricsListenPort > 0 {
		c.Metrics.ListenPort = flags.MetricsListenPort
	}
	// if flags.ReloadListenPort > 0 {
	// 	c.Main.Reload.ListenPort = flags.ReloadListenPort
	// }
	if flags.LogLevel != "" {
		c.Logging.LogLevel = flags.LogLevel
	}
	if flags.InstanceID > 0 {
		c.Main.InstanceID = flags.InstanceID
	}
}
