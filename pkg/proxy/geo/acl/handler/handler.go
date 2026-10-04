/*
 * Copyright 2026 The Trickster Authors
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

// Package handler provides the HTTP middleware that judges requests by a geo ACL.
package handler

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// New returns next behind the geo ACL, or next itself without one. A denied request gets the refusal; a
// counted one goes on, naming the ACL in a request header for the origin
func New(a *acl.ACL, next http.Handler) http.Handler {
	if a == nil || next == nil {
		return next
	}
	name := []string{a.Name()}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch a.CheckRequest(r) {
		case acl.ResultDenied:
			a.WriteResponse(w)
			return
		case acl.ResultCounted:
			r.Header[headers.NameTricksterGeoDenied] = name
		}
		next.ServeHTTP(w, r)
	})
}
