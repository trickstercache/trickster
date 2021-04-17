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

// Package middleware provides middleware functions used by the Router
// during registration to construct handler chains
package middleware

import (
	"net/http"
	"strings"
)

// StripPathPrefix removes the provided prefix from incoming HTTP Requests URLs
func StripPathPrefix(prefix string, next http.Handler) http.Handler {

	// This is for adjusting due to backend routing, so it needs to have
	// leading and trailing slashes, such as /backend-name/
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	// We use the trailing slash as the starting slash on the modified path
	l := len(prefix) - 1

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r != nil && r.URL != nil && strings.HasPrefix(r.URL.Path, prefix) {
			r.URL.Path = r.URL.Path[l:]
		}
		next.ServeHTTP(w, r)
	})
}
