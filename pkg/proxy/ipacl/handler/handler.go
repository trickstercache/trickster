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

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Scopes an HTTP attachment passes to Middleware. They are the metric's scope label.
const (
	ScopeListener = metrics.IPACLScopeListener
	ScopeBackend  = metrics.IPACLScopeBackend
	ScopePath     = metrics.IPACLScopePath
)

// Middleware returns next when list or next is nil. A client_ip list judges
// request.ClientIP. A peer list judges r.RemoteAddr only for HTTP/3, which has
// no TCP accept; every other peer list was judged on the socket and is skipped.
// reject writes the list's HTTP status, an empty body and Cache-Control: no-store.
// drop panics with http.ErrAbortHandler and writes nothing. An address that
// cannot be parsed is denied. name and scope select the decision counters resolved
// here; a skipped peer list was already counted at accept.
func Middleware(list *ipacl.List, name, scope string, next http.Handler) http.Handler {
	if list == nil || next == nil {
		return next
	}
	decision := metrics.NewIPACLDecision(name, scope)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if list.Source() == ipacl.Peer && (r == nil || r.ProtoMajor != 3) {
			next.ServeHTTP(w, r)
			return
		}
		judged := subject(list, r)
		addr, err := netip.ParseAddr(judged)
		allowed := err == nil && list.Check(addr) == ipacl.Allow
		decision.Observe(allowed)
		if !allowed {
			logger.Debug("ip acl denied", logging.Pairs{
				keys.IP_ACL:  name,
				keys.Scope:   scope,
				keys.Address: judged,
				keys.Action:  list.Action().String(),
			})
			if list.Action() == ipacl.Drop {
				panic(http.ErrAbortHandler)
			}
			if w != nil {
				w.Header().Set(headers.NameCacheControl, headers.ValueNoStore)
			}
			failures.HandleMiscFailure(list.Status(), w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// subject is the address the list judges. client_ip is the address already
// resolved through trusted proxies and the PROXY protocol. peer is the host of
// r.RemoteAddr, which for HTTP/3 is the QUIC peer.
func subject(list *ipacl.List, r *http.Request) string {
	if list.Source() == ipacl.Peer {
		if r == nil {
			return ""
		}
		return clientip.PeerIP(r.RemoteAddr)
	}
	return request.ClientIP(r)
}
