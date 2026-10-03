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

package ipacl

import (
	"bytes"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

// sink keeps a compiled list alive so the compiler cannot drop Compile.
var sink *List

func BenchmarkCheck(b *testing.B) {
	// The address is covered only by the shortest prefix, so Check walks
	// every longer length and misses before it hits. The 100,000-entry
	// lists are a single /32 or /128 length: one map lookup, which is the
	// shape of a blocklist file.
	v4 := mustBenchList(b, Options{Allow: []string{
		"203.0.113.5",
		"198.51.100.0/24",
		"192.168.0.0/16",
		"172.16.0.0/12",
		"10.1.2.0/24",
		"10.2.0.0/16",
		"192.0.2.0/24",
		"203.0.113.0/24",
		"10.9.0.0/16",
		"10.0.0.0/8",
	}})
	v6 := mustBenchList(b, Options{Allow: []string{
		"2001:db8:1:2::5",
		"2001:db8:1:2::/64",
		"2001:db8:1::/48",
		"2001:db8:2::/48",
		"2001:db8:aaaa::/48",
		"2001:db8:bbbb:1::/64",
		"2001:db8:cccc::1",
		"2001:db8:dddd::/64",
		"2001:db8:eeee::/48",
		"2001:db8::/32",
	}})
	v4Hosts := hostBlock(100_000, false)
	v6Hosts := hostBlock(100_000, true)
	v4Block := mustBenchList(b, Options{Allow: v4Hosts})
	v6Block := mustBenchList(b, Options{Allow: v6Hosts})

	cases := []struct {
		name string
		list *List
		addr netip.Addr
		want Verdict
	}{
		{"v4-10", v4, netip.MustParseAddr("10.8.8.8"), Allow},
		{"v6-10", v6, netip.MustParseAddr("2001:db8:ffff::1"), Allow},
		{"v4-100000", v4Block, netip.MustParseAddr(v4Hosts[50_000]), Allow},
		{"v6-100000", v6Block, netip.MustParseAddr(v6Hosts[50_000]), Allow},
	}
	for _, tc := range cases {
		if got := tc.list.Check(tc.addr); got != tc.want {
			b.Fatalf("setup %s: Check(%s) = %s, want %s", tc.name, tc.addr, got, tc.want)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var v Verdict
			for b.Loop() {
				v = tc.list.Check(tc.addr)
			}
			if v != tc.want {
				b.Fatalf("Check = %s, want %s", v, tc.want)
			}
		})
	}
}

func BenchmarkCompileFile100k(b *testing.B) {
	path := filepath.Join(b.TempDir(), "block.lst")
	var buf bytes.Buffer
	buf.Grow(100_000 * 16)
	for i := range 100_000 {
		fmt.Fprintf(&buf, "%d.%d.%d.%d\n", (i>>24)&0xff, (i>>16)&0xff, (i>>8)&0xff, i&0xff)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		list, _, err := Compile(Options{AllowFile: path})
		if err != nil {
			b.Fatal(err)
		}
		sink = list
	}
}

func mustBenchList(b *testing.B, o Options) *List {
	b.Helper()
	list, _, err := Compile(o)
	if err != nil {
		b.Fatal(err)
	}
	return list
}

func hostBlock(n int, v6 bool) []string {
	out := make([]string, n)
	for i := range out {
		if v6 {
			u := uint32(i + 1)
			out[i] = netip.AddrFrom16([16]byte{
				0x20, 0x01, 0x0d, 0xb8,
				0, 0, 0, 0,
				0, 0, 0, 0,
				byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u),
			}).String()
			continue
		}
		out[i] = fmt.Sprintf("%d.%d.%d.%d", (i>>24)&0xff, (i>>16)&0xff, (i>>8)&0xff, i&0xff)
	}
	return out
}
