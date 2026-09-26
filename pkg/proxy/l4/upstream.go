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
package l4

import (
	"errors"
	"net"
	"net/netip"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
)

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

// Upstream chooses where a flow is relayed to.
type Upstream interface {
	// Pick commits the flow to one route, or returns false to refuse it.
	Pick(Flow) (Route, bool)
}

// Retrier is optionally implemented by an Upstream that may offer another route when a dial
// fails. The relay asks only for a route that is not Final, never for a udp flow, and keeps
// every attempt inside the one connect timeout.
type Retrier interface {
	// Retry returns a route to try in place of failed, or false when there is none to offer.
	Retry(f Flow, failed Route) (Route, bool)
}

// Racer is optionally implemented by an Upstream that connects a tcp or tls flow to several
// routes at once. The relay keeps the first to connect and reports ErrAbandoned to the rest.
type Racer interface {
	// Race returns the routes to connect to together; none refuses the flow.
	Race(Flow) []Route
}

// Mirrorer is optionally implemented by an Upstream whose udp flows are copied to further
// routes: each receives every datagram the client sends, and its replies are discarded.
type Mirrorer interface {
	// Mirror returns the routes to copy the flow to, beside the primary route it was given.
	Mirror(f Flow, primary Route) []Route
}

// Route is one committed choice of upstream. The relay reports what became of it: Dialed
// once, then, only if the dial succeeded, Closed once when the relay ends.
type Route interface {
	// Addr is the host:port to dial.
	Addr() string
	// Final reports that a failed dial must not be retried on another route.
	Final() bool
	// Dialed reports how long the dial took and whether it failed. A failed dial ends the route.
	Dialed(time.Duration, error)
	// FirstByte reports the first byte or datagram received from the upstream.
	FirstByte()
	// Closed reports that the relayed connection or session has ended. err is nil unless the
	// upstream turned out to be unreachable after all, as a udp upstream that answers a
	// datagram with a port-unreachable does.
	Closed(err error)
}

// ErrAbandoned is reported to Route.Dialed when the relay gave the route up before dialing it,
// which says nothing about the upstream.
var ErrAbandoned = errors.New("route abandoned before it was dialed")

// Refusing reports whether an address can never be dialed: one whose host is under the
// reserved .invalid domain, which is how an upstream that must refuse its share is expressed.
func Refusing(addr string) bool {
	return hostnames.Reserved(addr)
}

// Static returns an upstream with one fixed address, or one that refuses every flow when the
// address can never be dialed.
func Static(addr string) Upstream {
	if Refusing(addr) {
		return refusingUpstream{}
	}
	return &staticUpstream{addr: addr}
}

// staticUpstream is its own route: there is nothing to report to and nowhere else to go
type staticUpstream struct {
	addr string
}

func (s *staticUpstream) Pick(Flow) (Route, bool) { return s, true }

func (s *staticUpstream) Addr() string { return s.addr }

func (s *staticUpstream) Final() bool { return true }

func (s *staticUpstream) Dialed(time.Duration, error) {}

func (s *staticUpstream) FirstByte() {}

func (s *staticUpstream) Closed(error) {}

// refusingUpstream refuses every flow, without a lookup or a dial
type refusingUpstream struct{}

func (refusingUpstream) Pick(Flow) (Route, bool) { return nil, false }

// flowOf describes a flow from its peer address, which a PROXY protocol listener has already
// replaced with the real client's
func flowOf(listener, protocol string, peer net.Addr, serverName string) Flow {
	f := Flow{Listener: listener, Protocol: protocol, ServerName: serverName}
	switch a := peer.(type) {
	case *net.TCPAddr:
		f.Client = a.AddrPort()
	case *net.UDPAddr:
		f.Client = a.AddrPort()
	case nil:
	default:
		if ap, err := netip.ParseAddrPort(a.String()); err == nil {
			f.Client = ap
		}
	}
	if f.Client.IsValid() {
		f.Client = netip.AddrPortFrom(f.Client.Addr().Unmap(), f.Client.Port())
	}
	return f
}
