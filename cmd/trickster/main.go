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

package main

import (
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof" // Comment to disable. Available on :METRICS_PORT/debug/pprof
	"os"
	"sync"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	th "github.com/Comcast/trickster/internal/proxy/handlers"
	"github.com/Comcast/trickster/internal/routing"
	rr "github.com/Comcast/trickster/internal/routing/registration"
	"github.com/Comcast/trickster/internal/runtime"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
	tr "github.com/Comcast/trickster/internal/util/tracing/registration"

	"github.com/gorilla/handlers"
)

var (
	applicationGitCommitID string
	applicationBuildTime   string
	applicationGoVersion   string
	applicationGoArch      string
)

const (
	applicationName    = "trickster"
	applicationVersion = "1.0.2"
)

// Package main is the main package for the Trickster application
func main() {

	var err error

	runtime.ApplicationName = applicationName
	runtime.ApplicationVersion = applicationVersion

	err = config.Load(runtime.ApplicationName, runtime.ApplicationVersion, os.Args[1:])
	if err != nil {
		fmt.Println("\nERROR: Could not load configuration:", err.Error())
		printUsage()
		os.Exit(1)
	}

	if config.Flags.PrintVersion {
		printVersion()
		os.Exit(0)
	}

	log.Init()
	defer log.Logger.Close()
	log.Info("application start up",
		log.Pairs{
			"name":      runtime.ApplicationName,
			"version":   runtime.ApplicationVersion,
			"goVersion": applicationGoVersion,
			"goArch":    applicationGoArch,
			"commitID":  applicationGitCommitID,
			"buildTime": applicationBuildTime,
			"logLevel":  config.Logging.LogLevel,
		},
	)

	for _, w := range config.LoaderWarnings {
		log.Warn(w, log.Pairs{})
	}

	metrics.Init()

	// Register Tracing Configurations
	tracerFlushers, err := tr.RegisterAll(config.Config)
	if err != nil {
		log.Fatal(1, "tracing registration failed", log.Pairs{"detail": err.Error()})
	}

	if len(tracerFlushers) > 0 {
		for _, f := range tracerFlushers {
			defer f()
		}
	}

	cr.LoadCachesFromConfig()
	th.RegisterPingHandler()
	th.RegisterConfigHandler()
	err = rr.RegisterProxyRoutes()
	if err != nil {
		log.Fatal(1, "route registration failed", log.Pairs{"detail": err.Error()})
	}

	if config.Frontend.TLSListenPort < 1 && config.Frontend.ListenPort < 1 {
		log.Fatal(1, "no http or https listeners configured", log.Pairs{})
	}

	wg := sync.WaitGroup{}
	var l net.Listener

	// if TLS port is configured and at least one origin is mapped to a good tls config,
	// then set up the tls server listener instance
	if config.Frontend.ServeTLS && config.Frontend.TLSListenPort > 0 {
		wg.Add(1)
		go func() {
			tlsConfig, err := config.Config.TLSCertConfig()
			if err == nil {
				l, err = proxy.NewListener(
					config.Frontend.TLSListenAddress,
					config.Frontend.TLSListenPort,
					config.Frontend.ConnectionsLimit,
					tlsConfig)
				if err == nil {
					log.Info("tls listener starting", log.Pairs{"tlsPort": config.Frontend.TLSListenPort, "tlsListenAddress": config.Frontend.TLSListenAddress})
					err = http.Serve(l, handlers.CompressHandler(routing.TLSRouter))
				}
			}
			log.Error("exiting", log.Pairs{"err": err})
			wg.Done()
		}()
	}

	// if the plaintext HTTP port is configured, then set up the http listener instance
	if config.Frontend.ListenPort > 0 {
		wg.Add(1)
		go func() {
			l, err := proxy.NewListener(config.Frontend.ListenAddress, config.Frontend.ListenPort,
				config.Frontend.ConnectionsLimit, nil)

			if err == nil {
				log.Info("http listener starting", log.Pairs{"httpPort": config.Frontend.ListenPort, "httpListenAddress": config.Frontend.ListenAddress})
				err = http.Serve(l, handlers.CompressHandler(routing.Router))
			}
			log.Error("exiting", log.Pairs{"err": err})
			wg.Done()
		}()
	}

	wg.Wait()
}

func printVersion() {
	fmt.Println("Trickster",
		"version:", runtime.ApplicationVersion,
		"buildInfo:", applicationBuildTime, applicationGitCommitID,
		"goVersion:", applicationGoVersion, "goArch:", applicationGoArch,
	)
}

func printUsage() {
	fmt.Println("")
	printVersion()
	fmt.Printf(`
Trickster Usage:
 
 You must provide -version, -config or both -origin-url and -origin-type.

 Print Version Info:
 trickster -version
 
 Using a configuration file:
  trickster -config /path/to/file.conf [-log-level DEBUG|INFO|WARN|ERROR] [-proxy-port 8081] [-metrics-port 8082]

 Using origin-url and origin-type:
  trickster -origin-url https://example.com -origin-type reverseproxycache [-log-level DEBUG|INFO|WARN|ERROR] [-proxy-port 8081] [-metrics-port 8082]

------

 Simple HTTP Reverse Proxy Cache listening on 8080:
   trickster -origin-url https://example.com/ -origin-type reverseproxycache -proxy-port 8080

 Simple Prometheus Accelerator listening on 9090 (default port) with Debugging:
   trickster -origin-url http://prometheus.example.com:9090/ -origin-type prometheus -log-level DEBUG

 Simple InfluxDB Accelerator listening on 8086:
   trickster -origin-url http://influxdb.example.com:8086/ -origin-type influxdb -proxy-port 8086

 Simple ClickHouse Accelerator listening on 8123:
   trickster -origin-url http://clickhouse.example.com:8123/ -origin-type clickhouse -proxy-port 8123

------

Trickster currently listens on port 9090 by default; Set in a config file,
or override using -proxy-port. The default port will change in a future release.

Default log level is INFO. Set in a config file, or override with -log-level. 

The configuration file is much more robust than the command line arguments, and the example file
is well-documented. We also have docker images on DockerHub, as well as Kubernetes and Helm
deployment examples in our GitHub repository.
 
Thank you for using and contributing to Open Source Software!

https://github.com/Comcast/trickster

`)

}
