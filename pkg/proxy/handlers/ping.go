/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package handlers

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"
)

// RegisterPingHandler registers the application's /ping handler
// func RegisterPingHandler() {
// 	routing.Router.HandleFunc(config.Main.PingHandlerPath, pingHandler).Methods("GET")
// }

// // pingHandler responds to an HTTP Request with 200 OK and "pong"
// func pingHandler(w http.ResponseWriter, r *http.Request) {
// 	w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
// 	w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
// 	w.WriteHeader(http.StatusOK)
// 	w.Write([]byte("pong"))
// }

// PingHandleFunc responds to an HTTP Request with 200 OK and "pong"
func PingHandleFunc(conf *config.TricksterConfig) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("pong"))
	}
}
