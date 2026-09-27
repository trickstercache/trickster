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

// Package ipacl compiles an IP access list and matches addresses against it.
//
// A compiled List is immutable. Check allocates nothing and takes no lock, so
// any number of requests may call it at once. A reload builds a new list and
// swaps the pointer; this package does not keep that pointer.
//
// Addresses are read and compared directly. The list does not hash them and
// does not keep a per-client table.
package ipacl

import (
	"fmt"
	"net/netip"
)

// Config spellings. Empty means the default written beside each name.
const (
	matchLongest = "longest" // default
	matchOrdered = "ordered"

	fallbackDeny  = "deny" // default
	fallbackAllow = "allow"

	sourceClientIP = "client_ip" // default
	sourcePeer     = "peer"

	actionReject = "reject" // default
	actionDrop   = "drop"

	entryAll = "all"

	// DefaultStatus is the HTTP status a reject uses when status is unset.
	DefaultStatus = 403
)

// Verdict is the result of matching one address.
//
// Deny is the zero value, so an unset verdict fails closed.
type Verdict uint8

const (
	// Deny means the address is not permitted.
	Deny Verdict = iota
	// Allow means the address is permitted.
	Allow
)

// String returns allow or deny.
func (v Verdict) String() string {
	switch v {
	case Allow:
		return fallbackAllow
	case Deny:
		return fallbackDeny
	default:
		return fmt.Sprintf("verdict(%d)", v)
	}
}

// Action is what an enforcement layer does with a denied address.
//
// Reject is the zero value, which is also the default.
type Action uint8

const (
	// Reject denies with a response: an HTTP status, or a TCP reset.
	Reject Action = iota
	// Drop denies by closing or discarding, with no HTTP response.
	Drop
)

// String returns reject or drop.
func (a Action) String() string {
	switch a {
	case Drop:
		return actionDrop
	case Reject:
		return actionReject
	default:
		return fmt.Sprintf("action(%d)", a)
	}
}

// Source is which address an enforcement layer passes to Check.
//
// ClientIP is the zero value, which is also the default.
type Source uint8

const (
	// ClientIP is the address resolved through trusted proxies and the PROXY protocol.
	ClientIP Source = iota
	// Peer is the socket peer, before those layers. Listener scope only.
	Peer
)

// String returns client_ip or peer.
func (s Source) String() string {
	switch s {
	case Peer:
		return sourcePeer
	case ClientIP:
		return sourceClientIP
	default:
		return fmt.Sprintf("source(%d)", s)
	}
}

// Options is one access list, in the shape ip_acls will carry.
//
// Compile does not resolve names or attachments. Name is copied into warnings
// when the caller has set it; an empty name, including the reserved name none,
// is left for the configuration loader to accept or refuse.
type Options struct {
	// Name is the object name. The loader sets it from the map key.
	Name string `yaml:"-"`
	// Match is longest or ordered. Empty means longest.
	Match string `yaml:"match,omitempty"`
	// Default is the verdict when nothing matches. Empty means deny.
	Default string `yaml:"default,omitempty"`
	// Allow is the longest-mode allow list. Entries are addresses, CIDRs or all.
	Allow []string `yaml:"allow,omitempty"`
	// Deny is the longest-mode deny list.
	Deny []string `yaml:"deny,omitempty"`
	// AllowFile is a longest-mode file of allow entries, one per line.
	AllowFile string `yaml:"allow_file,omitempty"`
	// DenyFile is a longest-mode file of deny entries, one per line.
	DenyFile string `yaml:"deny_file,omitempty"`
	// Source is client_ip or peer. Empty means client_ip.
	Source string `yaml:"source,omitempty"`
	// Action is reject or drop. Empty means reject.
	Action string `yaml:"action,omitempty"`
	// Status is the HTTP status for reject, from 400 to 599. Zero means 403.
	Status int `yaml:"status,omitempty"`
	// Rules is the ordered-mode list. Each rule is one allow, deny or file.
	Rules []Rule `yaml:"rules,omitempty"`
}

// Rule is one ordered-mode step. Exactly one field is set.
type Rule struct {
	// Allow is one address, CIDR or all.
	Allow string `yaml:"allow,omitempty"`
	// Deny is one address, CIDR or all.
	Deny string `yaml:"deny,omitempty"`
	// AllowFile reads allow entries in file order at this position.
	AllowFile string `yaml:"allow_file,omitempty"`
	// DenyFile reads deny entries in file order at this position.
	DenyFile string `yaml:"deny_file,omitempty"`
}

// lengthTable is one prefix length and the verdict of each network at that length.
type lengthTable struct {
	bits   int
	byAddr map[netip.Addr]Verdict
}

// family is one address family, longest prefix first.
type family struct {
	tables []lengthTable
}

// List is a compiled access list. It is safe for concurrent Check calls
// and must not be mutated.
type List struct {
	v4, v6 family
	def    Verdict
	action Action
	source Source
	status int
}

// Action reports what a denial does.
func (l *List) Action() Action { return l.action }

// Source reports which address the enforcement layer should pass to Check.
func (l *List) Source() Source { return l.source }

// Status reports the HTTP status for a reject. It is 403 when unset.
func (l *List) Status() int { return l.status }

// Default reports the verdict used when no prefix matches.
func (l *List) Default() Verdict { return l.def }

// Check reports whether addr is allowed.
//
// An invalid address is denied, whatever the default is: a request with no
// client address fails closed. IPv4-mapped IPv6 addresses are matched as
// their IPv4 form. An IPv6 zone matches no prefix, as netip.Prefix.Contains
// and clientip.Contains do, so the default applies. Unmap has already
// dropped the zone from an IPv4-mapped address.
func (l *List) Check(addr netip.Addr) Verdict {
	addr, ok := canonical(addr)
	if !ok {
		return Deny
	}
	if addr.Zone() != "" {
		return l.def
	}
	fam := &l.v6
	if addr.Is4() {
		fam = &l.v4
	}
	for i := range fam.tables {
		key := maskedAddr(addr, fam.tables[i].bits)
		if v, found := fam.tables[i].byAddr[key]; found {
			return v
		}
	}
	return l.def
}

// canonical returns the address Check matches on. The zone is left in place:
// Prefix.Contains refuses a zoned address, and masking would hide that by
// stripping it.
func canonical(addr netip.Addr) (netip.Addr, bool) {
	if !addr.IsValid() {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// maskedAddr is addr with the low bits past bits cleared. bits is a length
// this list stored, so it is in range for addr's family.
func maskedAddr(addr netip.Addr, bits int) netip.Addr {
	return netip.PrefixFrom(addr, bits).Masked().Addr()
}
