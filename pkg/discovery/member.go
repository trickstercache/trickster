/*
 * Copyright 2018 The Trickster Authors
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

package discovery

import (
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"strings"
)

// ReadyState indicates a discovered member's readiness as reported by the
// provider. Providers that cannot observe readiness (dns_srv, dns_a) report
// ReadyUnknown; how each state interacts with the ALB's active health
// checks and healthy_floor is a per-ALB policy, not a property of the member.
type ReadyState int8

const (
	// ReadyUnknown means the provider does not convey readiness
	ReadyUnknown ReadyState = iota
	// Ready means the provider reports the member ready for traffic
	Ready
	// NotReady means the provider reports the member not ready
	NotReady
	// Terminating means the provider reports the member shutting down; it
	// should be removed from pools ahead of the member disappearing
	Terminating
)

var readyStateNames = map[ReadyState]string{
	ReadyUnknown: "unknown",
	Ready:        "ready",
	NotReady:     "not_ready",
	Terminating:  "terminating",
}

func (s ReadyState) String() string {
	if name, ok := readyStateNames[s]; ok {
		return name
	}
	return "unknown"
}

// Member is one discovered pool member. Its identity is Key(): two members
// with the same scheme, address and path prefix are the same member
// regardless of name, weight, readiness or labels.
type Member struct {
	// Name is the provider-assigned name (pod name, SRV target, file entry
	// name). It seeds the generated backend name but is not part of the
	// member's identity.
	Name string
	// Scheme is the member's URL scheme (http, https)
	Scheme string
	// Address is the member's host:port
	Address string
	// PathPrefix is an optional path prepended to proxied request paths
	PathPrefix string
	// Weight is the member's relative load-balancing weight. 0 means
	// unweighted and is treated as 1.
	Weight int
	// ReplicaGroup optionally assigns the member to a TSM replica group
	// (the logical data shard it replicates), as conveyed by the provider
	// (e.g., a configured kubernetes label, or a member-file field). When
	// empty, the member's group follows the template backend's
	// replica_group semantics.
	ReplicaGroup string
	// Ready is the provider-reported readiness
	Ready ReadyState
	// Labels carries provider metadata (kubernetes labels, SRV priority,
	// file entry metadata) for observability and future filtering; it is
	// not part of the member's identity
	Labels map[string]string
}

// Key returns the member's stable identity: scheme://address/pathprefix
func (m *Member) Key() string {
	return m.URL()
}

// URL returns the member's base URL, suitable for a backend origin_url
func (m *Member) URL() string {
	scheme := m.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + m.Address + m.PathPrefix
}

// Equal returns true when two members are identical in identity and in all
// provider-reported attributes
func (m *Member) Equal(other *Member) bool {
	return m.Name == other.Name && m.Scheme == other.Scheme &&
		m.Address == other.Address && m.PathPrefix == other.PathPrefix &&
		m.Weight == other.Weight && m.ReplicaGroup == other.ReplicaGroup &&
		m.Ready == other.Ready && maps.Equal(m.Labels, other.Labels)
}

// Clone returns a perfect copy of the Member
func (m *Member) Clone() Member {
	out := *m
	if m.Labels != nil {
		out.Labels = maps.Clone(m.Labels)
	}
	return out
}

// Snapshot is the full membership reported by a discoverer for one query at
// one point in time. Discoverers always deliver complete snapshots, never
// deltas; consumers diff successive snapshots themselves.
type Snapshot []Member

// Clone returns a perfect copy of the Snapshot
func (s Snapshot) Clone() Snapshot {
	out := make(Snapshot, len(s))
	for i := range s {
		out[i] = s[i].Clone()
	}
	return out
}

// Canonical returns the Snapshot sorted (by Key, then Name) and deduplicated
// by Key, first-in-sorted-order wins. Canonical forms make snapshot
// comparison, debounce checks and backend-name generation deterministic
// regardless of provider enumeration order.
func (s Snapshot) Canonical() Snapshot {
	out := s.Clone()
	slices.SortStableFunc(out, func(a, b Member) int {
		if c := strings.Compare(a.Key(), b.Key()); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return slices.CompactFunc(out, func(a, b Member) bool {
		return a.Key() == b.Key()
	})
}

// Equal returns true when two snapshots have identical canonical forms
func (s Snapshot) Equal(other Snapshot) bool {
	a, b := s.Canonical(), other.Canonical()
	return slices.EqualFunc(a, b, func(x, y Member) bool {
		return x.Equal(&y)
	})
}

// BackendNames deterministically assigns a generated backend name to each
// member of the snapshot's canonical form, returning name -> member.
// The name is albName + "-" + the sanitized member name (or address when
// the member is unnamed). When two distinct members sanitize to the same
// name, all but the first (in canonical order) are disambiguated with a
// short hash of their Key, so a given membership always yields the same
// names in any order of discovery.
func (s Snapshot) BackendNames(albName string) map[string]Member {
	canon := s.Canonical()
	out := make(map[string]Member, len(canon))
	for _, m := range canon {
		seed := m.Name
		if seed == "" {
			seed = m.Address
		}
		name := albName + "-" + sanitizeBackendName(seed)
		if _, taken := out[name]; taken {
			name = fmt.Sprintf("%s-%08x", name, fnv32a(m.Key()))
		}
		out[name] = m
	}
	return out
}

// sanitizeBackendName lowercases the input and replaces any character
// outside [a-z0-9._-] with '-'
func sanitizeBackendName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, s)
}

func fnv32a(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}
