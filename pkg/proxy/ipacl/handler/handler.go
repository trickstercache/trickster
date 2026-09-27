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

// Package handler enforces a compiled IP access list on an HTTP request.
package handler

import (
	"net/http"
	"net/netip"

	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Middleware returns next when list or next is nil. readyPath, when set, is
// served without consulting the list; listeners pass the configured readiness
// path and route checks pass an empty one. A denial is the list's HTTP status
// and does not call next. An address that cannot be parsed is denied.
func Middleware(list *ipacl.List, readyPath string, next http.Handler) http.Handler {
	if list == nil || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if readyPath != "" && r.URL.Path == readyPath {
			next.ServeHTTP(w, r)
			return
		}
		addr, err := netip.ParseAddr(subject(list, r))
		if err != nil || list.Check(addr) != ipacl.Allow {
			failures.HandleMiscFailure(list.Status(), w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// subject is the address the list judges. client_ip is the address already
// resolved through trusted proxies. peer is the host of the connection's
// remote address, which is the PROXY header source when that header was honored.
func subject(list *ipacl.List, r *http.Request) string {
	if list.Source() == ipacl.Peer {
		if r == nil {
			return ""
		}
		return clientip.PeerIP(r.RemoteAddr)
	}
	return request.ClientIP(r)
}
