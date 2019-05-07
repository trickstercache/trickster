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
	"flag"
)

const (
	// Command-line flags
	cfConfig      = "config"
	cfVersion     = "version"
	cfLogLevel    = "log-level"
	cfInstanceID  = "instance-id"
	cfOrigin      = "origin"
	cfOriginType  = "origin-type"
	cfProxyPort   = "proxy-port"
	cfMetricsPort = "metrics-port"

	// DefaultConfigPath defines the default location of the Trickster config file
	DefaultConfigPath = "/etc/trickster/trickster.conf"
)

// TricksterFlags holds the values for whitelisted flags
type TricksterFlags struct {
	PrintVersion      bool
	ConfigPath        string
	customPath        bool
	Origin            string
	OriginType        string
	ProxyListenPort   int
	MetricsListenPort int
	LogLevel          string
	InstanceID        int
}

// loadFlags loads configuration from command line flags.
func (c *TricksterConfig) parseFlags(applicationName string, arguments []string) {

	Flags = TricksterFlags{}

	f := flag.NewFlagSet(applicationName, flag.ExitOnError)
	f.BoolVar(&Flags.PrintVersion, cfVersion, false, "Prints trickster version")
	f.StringVar(&Flags.ConfigPath, cfConfig, "", "Path to Trickster Config File")
	f.StringVar(&Flags.LogLevel, cfLogLevel, "", "Level of Logging to use (debug, info, warn, error)")
	f.IntVar(&Flags.InstanceID, cfInstanceID, 0, "Instance ID is for running multiple Trickster processes from the same config while logging to their own files.")
	f.StringVar(&Flags.Origin, cfOrigin, "", "URL to the Origin. Enter it like you would in grafana, e.g., http://prometheus:9090")
	f.StringVar(&Flags.OriginType, cfOriginType, "", "Type of origin (prometheus, influxdb)")
	f.IntVar(&Flags.ProxyListenPort, cfProxyPort, 0, "Port that the primary Proxy server will listen on.")
	f.IntVar(&Flags.MetricsListenPort, cfMetricsPort, 0, "Port that the /metrics endpoint will listen on.")
	f.Parse(arguments)

	if Flags.ConfigPath != "" {
		Flags.customPath = true
	} else {
		Flags.ConfigPath = DefaultConfigPath
	}
}

func (c *TricksterConfig) loadFlags() {
	if len(Flags.Origin) > 0 {
		defaultOriginURL = Flags.Origin
	}
	if len(Flags.OriginType) > 0 {
		defaultOriginType = Flags.OriginType
	}
	if Flags.ProxyListenPort > 0 {
		c.ProxyServer.ListenPort = Flags.ProxyListenPort
	}
	if Flags.MetricsListenPort > 0 {
		c.Metrics.ListenPort = Flags.MetricsListenPort
	}
	if Flags.LogLevel != "" {
		c.Logging.LogLevel = Flags.LogLevel
	}
	if Flags.InstanceID > 0 {
		c.Main.InstanceID = Flags.InstanceID
	}

}
