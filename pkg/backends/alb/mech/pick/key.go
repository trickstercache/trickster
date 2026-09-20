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
package pick

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// keyFunc derives a request's flow key; it must not allocate
type keyFunc func(*http.Request) lb.Flow

// newKeyFunc returns the extractor for a key source. v6Prefix applies to client_ip keys.
func newKeyFunc(ks options.KeySource, v6Prefix int) keyFunc {
	name := ks.Name
	switch ks.Kind {
	case options.KeyHost:
		return hostKey
	case options.KeyHeader:
		return func(r *http.Request) lb.Flow {
			if v := r.Header[name]; len(v) > 0 {
				return keyOf(v[0])
			}
			return lb.Flow{}
		}
	case options.KeyCookie:
		return func(r *http.Request) lb.Flow {
			for _, line := range r.Header[headers.NameCookie] {
				if v, ok := scanPairs(line, name, ';'); ok {
					return keyOf(strings.Trim(v, `"`))
				}
			}
			return lb.Flow{}
		}
	case options.KeyQuery:
		return func(r *http.Request) lb.Flow {
			if r.URL == nil {
				return lb.Flow{}
			}
			v, _ := scanPairs(r.URL.RawQuery, name, '&')
			return keyOf(v)
		}
	}
	return func(r *http.Request) lb.Flow { return clientIPKey(r, v6Prefix) }
}

// keyOf is the flow of a key value; an empty value identifies nothing
func keyOf(v string) lb.Flow {
	if v == "" {
		return lb.Flow{}
	}
	return lb.Flow{Key: lb.HashString(v), HasKey: true}
}

// scanPairs finds name=value among the sep-separated pairs of s without allocating. The
// value is returned as written: not unquoted, not unescaped.
func scanPairs(s, name string, sep byte) (string, bool) {
	for s != "" {
		var pair string
		if i := strings.IndexByte(s, sep); i >= 0 {
			pair, s = s[:i], s[i+1:]
		} else {
			pair, s = s, ""
		}
		pair = strings.TrimLeft(pair, " \t")
		if len(pair) > len(name) && pair[len(name)] == '=' && pair[:len(name)] == name {
			return strings.TrimRight(pair[len(name)+1:], " \t"), true
		}
	}
	return "", false
}

func hostKey(r *http.Request) lb.Flow {
	host := r.Host
	// drop a port, which follows the last colon unless that colon is inside an IPv6 literal
	if i := strings.LastIndexByte(host, ':'); i > 0 && strings.IndexByte(host[i:], ']') < 0 {
		host = host[:i]
	}
	if host == "" {
		return lb.Flow{}
	}
	return lb.Flow{Key: lb.HashFold(host), HasKey: true}
}

// clientIPKey keys on the address resolved from trusted proxies when there is one, else on
// the peer's; the port is never part of it, as an ephemeral port would end all affinity
func clientIPKey(r *http.Request, v6Prefix int) lb.Flow {
	if ip := tctx.ClientIP(r.Context()); ip != "" {
		if addr, err := netip.ParseAddr(ip); err == nil {
			return lb.Flow{Key: lb.HashAddr(addr, v6Prefix), HasKey: true}
		}
		return keyOf(ip)
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return lb.Flow{Key: lb.HashAddr(ap.Addr(), v6Prefix), HasKey: true}
	}
	if addr, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return lb.Flow{Key: lb.HashAddr(addr, v6Prefix), HasKey: true}
	}
	return keyOf(r.RemoteAddr)
}
