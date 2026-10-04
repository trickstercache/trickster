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

// Package stream enforces compiled IP access lists on stream listeners through
// the relay's admission.
package stream

import (
	"net/netip"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

// Attached is one compiled list and the name its decision metric uses.
type Attached struct {
	List *ipacl.List
	Name string
}

type counted struct { // a list and the counters resolved when the admission was built
	list  *ipacl.List
	dec   *metrics.IPACLDecision
	name  string
	scope string
}

func attach(a Attached, scope string) *counted {
	if a.List == nil {
		return nil
	}
	c := &counted{list: a.List, name: a.Name, scope: scope}
	if a.Name != "" {
		c.dec = metrics.NewIPACLDecision(a.Name, scope)
	}
	return c
}

// New returns a stream listener's admission, or nil with nothing to judge; a tcp or tls listener's peer list was
// judged at accept, and a backend's peer list is not enforced
func New(protocol string, listener Attached, table *l4.Table, backend map[l4.Upstream]Attached) l4.Admission {
	lists := make(map[l4.Upstream]*counted, len(backend))
	for up, attached := range backend {
		if up == nil || attached.List == nil || attached.List.Source() == ipacl.Peer {
			continue
		}
		lists[up] = attach(attached, metrics.IPACLScopeBackend)
	}
	var listenerList *counted
	if judgesListener(protocol, listener.List) {
		listenerList = attach(listener, metrics.IPACLScopeListener)
	}
	if len(lists) == 0 && listenerList == nil {
		return nil
	}
	return &admission{protocol: protocol, listener: listenerList, table: table, backend: lists}
}

func judgesListener(protocol string, listener *ipacl.List) bool {
	// udp has no accept wrapper, so both its sources are judged here; tcp and tls peer lists were judged at accept
	if listener == nil {
		return false
	}
	if protocol == l4.ProtocolUDP {
		return true
	}
	return listener.Source() != ipacl.Peer
}

type admission struct { // one listener's lists, immutable after New
	protocol string
	listener *counted
	table    *l4.Table
	backend  map[l4.Upstream]*counted
}

func (a *admission) Peer(f l4.Flow) l4.Verdict {
	// the listener list judges Flow.Client, from the socket or a trusted PROXY header; on udp the backend list
	// follows, while tcp's waits for Flow
	if v := judge(a.listenerList(), f.Client.Addr()); v != l4.Allow {
		return v
	}
	if a.protocol == l4.ProtocolUDP {
		return judge(a.backendList(""), f.Client.Addr())
	}
	return l4.Allow
}

func (a *admission) Flow(f l4.Flow) l4.Verdict {
	// the backend list is the one of the upstream the table routes the server name to, as the relay just did
	if a.protocol == l4.ProtocolUDP {
		return l4.Allow
	}
	return judge(a.backendList(f.ServerName), f.Client.Addr())
}

func (a *admission) Datagram(l4.Flow, int) l4.Verdict {
	return l4.Allow // never asked, since Datagrams is false
}

func (a *admission) Datagrams() bool {
	// an allowed udp flow is not judged per datagram, and the relay's peer-stage hold keeps a denied one out
	return false
}

func (a *admission) listenerList() *counted {
	// a tcp or tls peer list was applied to the socket before PROXY replaced the connection address
	return a.listener
}

func (a *admission) backendList(serverName string) *counted {
	// the lists are keyed by the upstream the relay's own lookup selected, so selection and enforcement agree
	if a.table == nil {
		return nil
	}
	return a.backend[a.table.Lookup(serverName)]
}

func judge(c *counted, addr netip.Addr) l4.Verdict {
	// a nil list allows and Check denies an invalid address; reject and drop keep the list's action, and both count
	// as deny
	if c == nil || c.list == nil || c.list.Check(addr) == ipacl.Allow {
		if c != nil && c.list != nil {
			c.dec.Observe(true)
		}
		return l4.Allow
	}
	c.dec.Observe(false)
	logger.Debug("ip acl denied", logging.Pairs{
		keys.IP_ACL:  c.name,
		keys.Scope:   c.scope,
		keys.Address: addr.String(),
		keys.Action:  c.list.Action().String(),
	})
	if c.list.Action() == ipacl.Drop {
		return l4.Drop
	}
	return l4.Reject
}
