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

package middleware

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// AllowedMethods is the Allow field Trickster returns when it answers an
// OPTIONS request as the final recipient
const AllowedMethods = "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS"

// MaxForwards enforces RFC 9110 7.6.2 on TRACE and OPTIONS requests: it
// decrements a non-zero Max-Forwards before the request is forwarded, and
// answers the request here when the count has reached zero.
func MaxForwards(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !headers.ConsumeMaxForwards(r) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set(headers.NameAllow, AllowedMethods)
		h.Set(headers.NameContentLength, "0")
		headers.AddResponseVia(h, r.Proto)
		if r.Method == http.MethodTrace {
			// Trickster does not implement TRACE; echoing the request back
			// would expose fields the client never sent to this origin
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
