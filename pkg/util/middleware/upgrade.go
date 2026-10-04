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

	"golang.org/x/net/http/httpguts"
)

// IsUpgradeRequest returns true if the client asked to switch protocols, as a
// WebSocket handshake or an h2c upgrade does.
func IsUpgradeRequest(r *http.Request) bool {
	if r == nil || r.Header == nil {
		return false
	}
	return httpguts.HeaderValuesContainsToken(r.Header[headers.NameConnection], "Upgrade") &&
		r.Header.Get(headers.NameUpgrade) != ""
}

// UpgradeSwitch diverts protocol-upgrade requests to passthrough, which can
// tunnel them, and sends everything else to next. An upgrade is uncacheable by
// definition, so a cached path still hands its tunnels to the passthrough lane.
func UpgradeSwitch(passthrough, next http.Handler) http.Handler {
	if passthrough == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsUpgradeRequest(r) {
			passthrough.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// IgnoreUpgrade serves a protocol-upgrade request as the plain request it also is, which a server
// may do (RFC 9110 7.8), for a route with no one backend to tunnel it to, such as a fanout.
func IgnoreUpgrade(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsUpgradeRequest(r) {
			r = r.Clone(r.Context())
			r.Header.Del(headers.NameUpgrade)
		}
		next.ServeHTTP(w, r)
	})
}
