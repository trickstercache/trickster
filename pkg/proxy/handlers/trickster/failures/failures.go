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

package failures

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// HandleBadRequestResponse responds to an HTTP Request with 400 Bad Request
func HandleBadRequestResponse(w http.ResponseWriter, _ *http.Request) {
	HandleMiscFailure(http.StatusBadRequest, w)
}

// HandleInternalServerError responds to an HTTP Request with 500 Internal Server Error
func HandleInternalServerError(w http.ResponseWriter, _ *http.Request) {
	HandleMiscFailure(http.StatusInternalServerError, w)
}

// HandleBadGateway responds to an HTTP Request with 502 Bad Gateway
func HandleBadGateway(w http.ResponseWriter, _ *http.Request) {
	HandleMiscFailure(http.StatusBadGateway, w)
}

// HandleUnauthorized responds to an HTTP Request with a 401 Unauthorized
func HandleUnauthorized(w http.ResponseWriter, _ *http.Request) {
	if w == nil {
		return
	}
	w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte("Unauthorized"))
}

// HandleNotFound responds to an HTTP Request with a 404 Not Found
func HandleNotFound(w http.ResponseWriter, _ *http.Request) {
	if w == nil {
		return
	}
	w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("Resource Not Found"))
}

// HandlePayloadTooLarge responds to an HTTP Request with a 413 Payload Too Large
func HandlePayloadTooLarge(w http.ResponseWriter, _ *http.Request) {
	if w == nil {
		return
	}
	w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	w.Write([]byte("Request Body is too large"))
}

// HandleMiscFailure responds to an HTTP Request the provided status code
func HandleMiscFailure(code int, w http.ResponseWriter) {
	if w == nil {
		return
	}
	w.WriteHeader(code)
	w.Write(nil)
}
