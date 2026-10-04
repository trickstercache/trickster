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

package prefixtable

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"10.1.2.3/8":             "10.0.0.0/8",
		"2001:db8::1/32":         "2001:db8::/32",
		"::ffff:10.1.0.0/104":    "10.0.0.0/8",
		"::ffff:0:0/96":          "0.0.0.0/0",
		"::ffff:192.0.2.1/128":   "192.0.2.1/32",
		"::ffff:10.0.0.0/80":     "::/80",
		"2001:db8::ffff:0:0/112": "2001:db8::ffff:0:0/112",
	} {
		require.Equal(t, want, Canonical(netip.MustParsePrefix(in)).String(), in)
	}
}

func TestTable(t *testing.T) {
	var b Builder[string]
	for p, v := range map[string]string{
		"0.0.0.0/0":       "v4-any",
		"10.0.0.0/8":      "ten",
		"10.1.0.0/16":     "ten-one",
		"10.1.2.3/32":     "host",
		"2001:db8::/32":   "doc",
		"2001:db8:1::/48": "doc-one",
		"::/0":            "v6-any",
	} {
		_, replaced := b.Set(netip.MustParsePrefix(p), v)
		require.False(t, replaced, p)
	}
	old, replaced := b.Set(netip.MustParsePrefix("10.0.0.0/8"), "TEN")
	require.True(t, replaced)
	require.Equal(t, "ten", old)

	require.True(t, b.Covers(netip.MustParsePrefix("10.9.0.0/16")))
	require.True(t, b.Covers(netip.MustParsePrefix("10.0.0.0/8")), "an equal prefix covers")
	require.True(t, b.Covers(netip.MustParsePrefix("2001:db8:2::/48")))
	var narrow Builder[string]
	narrow.Set(netip.MustParsePrefix("10.1.0.0/16"), "ten-one")
	narrow.Set(netip.MustParsePrefix("2001:db8:1::/48"), "doc-one")
	require.False(t, narrow.Covers(netip.MustParsePrefix("10.0.0.0/8")), "a longer prefix covers a shorter one")
	require.False(t, narrow.Covers(netip.MustParsePrefix("2001:db8::/32")))
	require.False(t, narrow.Covers(netip.MustParsePrefix("192.0.2.0/24")))

	table := b.Table()
	require.Equal(t, 7, table.Len())
	for addr, want := range map[string]string{
		"10.1.2.3":         "host",
		"10.1.2.4":         "ten-one",
		"10.2.0.1":         "TEN",
		"192.0.2.1":        "v4-any",
		"2001:db8:1::5":    "doc-one",
		"2001:db8:2::5":    "doc",
		"2001:db9::1":      "v6-any",
		"fe80::1%eth0":     "v6-any",
		"::ffff:10.1.2.3":  "v6-any",
		"2001:db8:1::5%en": "doc-one",
	} {
		v, ok := table.Lookup(netip.MustParseAddr(addr))
		require.True(t, ok, addr)
		require.Equal(t, want, v, addr)
	}
	_, ok := table.Lookup(netip.Addr{})
	require.False(t, ok, "an invalid address is held")

	var empty Table[string]
	v, ok := empty.Lookup(netip.MustParseAddr("192.0.2.1"))
	require.False(t, ok)
	require.Empty(t, v)
	require.Zero(t, empty.Len())
}

func TestMasks(t *testing.T) {
	require.Equal(t, uint32(0), v4Mask(0))
	require.Equal(t, uint32(0xffffff00), v4Mask(24))
	require.Equal(t, ^uint32(0), v4Mask(32))
	for bits, want := range map[int][2]uint64{
		0:   {0, 0},
		32:  {0xffffffff00000000, 0},
		64:  {^uint64(0), 0},
		96:  {^uint64(0), 0xffffffff00000000},
		128: {^uint64(0), ^uint64(0)},
	} {
		hi, lo := v6Mask(bits)
		require.Equal(t, want, [2]uint64{hi, lo}, bits)
	}
}

func BenchmarkLookup(b *testing.B) {
	var bl Builder[uint8]
	for _, p := range []string{"10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24", "2001:db8::/32", "2001:db8:1::/48"} {
		bl.Set(netip.MustParsePrefix(p), 1)
	}
	table := bl.Table()
	addrs := []netip.Addr{netip.MustParseAddr("10.9.9.9"), netip.MustParseAddr("2001:db8:2::1"),
		netip.MustParseAddr("192.0.2.1")}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _ = table.Lookup(addrs[i%len(addrs)])
	}
}
