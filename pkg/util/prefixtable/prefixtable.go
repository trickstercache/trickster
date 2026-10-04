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

// Package prefixtable finds the value of the longest prefix holding an address, reading one map per prefix
// length, longest first, without allocating
package prefixtable

import (
	"cmp"
	"encoding/binary"
	"net/netip"
	"slices"
)

// MappedBits is how much longer an IPv4-mapped IPv6 prefix is than the IPv4 prefix it maps
const MappedBits = 96

// Canonical returns p masked, with an IPv4-mapped prefix of at least /96 rewritten as the IPv4 prefix it maps;
// a shorter one stays IPv6, since the mask clears its mapped marker
func Canonical(p netip.Prefix) netip.Prefix {
	p = p.Masked()
	if a := p.Addr(); a.Is4In6() {
		return netip.PrefixFrom(a.Unmap(), p.Bits()-MappedBits)
	}
	return p
}

// Builder collects the prefixes of a Table and their values. Its zero value is ready to use.
type Builder[V any] struct {
	v4 map[int]map[uint32]V
	v6 map[int]map[[2]uint64]V
}

// Set stores v for p, which must be canonical, and returns the value it replaced, if there was one
func (b *Builder[V]) Set(p netip.Prefix, v V) (V, bool) {
	bits := p.Bits()
	if a := p.Addr(); a.Is4() {
		if b.v4 == nil {
			b.v4 = make(map[int]map[uint32]V)
		}
		return set(b.v4, bits, v4Key(a), v)
	}
	if b.v6 == nil {
		b.v6 = make(map[int]map[[2]uint64]V)
	}
	return set(b.v6, bits, v6Key(p.Addr()), v)
}

func set[K comparable, V any](levels map[int]map[K]V, bits int, k K, v V) (V, bool) {
	m := levels[bits]
	if m == nil {
		m = make(map[K]V)
		levels[bits] = m
	}
	old, ok := m[k]
	m[k] = v
	return old, ok
}

// Covers reports whether a prefix already set holds every address of p, which must be canonical
func (b *Builder[V]) Covers(p netip.Prefix) bool {
	bits := p.Bits()
	if a := p.Addr(); a.Is4() {
		k := v4Key(a)
		for l, m := range b.v4 {
			if l > bits {
				continue // a longer prefix cannot hold all of a shorter one
			}
			if _, ok := m[k&v4Mask(l)]; ok {
				return true
			}
		}
		return false
	}
	k := v6Key(p.Addr())
	for l, m := range b.v6 {
		if l > bits {
			continue
		}
		hi, lo := v6Mask(l)
		if _, ok := m[[2]uint64{k[0] & hi, k[1] & lo}]; ok {
			return true
		}
	}
	return false
}

// Table returns what was set as an immutable Table; the Builder must not be used afterward
func (b *Builder[V]) Table() Table[V] {
	var t Table[V]
	for bits, m := range b.v4 {
		t.v4 = append(t.v4, v4Level[V]{mask: v4Mask(bits), entries: m})
		t.n += len(m)
	}
	for bits, m := range b.v6 {
		hi, lo := v6Mask(bits)
		t.v6 = append(t.v6, v6Level[V]{hi: hi, lo: lo, entries: m})
		t.n += len(m)
	}
	// a longer prefix has the greater mask, so sorting masks downward puts the longest first
	slices.SortFunc(t.v4, func(a, b v4Level[V]) int { return cmp.Compare(b.mask, a.mask) })
	slices.SortFunc(t.v6, func(a, b v6Level[V]) int {
		if c := cmp.Compare(b.hi, a.hi); c != 0 {
			return c
		}
		return cmp.Compare(b.lo, a.lo)
	})
	return t
}

// Table holds the value of each prefix set in its Builder. It is immutable, so safe for concurrent use.
type Table[V any] struct {
	v4 []v4Level[V]
	v6 []v6Level[V]
	n  int
}

type v4Level[V any] struct {
	mask    uint32
	entries map[uint32]V
}

type v6Level[V any] struct {
	hi, lo  uint64
	entries map[[2]uint64]V
}

// Lookup returns the value of the longest prefix holding addr, which must be unmapped. An IPv6 zone is not
// read, and an invalid address is held by no prefix.
func (t *Table[V]) Lookup(addr netip.Addr) (V, bool) {
	if addr.Is4() {
		k := v4Key(addr)
		for _, lv := range t.v4 {
			if v, ok := lv.entries[k&lv.mask]; ok {
				return v, true
			}
		}
	} else if addr.IsValid() {
		k := v6Key(addr)
		for _, lv := range t.v6 {
			if v, ok := lv.entries[[2]uint64{k[0] & lv.hi, k[1] & lv.lo}]; ok {
				return v, true
			}
		}
	}
	var zero V
	return zero, false
}

// Len returns the number of distinct prefixes the table holds
func (t *Table[V]) Len() int {
	return t.n
}

func v4Key(a netip.Addr) uint32 {
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}

func v6Key(a netip.Addr) [2]uint64 {
	b := a.As16()
	return [2]uint64{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

func v4Mask(bits int) uint32 {
	if bits == 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

func v6Mask(bits int) (hi, lo uint64) {
	switch {
	case bits == 0:
		return 0, 0
	case bits <= 64:
		return ^uint64(0) << (64 - bits), 0
	case bits == 128:
		return ^uint64(0), ^uint64(0)
	}
	return ^uint64(0), ^uint64(0) << (128 - bits)
}
