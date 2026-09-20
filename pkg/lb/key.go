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

import "net/netip"

// Flow keys are hashed with no per-process seed: replicas behind one front door must agree on
// where a key lands, and a restart must not move it.

// Mix is a 64-bit finalizer: every input bit affects every output bit. Strategies use it to
// combine a flow key with a member hash.
func Mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// HashString returns the flow key of a string.
func HashString(s string) uint64 {
	return Mix(hashString(s))
}

// HashBytes returns the flow key of a byte slice; equal to HashString of the same bytes.
func HashBytes(b []byte) uint64 {
	h := fnvOffset64
	for _, c := range b {
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return Mix(h)
}

// HashFold returns the flow key of a string with ASCII letters folded to lower case, for
// identifiers such as host names that compare without regard to case.
func HashFold(s string) uint64 {
	h := fnvOffset64
	for i := range len(s) {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		h ^= uint64(c)
		h *= fnvPrime64
	}
	return Mix(h)
}

// HashAddr returns the flow key of an IP address, never of a port. An IPv6 address is masked
// to v6Prefix bits first, since privacy addressing rotates a client's low bits; a prefix
// outside 1-128 keeps the whole address. An IPv4-mapped IPv6 address keys as its IPv4 form.
func HashAddr(addr netip.Addr, v6Prefix int) uint64 {
	addr = addr.Unmap()
	if addr.Is6() && v6Prefix >= 1 && v6Prefix < 128 {
		if p, err := addr.Prefix(v6Prefix); err == nil {
			addr = p.Addr()
		}
	}
	if addr.Is4() {
		b := addr.As4()
		return HashBytes(b[:])
	}
	b := addr.As16()
	return HashBytes(b[:])
}
