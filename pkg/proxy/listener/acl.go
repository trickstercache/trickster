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

package listener

import (
	"errors"
	"net"
	"net/netip"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
)

// aclListener judges the socket peer before a connection is returned. It sits on the raw
// listener, beneath the connection limit, PROXY protocol and TLS, so a denial never becomes
// a limited connection and never reads a header or starts a handshake.
type aclListener struct {
	net.Listener
	list      *atomic.Pointer[ipacl.List]
	decisions *atomic.Pointer[metrics.IPACLDecision]
	// judgeClientIP is set for a native listener without PROXY protocol, where the socket
	// peer is the client. HTTP resolves client_ip in middleware. Stream tcp and tls resolve
	// it from Flow.Client. A peer list is always judged here.
	judgeClientIP bool
}

func newACLListener(inner net.Listener, list *atomic.Pointer[ipacl.List],
	decisions *atomic.Pointer[metrics.IPACLDecision], judgeClientIP bool,
) net.Listener {
	if list == nil {
		list = &atomic.Pointer[ipacl.List]{}
	}
	return &aclListener{Listener: inner, list: list, decisions: decisions, judgeClientIP: judgeClientIP}
}

func (a *aclListener) Accept() (net.Conn, error) {
	for {
		c, err := a.Listener.Accept()
		if err != nil {
			return nil, err
		}
		list := a.list.Load()
		if list == nil || !a.judges(list) {
			return c, nil
		}
		allowed := a.allows(list, c)
		a.observe(allowed)
		if allowed {
			return c, nil
		}
		turnAway(c, list.Action())
	}
}

// judges reports whether this socket applies the list. A client_ip list on HTTP or a
// proxied stream is resolved later, so accepting it here is not a decision.
func (a *aclListener) judges(list *ipacl.List) bool {
	return list.Source() != ipacl.ClientIP || a.judgeClientIP
}

func (a *aclListener) observe(allowed bool) {
	if a.decisions == nil {
		return
	}
	if dec := a.decisions.Load(); dec != nil {
		dec.Observe(allowed)
	}
}

func (a *aclListener) allows(list *ipacl.List, c net.Conn) bool {
	addr, ok := socketPeer(c)
	if !ok {
		return false
	}
	return list.Check(addr) == ipacl.Allow
}

// socketPeer is the address of the accepted connection. An address that cannot be parsed is
// reported as not ok so the caller denies it.
func socketPeer(c net.Conn) (netip.Addr, bool) {
	if c == nil || c.RemoteAddr() == nil {
		return netip.Addr{}, false
	}
	switch a := c.RemoteAddr().(type) {
	case *net.TCPAddr:
		ap := a.AddrPort()
		if !ap.IsValid() {
			return netip.Addr{}, false
		}
		return ap.Addr().Unmap(), true
	default:
		host, _, err := net.SplitHostPort(a.String())
		if err != nil {
			host = a.String()
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return netip.Addr{}, false
		}
		return addr.Unmap(), true
	}
}

// turnAway consumes a denied connection. reject resets the TCP connection; drop closes it.
// Anything that cannot be reset is closed.
func turnAway(c net.Conn, action ipacl.Action) {
	if action != ipacl.Drop && resetTCP(c) == nil {
		return
	}
	_ = c.Close()
}

// resetTCP ends a TCP connection with a reset, matching observedConnection.Reset.
func resetTCP(c net.Conn) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return errors.ErrUnsupported
	}
	if err := tc.SetLinger(0); err != nil {
		return err
	}
	return tc.Close()
}
