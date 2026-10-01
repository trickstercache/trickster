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

// Package stream judges tcp, tls and udp flows by the geo ACLs of the backends a stream listener serves. Only
// the daemon's wiring imports it, since it depends on the relay.
package stream

import (
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"
)

// Admission judges a stream listener's clients, not their datagrams, by its backends' geo ACLs. A refusal
// resets tcp and tls and drops udp, since an opaque stream carries no message.
type Admission struct {
	peer  *acl.ACL
	hosts *l4.HostTable[*acl.ACL]
}

var _ l4.Admission = (*Admission)(nil)

// ForBackend returns an Admission that judges every flow of a tcp or udp listener by one geo ACL
func ForBackend(a *acl.ACL) *Admission {
	return &Admission{peer: a}
}

// ForHosts returns an Admission judging each tls flow by the geo ACL of the backend its server name routes
// to, mapped as the relay maps them; a host mapped to nil is allowed
func ForHosts(hosts *l4.HostTable[*acl.ACL]) *Admission {
	return &Admission{hosts: hosts}
}

// Peer judges a tcp or udp flow by its client address
func (a *Admission) Peer(f l4.Flow) l4.Verdict {
	if a.peer == nil {
		return l4.Allow
	}
	return verdict(a.peer.Check(f.Client.Addr(), acl.PlaneStream))
}

// Flow judges a tls flow once its server name is known
func (a *Admission) Flow(f l4.Flow) l4.Verdict {
	if a.hosts == nil {
		return l4.Allow
	}
	g := a.hosts.Lookup(f.ServerName)
	if g == nil {
		return l4.Allow
	}
	return verdict(g.Check(f.Client.Addr(), acl.PlaneStream))
}

// Datagram is never asked, since Datagrams is false
func (*Admission) Datagram(l4.Flow, int) l4.Verdict {
	return l4.Allow
}

// Datagrams reports false: a udp flow is judged once, by its client
func (*Admission) Datagrams() bool {
	return false
}

func verdict(r acl.Result) l4.Verdict {
	if r == acl.ResultDenied {
		return l4.Reject
	}
	return l4.Allow
}
