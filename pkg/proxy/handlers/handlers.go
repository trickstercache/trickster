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

// Package handlers provides several non-proxy handlers for use internally
// by other Trickster handlers
package handlers

import "net/http"

type Lookup map[string]http.Handler

// Names of the handlers that answer a request locally, without an upstream
const (
	// NameLocalResponse serves a configured fixed response
	NameLocalResponse = "localresponse"
	// NameRedirect answers with a redirection composed from the request
	NameRedirect = "redirect"
	// NameStatic serves files from a local directory
	NameStatic = "static"
)

// IsLocal reports whether a handler answers from configuration alone. A
// request such a handler matches has no upstream to be tunneled to, so an
// upgrade request is answered by the handler rather than diverted to one.
func IsLocal(name string) bool {
	return name == NameLocalResponse || name == NameRedirect || name == NameStatic
}
