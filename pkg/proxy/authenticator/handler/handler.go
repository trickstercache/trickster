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

package handler

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/unauthorized"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Middleware returns a handler that authenticates the request and passes to
// the next handler on success, else responds with unauthorized and aborts
func Middleware(a types.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res, err := a.Authenticate(r)
		if err != nil || res == nil ||
			res.Status != types.AuthSuccess {
			if res != nil && res.ResponseHeaders != nil {
				for k, v := range res.ResponseHeaders {
					w.Header().Set(k, v)
				}
			}
			unauthorized.ServeHTTP(w, r)
			return
		}
		rsc := request.GetResources(r)
		rsc.AuthResult = res
		a.Sanitize(r)
		next.ServeHTTP(w, r)
	})
}
