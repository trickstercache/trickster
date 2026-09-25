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
// Package flow describes a connection or session as the relay knows it before it chooses an
// upstream. It stands apart from the relay so that any package can name a flow without
// depending on the relay itself.
package flow

import "net/netip"

// Flow is what the relay knows about a connection or session before it chooses an upstream.
type Flow struct {
	// Listener is the name of the listener that accepted the flow
	Listener string
	// Protocol is the listener's: tcp, tls or udp
	Protocol string
	// Client is the peer, or the source a trusted PROXY protocol header named
	Client netip.AddrPort
	// ServerName is the TLS server name the client offered; tls only
	ServerName string
	// Proxy reads the PROXY protocol header the connection arrived behind; nil without one
	Proxy ProxyHeader
}

// ProxyHeader is implemented by a client connection that was accepted behind a PROXY protocol
// header, which a listener's accepted connections may be.
type ProxyHeader interface {
	// ProxyTLV returns the value of the first version 2 TLV of the given type, if it has one.
	ProxyTLV(typ byte) ([]byte, bool)
}
