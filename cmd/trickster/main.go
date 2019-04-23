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
	"net/http"
	_ "net/http/pprof" // Comment to disable. Available on :METRICS_PORT/debug/pprof
	"os"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/routing"
	rr "github.com/Comcast/trickster/internal/routing/registration"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"

	"github.com/gorilla/handlers"
)

const (
	applicationName    = "trickster"
	applicationVersion = "1.0.4"
)

func main() {

	var err error
	err = config.Load(applicationName, applicationVersion, os.Args[1:])
	if err != nil {
		fmt.Println("Could not load configuration:", err.Error())
		os.Exit(1)
	}

	if config.Flags.PrintVersion {
		fmt.Println(applicationVersion)
		os.Exit(0)
	}

	log.Init()
	defer log.Logger.Close()
	log.Info("application start up", log.Pairs{"name": applicationName, "version": applicationVersion})

	metrics.Init()
	cr.LoadCachesFromConfig()
	proxy.RegisterPingHandler()
	rr.RegisterProxyRoutes()

	log.Info("proxy http endpoint starting", log.Pairs{"address": config.ProxyServer.ListenAddress, "port": config.ProxyServer.ListenPort})

	// Start the Server
	err = http.ListenAndServe(fmt.Sprintf("%s:%d", config.ProxyServer.ListenAddress, config.ProxyServer.ListenPort), handlers.CompressHandler(routing.Router))
	log.Error("exiting", log.Pairs{"err": err})
}
