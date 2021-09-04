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

package handlers

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// HandleLocalResponse responds to an HTTP Request based on the local configuration without making any upstream requests
func HandleLocalResponse(w http.ResponseWriter, r *http.Request) {
	rsc := request.GetResources(r)
	if w == nil || rsc == nil {
		return
	}
	p := rsc.PathConfig
	if p == nil {
		return
	}
	if len(p.ResponseHeaders) > 0 {
		headers.UpdateHeaders(w.Header(), p.ResponseHeaders)
	}
	if p.ResponseCode > 0 {
		w.WriteHeader(p.ResponseCode)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	w.Write([]byte(p.ResponseBody))
}

// HandleBadRequestResponse responds to an HTTP Request with 400 Bad Request
func HandleBadRequestResponse(w http.ResponseWriter, r *http.Request) {
	if w == nil {
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	w.Write(nil)
}

// HandleInternalServerError responds to an HTTP Request with 500 Internal Server Error
func HandleInternalServerError(w http.ResponseWriter, r *http.Request) {
	if w == nil {
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
	w.Write(nil)
}

// HandleBadGateway responds to an HTTP Request with 502 Bad Gateway
func HandleBadGateway(w http.ResponseWriter, r *http.Request) {
	if w == nil {
		return
	}
	w.WriteHeader(http.StatusBadGateway)
	w.Write(nil)
}
