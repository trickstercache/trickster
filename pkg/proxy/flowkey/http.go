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
package flowkey

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// Value is a flow key as read from one request or flow. OK is false when there was nothing
// to derive it from, and Hash is then zero.
type Value struct {
	Hash uint64
	OK   bool
}

// Composite is the key of several sources folded together in order. OK is set only when every
// source was present; Present counts those that were, so flows lacking the same ones still group.
type Composite struct {
	Value
	Present int
}

// HTTP returns an extractor that reads the key source from requests without allocating; a
// source a request cannot carry is never present. v6Prefix masks an IPv6 client address.
func HTTP(ks KeySource, v6Prefix int) func(*http.Request) Value {
	name := ks.Name
	switch ks.Kind {
	case KeyClientIP:
		return func(r *http.Request) Value { return clientIPValue(r, v6Prefix) }
	case KeyHost:
		return hostValue
	case KeyHeader:
		return func(r *http.Request) Value {
			if v := r.Header[name]; len(v) > 0 {
				return valueOf(v[0])
			}
			return Value{}
		}
	case KeyCookie:
		return func(r *http.Request) Value {
			for _, line := range r.Header[headers.NameCookie] {
				if v, ok := scanPairs(line, name, ';'); ok {
					return valueOf(strings.Trim(v, `"`))
				}
			}
			return Value{}
		}
	case KeyQuery:
		return func(r *http.Request) Value {
			if r.URL == nil {
				return Value{}
			}
			v, _ := scanPairs(r.URL.RawQuery, name, '&')
			return valueOf(v)
		}
	case KeyMethod:
		return func(r *http.Request) Value { return valueOf(r.Method) }
	case KeyPath:
		return func(r *http.Request) Value {
			if r.URL == nil {
				return Value{}
			}
			return valueOf(r.URL.Path)
		}
	case KeyRawQuery:
		return func(r *http.Request) Value {
			if r.URL == nil {
				return Value{}
			}
			return valueOf(r.URL.RawQuery)
		}
	}
	return func(*http.Request) Value { return Value{} }
}

// HTTPComposite returns an extractor that folds the key sources, in order, into one key. A
// source the request lacks folds as zero, so requests lacking the same sources share a key.
func HTTPComposite(sources []KeySource, v6Prefix int) func(*http.Request) Composite {
	parts := make([]func(*http.Request) Value, len(sources))
	for i, ks := range sources {
		parts[i] = HTTP(ks, v6Prefix)
	}
	return func(r *http.Request) Composite {
		var c Composite
		for _, part := range parts {
			c.fold(part(r))
		}
		c.OK = len(parts) > 0 && c.Present == len(parts)
		return c
	}
}

// fold mixes one part into the composite, so that the order of the parts matters
func (c *Composite) fold(v Value) {
	if v.OK {
		c.Present++
	}
	c.Hash = lb.Mix(c.Hash ^ v.Hash)
}

// valueOf is the key of a string; an empty string identifies nothing
func valueOf(v string) Value {
	if v == "" {
		return Value{}
	}
	return Value{Hash: lb.HashString(v), OK: true}
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

func hostValue(r *http.Request) Value {
	host := r.Host
	// drop a port, which follows the last colon unless that colon is inside an IPv6 literal
	if i := strings.LastIndexByte(host, ':'); i > 0 && strings.IndexByte(host[i:], ']') < 0 {
		host = host[:i]
	}
	if host == "" {
		return Value{}
	}
	return Value{Hash: lb.HashFold(host), OK: true}
}

// clientIPValue keys on the address resolved from trusted proxies when there is one, else on
// the peer's; the port is never part of it, as an ephemeral port would end all affinity
func clientIPValue(r *http.Request, v6Prefix int) Value {
	if ip := tctx.ClientIP(r.Context()); ip != "" {
		if addr, err := netip.ParseAddr(ip); err == nil {
			return Value{Hash: lb.HashAddr(addr, v6Prefix), OK: true}
		}
		return valueOf(ip)
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return Value{Hash: lb.HashAddr(ap.Addr(), v6Prefix), OK: true}
	}
	if addr, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return Value{Hash: lb.HashAddr(addr, v6Prefix), OK: true}
	}
	return valueOf(r.RemoteAddr)
}
