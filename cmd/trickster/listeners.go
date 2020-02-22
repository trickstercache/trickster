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
	"crypto/tls"
	"net/http"
	"os"
	"sync"

	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/gorilla/handlers"
)

func startListener(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, router http.Handler, wg *sync.WaitGroup,
	exitOnError bool) error {
	if wg != nil {
		defer wg.Done()
	}
	l, err := proxy.NewListener(address, port, connectionsLimit, tlsConfig)
	if err != nil {
		log.Error("http listener startup failed", log.Pairs{"name": listenerName, "detail": err})
		if exitOnError {
			os.Exit(1)
		}
		return err
	}
	log.Info("proxy listener starting",
		log.Pairs{"name": listenerName, "port": port, "address": address})

	err = http.Serve(l, handlers.CompressHandler(router))
	if err != nil {
		log.Error("listener stopping", log.Pairs{"name": listenerName, "detail": err})
	}
	return err
}

func startListenerRouter(listenerName, address string, port int, connectionsLimit int,
	tlsConfig *tls.Config, path string, handler http.Handler, wg *sync.WaitGroup,
	exitOnError bool) error {
	router := http.NewServeMux()
	router.Handle(path, handler)
	return startListener(listenerName, address, port, connectionsLimit, tlsConfig, router, wg, exitOnError)
}
