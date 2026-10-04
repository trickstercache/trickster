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
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
)

type aclListener struct { // judges the raw socket's peer, beneath the connection limit, PROXY protocol and TLS
	net.Listener
	list          *atomic.Pointer[ipacl.List]
	decisions     *atomic.Pointer[acceptACL]
	judgeClientIP bool // set where the socket peer is the client: a native listener without PROXY protocol
}

func newACLListener(inner net.Listener, list *atomic.Pointer[ipacl.List],
	decisions *atomic.Pointer[acceptACL], judgeClientIP bool,
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
		a.logDenial(list, c)
		turnAway(c, list.Action())
	}
}

func (a *aclListener) judges(list *ipacl.List) bool {
	// a client_ip list on HTTP or a proxied stream is resolved later, so it is not this socket's to judge
	return list.Source() != ipacl.ClientIP || a.judgeClientIP
}

func (a *aclListener) decision() *acceptACL {
	if a.decisions == nil {
		return nil
	}
	return a.decisions.Load()
}

func (a *aclListener) observe(allowed bool) {
	if dec := a.decision(); dec != nil {
		dec.dec.Observe(allowed)
	}
}

func (a *aclListener) logDenial(list *ipacl.List, c net.Conn) {
	// the address is the socket peer when it parses, and the raw remote address when it does not
	name := ""
	if dec := a.decision(); dec != nil {
		name = dec.name
	}
	addr := ""
	if parsed := clientip.FromNetAddr(c.RemoteAddr()); parsed.IsValid() {
		addr = parsed.String()
	} else if c.RemoteAddr() != nil {
		addr = c.RemoteAddr().String()
	}
	logger.Debug("ip acl denied", logging.Pairs{
		keys.IP_ACL:  name,
		keys.Scope:   metrics.IPACLScopeListener,
		keys.Address: addr,
		keys.Action:  list.Action().String(),
	})
}

func (a *aclListener) allows(list *ipacl.List, c net.Conn) bool {
	// an address that does not parse is invalid, which Check denies
	return list.Check(clientip.FromNetAddr(c.RemoteAddr())) == ipacl.Allow
}

func turnAway(c net.Conn, action ipacl.Action) {
	// reject resets a TCP connection and drop closes it; one that cannot be reset is closed
	if action != ipacl.Drop && resetTCP(c) == nil {
		return
	}
	_ = c.Close()
}

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
