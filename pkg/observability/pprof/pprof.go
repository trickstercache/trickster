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

package pprof

import (
	"net/http"
	"net/http/pprof"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/router"
)

// RegisterRoutes will register the Pprof Debugging endpoints to the provided router
func RegisterRoutes(routerName string, r router.Router) {
	logger.Info("registering pprof /debug routes", logging.Pairs{"routerName": routerName})
	r.RegisterRoute("/debug/pprof/", nil, nil,
		false, http.HandlerFunc(pprof.Index))
	r.RegisterRoute("/debug/pprof/cmdline", nil, nil,
		false, http.HandlerFunc(pprof.Cmdline))
	r.RegisterRoute("/debug/pprof/profile", nil, nil,
		false, http.HandlerFunc(pprof.Profile))
	r.RegisterRoute("/debug/pprof/symbol", nil, nil,
		false, http.HandlerFunc(pprof.Symbol))
	r.RegisterRoute("/debug/pprof/trace", nil, nil,
		false, http.HandlerFunc(pprof.Trace))
}
