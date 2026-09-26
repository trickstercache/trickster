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
	"time"
)

// Verdict is what an admission decides for a connection, session or datagram.
type Verdict uint8

const (
	// Allow lets the flow through.
	Allow Verdict = iota
	// Drop turns the flow away quietly: a connection is closed, a datagram is discarded.
	Drop
	// Reject turns the flow away with a reset on a tcp or tls listener; on udp it is Drop.
	Reject
)

// Admission judges flows before they are relayed; a Config without one admits everything. It is
// asked on a connection's or a flow's own worker, never on a listener's accept or receive loop.
type Admission interface {
	// Peer judges a connection before any byte of it is read, or a new udp flow before its first
	// datagram is relayed, by the client address alone: f carries Listener, Protocol, Client and Proxy.
	Peer(f Flow) Verdict
	// Flow judges a connection once its flow is fully known and routed: on a tls listener,
	// after the server name was read. Not called for udp.
	Flow(f Flow) Verdict
	// Datagram judges one datagram of size bytes before its flow relays it, the first included,
	// so a flow dials nothing until one is allowed; called only while Datagrams reports true.
	Datagram(f Flow, size int) Verdict
	// Datagrams reports whether Datagram must be consulted; read once per Config swap, never
	// per datagram.
	Datagrams() bool
}

// DefaultDeniedHold is how long a udp client whose new flow the admission denied at the peer
// stage is held, its datagrams dropped unasked, when the admission sets no hold of its own.
const DefaultDeniedHold = 5 * time.Second

// Holder is optionally implemented by an Admission to set, per flow, how long a udp client it
// denied at the peer stage is held before it is judged again; zero holds nothing. A config swap
// releases every hold, so a denial a reload reverses ends with the reload.
type Holder interface {
	Hold(f Flow) time.Duration
}

// resetter is a connection that can end with a reset rather than a close.
type resetter interface {
	Reset() error
}

// resetConn ends the connection with a reset where it can: a wrapped connection knows how, and
// a plain tcp connection is closed with no linger; anything else is unsupported and left open
func resetConn(c net.Conn) error {
	switch r := c.(type) {
	case resetter:
		return r.Reset()
	case *net.TCPConn:
		if err := r.SetLinger(0); err != nil {
			return err
		}
		return r.Close()
	}
	return errors.ErrUnsupported
}

// deny ends a connection its admission turned away: a reject resets it, a drop closes it
func (s *Server) deny(client net.Conn, v Verdict) {
	if v != Reject || resetConn(client) != nil {
		_ = client.Close()
	}
	s.result(ResultDenied)
}
