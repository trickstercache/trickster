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
package lb

// MemberOptions describes a Member to NewMember.
type MemberOptions struct {
	// Name identifies the member. Named members must be unique within a pool.
	Name string
	// Group is the member's replica group; empty means the member's Name.
	Group string
	// Weight is the member's relative share; values below 1 mean 1.
	Weight int
	// Tier is the member's failover tier. A pool selects from the lowest tier that has an
	// eligible member, so a higher tier stands by; values below 0 mean 0.
	Tier int
	// Health is the member's health source; nil means always eligible.
	Health Health
	// Stats carries runtime state over from a member this one replaces; nil starts fresh.
	Stats *Stats
	// Value is the owner's payload, returned untouched by Member.Value.
	Value any
}

// Member is one pool entry. It is immutable; its Stats and Health are live.
type Member struct {
	name   string
	group  string
	weight int
	tier   int
	hash   uint64
	health Health
	stats  *Stats
	// Value is the owner's payload: whatever it dispatches to. The core never reads it.
	Value any
}

// NewMember returns a Member for the provided options.
func NewMember(o MemberOptions) *Member {
	m := &Member{
		name:   o.Name,
		group:  o.Group,
		weight: max(o.Weight, 1),
		tier:   max(o.Tier, 0),
		hash:   hashString(o.Name),
		health: o.Health,
		stats:  o.Stats,
		Value:  o.Value,
	}
	if m.group == "" {
		m.group = m.name
	}
	if m.stats == nil {
		m.stats = &Stats{}
	}
	return m
}

// Name returns the member's name.
func (m *Member) Name() string { return m.name }

// Group returns the member's replica group, which is its name unless one was set.
func (m *Member) Group() string { return m.group }

// Weight returns the member's relative share, always at least 1.
func (m *Member) Weight() int { return m.weight }

// Tier returns the member's failover tier, 0 being the first selected from.
func (m *Member) Tier() int { return m.tier }

// Hash returns a hash of the member's name that is stable across processes and restarts.
func (m *Member) Hash() uint64 { return m.hash }

// Health returns the member's health source, or nil when it has none.
func (m *Member) Health() Health { return m.health }

// Stats returns the member's runtime state; never nil.
func (m *Member) Stats() *Stats { return m.stats }

// eligible reports whether the member's current status meets floor and it is not ejected
func (m *Member) eligible(floor int32, nowNano int64) bool {
	if m.stats.ejectedUntil.Load() > nowNano {
		return false
	}
	return m.health == nil || m.health.Get() >= floor
}

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

// hashString is FNV-1a: unseeded, so every process agrees on a name's hash
func hashString(s string) uint64 {
	h := fnvOffset64
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}
