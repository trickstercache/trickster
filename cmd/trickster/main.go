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
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"

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
	applicationVersion = "1.0.9"
)

func main() {

	var err error
	err = config.Load(applicationName, applicationVersion, os.Args[1:])
	if err != nil {
		printVersion()
		fmt.Println("Could not load configuration:", err.Error())
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
			"name":      applicationName,
			"version":   applicationVersion,
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
				err = http.Serve(l, handlers.CompressHandler(routing.Router))
			}
			log.Error("exiting", log.Pairs{"err": err})
			wg.Done()
		}()
	}

	wg.Wait()
}

func printVersion() {
	fmt.Println(applicationName, applicationVersion, applicationBuildTime, applicationGitCommitID, applicationGoVersion, applicationGoArch)
}
