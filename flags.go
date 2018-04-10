package main

/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. * See the License for the specific language governing permissions and
* limitations under the License.
 */

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
)

// loadConfiguration reads the config path from Flags,
// Loads the configs (w/ default values where missing)
// and then evaluates any provided flags as overrides
func loadConfiguration(c *Config, arguments []string) error {
	var path string
	var version bool

	f := flag.NewFlagSet(trickster, -1)
	f.SetOutput(ioutil.Discard)
	f.StringVar(&path, cfConfig, "", "Supplies Path to Config File")
	f.BoolVar(&version, cfVersion, false, "Prints trickster version")
	f.Parse(arguments)

	// If the config file is not specified on the cmdline then try the default
	// location to load the config file.  If the default config does not exist
	// then move on, no big deal.
	if path != "" {
		if err := c.LoadFile(path); err != nil {
			return err
		}
	} else {
		_, err := os.Open(c.Main.ConfigFile)
		if err == nil {
			if err := c.LoadFile(c.Main.ConfigFile); err != nil {
				return err
			}
		}
	}

	// Display version information then exit the program
	if version == true {
		fmt.Println(progversion)
		os.Exit(3)
	}

	// Load from Environment Variables
	loadEnvVars(c)

	//Load from command line flags.
	loadFlags(c, arguments)

	return nil
}

func loadEnvVars(c *Config) {

	// Origin
	if x := os.Getenv(evOrigin); x != "" {
		c.DefaultOriginURL = x
	}

	// Proxy Port
	if x := os.Getenv(evProxyPort); x != "" {
		if y, err := strconv.ParseInt(x, 10, 64); err == nil {
			c.ProxyServer.ListenPort = int(y)
		}
	}

	// Metrics Port
	if x := os.Getenv(evMetricsPort); x != "" {
		if y, err := strconv.ParseInt(x, 10, 64); err == nil {
			c.Metrics.ListenPort = int(y)
		}
	}

	// LogLevel
	if x := os.Getenv(evLogLevel); x != "" {
		c.Logging.LogLevel = x
	}

}

// loadFlags loads configuration from command line flags.
func loadFlags(c *Config, arguments []string) {

	var path string
	var version bool
	var origin string
	var proxyListenPort int
	var metricsListenPort int

	f := flag.NewFlagSet(trickster, flag.ExitOnError)
	f.BoolVar(&version, cfVersion, true, "Prints Trickster version")
	f.StringVar(&c.Logging.LogLevel, cfLogLevel, c.Logging.LogLevel, "Level of Logging to use (debug, info, warn, error)")
	f.IntVar(&c.Main.InstanceID, cfInstanceId, 0, "Instance ID for when running multiple processes")
	f.StringVar(&origin, cfOrigin, "", "URL to the Prometheus Origin. Enter it like you would in grafana, e.g., http://prometheus:9090")
	f.IntVar(&proxyListenPort, cfProxyPort, 0, "Port that the Proxy server will listen on.")
	f.IntVar(&metricsListenPort, cfMetricsPort, 0, "Port that the /metrics endpoint will listen on.")

	// BEGIN IGNORED FLAGS
	f.StringVar(&path, cfConfig, "", "Path to Trickster Config File")
	// END IGNORED FLAGS

	f.Parse(arguments)

	if len(origin) > 0 {
		c.DefaultOriginURL = origin
	}

	if proxyListenPort > 0 {
		c.ProxyServer.ListenPort = proxyListenPort
	}

	if metricsListenPort > 0 {
		c.Metrics.ListenPort = metricsListenPort
	}

}
