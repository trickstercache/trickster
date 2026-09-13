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

// Package clientip resolves the real client address of a request from the
// forwarding headers set by trusted proxies in front of Trickster.
package clientip

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// ErrInvalidTrustedProxy is returned when a trusted proxy entry is neither an IP address nor a CIDR.
var ErrInvalidTrustedProxy = errors.New("invalid trusted proxy address")

// Trusted is the set of proxy addresses whose forwarding headers are believed.
type Trusted []netip.Prefix

// ParseTrusted parses IP addresses and CIDR prefixes into a Trusted set; a
// bare address trusts exactly that host.
func ParseTrusted(entries []string) (Trusted, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(Trusted, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if p, err := netip.ParsePrefix(entry); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTrustedProxy, entry)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// Contains reports whether addr belongs to the trusted set.
func (t Trusted) Contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range t {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (t Trusted) containsString(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && t.Contains(a)
}

// Resolve returns the client IP for r: the peer address unless the peer is a
// trusted proxy, in which case the nearest untrusted hop it forwarded for.
func Resolve(r *http.Request, trusted Trusted) string {
	peer := PeerIP(r.RemoteAddr)
	if len(trusted) == 0 || !trusted.containsString(peer) {
		return peer
	}
	hops := headers.HopsFromHeader(r.Header)
	for i, hop := range slices.Backward(hops) {
		addr := hopAddr(hop.RemoteAddr)
		if addr == "" {
			continue
		}
		if !trusted.containsString(addr) {
			return addr
		}
		if i == 0 {
			return addr
		}
	}
	if ip := hopAddr(r.Header.Get(headers.NameXRealIP)); ip != "" {
		return ip
	}
	return peer
}

// PeerIP returns the host portion of a net address of the form host:port.
func PeerIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func hopAddr(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(s, "[]")
	if _, err := netip.ParseAddr(s); err != nil {
		return ""
	}
	return s
}

// Middleware resolves the client IP once per request and records it on the
// request context for the access log and any handler that needs it.
func Middleware(trusted Trusted, next http.Handler) http.Handler {
	if len(trusted) == 0 || next == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := Resolve(r, trusted)
		if ip != "" {
			r = r.WithContext(tctx.WithClientIP(r.Context(), ip))
		}
		next.ServeHTTP(w, r)
	})
}
