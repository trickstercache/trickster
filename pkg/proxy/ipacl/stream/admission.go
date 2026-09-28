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

	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

// New returns the admission for a stream listener, or nil when the relay has
// nothing to judge. A tcp or tls listener list whose source is the socket peer
// is already enforced at accept, including when PROXY protocol is enabled, so
// it is not asked again here. A backend list whose source is the socket peer
// is not a stream placement and is not enforced.
func New(protocol string, listener *ipacl.List, table *l4.Table, backend map[l4.Upstream]*ipacl.List) l4.Admission {
	lists := make(map[l4.Upstream]*ipacl.List, len(backend))
	for up, list := range backend {
		if up == nil || list == nil || list.Source() == ipacl.Peer {
			continue
		}
		lists[up] = list
	}
	if len(lists) == 0 && !judgesListener(protocol, listener) {
		return nil
	}
	return &admission{protocol: protocol, listener: listener, table: table, backend: lists}
}

// judgesListener reports whether the listener list is applied by admission.
// UDP has no accept wrapper, so both of its sources are judged here.
func judgesListener(protocol string, listener *ipacl.List) bool {
	if listener == nil {
		return false
	}
	if protocol == l4.ProtocolUDP {
		return true
	}
	return listener.Source() != ipacl.Peer
}

// admission is one listener's lists. It is immutable after New.
type admission struct {
	protocol string
	listener *ipacl.List
	table    *l4.Table
	backend  map[l4.Upstream]*ipacl.List
}

// Peer judges the listener list from Flow.Client, which the relay has already
// set from the socket or from a trusted PROXY header. On UDP the datagram peer
// is that address for either source, and the one backend list follows the
// listener list. On TCP the backend list waits for Flow, after the route exists.
func (a *admission) Peer(f l4.Flow) l4.Verdict {
	if v := judge(a.listenerList(), f.Client.Addr()); v != l4.Allow {
		return v
	}
	if a.protocol == l4.ProtocolUDP {
		return judge(a.backendList(""), f.Client.Addr())
	}
	return l4.Allow
}

// Flow judges the backend list for the upstream the listener table routes this
// server name to. The relay calls Flow only after that same Lookup has selected
// a route, so the list is the selected backend's.
func (a *admission) Flow(f l4.Flow) l4.Verdict {
	if a.protocol == l4.ProtocolUDP {
		return l4.Allow
	}
	return judge(a.backendList(f.ServerName), f.Client.Addr())
}

// Datagram is unused. Denied UDP flows use the relay's peer-stage hold.
func (a *admission) Datagram(l4.Flow, int) l4.Verdict { return l4.Allow }

// Datagrams reports false so an allowed UDP flow is not judged again per
// datagram, and a denied one is held by the relay rather than by a second table.
func (a *admission) Datagrams() bool { return false }

// listenerList is the listener list this admission applies. A tcp or tls peer
// list was applied to the socket before PROXY replaced the connection address.
func (a *admission) listenerList() *ipacl.List {
	if !judgesListener(a.protocol, a.listener) {
		return nil
	}
	return a.listener
}

// backendList is the list stored for the upstream Table.Lookup returns.
// Calling Lookup again is the routing decision the relay just made; the lists
// are keyed by that upstream, so selection and enforcement stay the same.
func (a *admission) backendList(serverName string) *ipacl.List {
	if a.table == nil {
		return nil
	}
	return a.backend[a.table.Lookup(serverName)]
}

// judge maps one list onto a relay verdict. Check denies an invalid address.
// A nil list allows. Reject and drop stay the list's own action.
func judge(list *ipacl.List, addr netip.Addr) l4.Verdict {
	if list == nil || list.Check(addr) == ipacl.Allow {
		return l4.Allow
	}
	if list.Action() == ipacl.Drop {
		return l4.Drop
	}
	return l4.Reject
}
