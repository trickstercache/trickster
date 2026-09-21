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
package options

import (
	"errors"
	"fmt"
	"net/textproto"
	"strconv"
	"strings"
)

// KeyKind is where a flow's affinity key is read from.
type KeyKind uint8

const (
	// KeyClientIP keys on the client address, after trusted-proxy resolution; never the port.
	KeyClientIP KeyKind = iota
	// KeyHost keys on the request's host, without its port and without regard to case.
	KeyHost
	// KeyHeader keys on the first value of a request header.
	KeyHeader
	// KeyCookie keys on the value of a request cookie.
	KeyCookie
	// KeyQuery keys on the raw value of a query string parameter.
	KeyQuery
	// KeySNI keys on the TLS server name a client offered; tls stream listeners only.
	KeySNI
	// KeyProxyTLV keys on the value of a PROXY protocol v2 TLV; tcp and tls stream listeners
	// that accept the PROXY protocol only.
	KeyProxyTLV
	// KeyUser keys on the name a session authenticated as; native protocol listeners only.
	KeyUser
)

// Key source spellings
const (
	KeySourceClientIP = "client_ip"
	KeySourceHost     = "host"
	KeySourceSNI      = "sni"
	KeySourceUser     = "user"
	keyPrefixHeader   = "header:"
	keyPrefixCookie   = "cookie:"
	keyPrefixQuery    = "query:"
	keyPrefixProxyTLV = "proxy_tlv:"
)

// ErrInvalidKeySource is returned for a key source that is not one of client_ip, host, sni,
// user, header:<name>, cookie:<name>, query:<name> or proxy_tlv:<type>.
var ErrInvalidKeySource = errors.New("invalid key source")

// StreamListener is what decides which keys a stream listener can read.
type StreamListener struct {
	// TLS is set for a tls listener, which reads the server name a client offers
	TLS bool
	// ProxyProtocol is set for a tcp or tls listener that accepts a PROXY protocol header
	ProxyProtocol bool
}

// OnStream reports whether a stream listener can read the key: the client address always, the
// server name when it is tls, a TLV when it accepts the PROXY protocol, nothing of a request.
func (k KeySource) OnStream(l StreamListener) bool {
	switch k.Kind {
	case KeyClientIP:
		return true
	case KeySNI:
		return l.TLS
	case KeyProxyTLV:
		return l.ProxyProtocol
	}
	return false
}

// OnHTTP reports whether an HTTP listener can read the key, which is any that is part of a
// request or is the client address.
func (k KeySource) OnHTTP() bool {
	return k.Kind != KeySNI && k.Kind != KeyProxyTLV && k.Kind != KeyUser
}

// OnNative reports whether a native protocol listener can read the key: the client address,
// or the name its session authenticated as.
func (k KeySource) OnNative() bool {
	return k.Kind == KeyClientIP || k.Kind == KeyUser
}

// KeySource is a parsed affinity key source.
type KeySource struct {
	Kind KeyKind
	// Name is the header (in canonical form), cookie or query parameter; empty otherwise.
	Name string
	// TLV is the PROXY protocol v2 TLV type of a KeyProxyTLV
	TLV byte
}

// ParseKeySource parses a key source; the empty string is client_ip.
func ParseKeySource(s string) (KeySource, error) {
	s = strings.TrimSpace(s)
	switch s {
	case "", KeySourceClientIP:
		return KeySource{Kind: KeyClientIP}, nil
	case KeySourceHost:
		return KeySource{Kind: KeyHost}, nil
	case KeySourceSNI:
		return KeySource{Kind: KeySNI}, nil
	case KeySourceUser:
		return KeySource{Kind: KeyUser}, nil
	}
	if t, ok := strings.CutPrefix(s, keyPrefixProxyTLV); ok {
		// the type is a byte, written in decimal or, as the PROXY protocol does, 0x hex
		if n, err := strconv.ParseUint(strings.TrimSpace(t), 0, 8); err == nil {
			return KeySource{Kind: KeyProxyTLV, TLV: byte(n)}, nil
		}
	}
	for prefix, kind := range map[string]KeyKind{
		keyPrefixHeader: KeyHeader, keyPrefixCookie: KeyCookie, keyPrefixQuery: KeyQuery,
	} {
		name, ok := strings.CutPrefix(s, prefix)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, " \t;=&,") {
			break
		}
		if kind == KeyHeader {
			name = textproto.CanonicalMIMEHeaderKey(name)
		}
		return KeySource{Kind: kind, Name: name}, nil
	}
	return KeySource{}, fmt.Errorf("%w %q: use %s, %s, %s, %s, %s<name>, %s<name>, %s<name> or %s<type>",
		ErrInvalidKeySource, s, KeySourceClientIP, KeySourceHost, KeySourceSNI, KeySourceUser,
		keyPrefixHeader, keyPrefixCookie, keyPrefixQuery, keyPrefixProxyTLV)
}
