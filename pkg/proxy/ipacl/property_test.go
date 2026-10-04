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
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

type scored struct { // a normalized CIDR and the verdict it carries
	prefix  netip.Prefix
	verdict Verdict
}

func naiveLongest(rules []scored, addr netip.Addr, def Verdict) Verdict {
	// the spec for match: longest, a deny winning at equal length
	addr, ok := canonical(addr)
	if !ok {
		return Deny
	}
	bestBits := -1
	best := def
	for _, rule := range rules {
		p := rule.prefix
		if p.Addr().Is4() != addr.Is4() || !p.Contains(addr) {
			continue
		}
		if p.Bits() > bestBits || (p.Bits() == bestBits && rule.verdict == Deny) {
			bestBits = p.Bits()
			best = rule.verdict
		}
	}
	return best
}

func naiveOrdered(groups [][]scored, addr netip.Addr, def Verdict) Verdict {
	// a straight first-match scan, before any reduction
	addr, ok := canonical(addr)
	if !ok {
		return Deny
	}
	for _, group := range groups {
		for _, rule := range group {
			p := rule.prefix
			if p.Addr().Is4() == addr.Is4() && p.Contains(addr) {
				return rule.verdict
			}
		}
	}
	return def
}

func TestPropertyLongestMatchesScan(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		items := genItems(rt)
		def := genDefault(rt)
		allow, deny, flat := splitLongest(rt, items)
		list, _, err := Compile(Options{Allow: allow, Deny: deny, Default: def})
		if err != nil {
			rt.Fatalf("compile: %v", err)
		}
		for _, addr := range genAddrs(rt, flat) {
			want := naiveLongest(flat, addr, defaultVerdict(def))
			got := list.Check(addr)
			if got != want || list.Check(addr) != got {
				rt.Fatalf("Check(%s) = %s, want %s\nallow %v\ndeny %v\ndefault %s",
					addr, got, want, allow, deny, def)
			}
			if addr.Is4() {
				mapped := netip.MustParseAddr("::ffff:" + addr.String())
				if list.Check(mapped) != got {
					rt.Fatalf("mapped %s = %s, want %s", mapped, list.Check(mapped), got)
				}
			}
		}
	})
}

func TestPropertyOrderedMatchesFirst(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		items := genItems(rt)
		def := genDefault(rt)
		rules := make([]Rule, len(items))
		groups := make([][]scored, len(items))
		for i, item := range items {
			if item.verdict == Allow {
				rules[i].Allow = item.raw
			} else {
				rules[i].Deny = item.raw
			}
			prefixes, err := parseEntry(item.raw)
			if err != nil {
				rt.Fatalf("parse %q: %v", item.raw, err)
			}
			groups[i] = make([]scored, len(prefixes))
			for j, p := range prefixes {
				groups[i][j] = scored{prefix: p, verdict: item.verdict}
			}
		}
		list, _, err := Compile(Options{Match: matchOrdered, Default: def, Rules: rules})
		if err != nil {
			rt.Fatalf("compile: %v", err)
		}
		var flat []scored
		for _, g := range groups {
			flat = append(flat, g...)
		}
		for _, addr := range genAddrs(rt, flat) {
			want := naiveOrdered(groups, addr, defaultVerdict(def))
			if got := list.Check(addr); got != want {
				rt.Fatalf("Check(%s) = %s, want %s\nrules %v\ndefault %s",
					addr, got, want, rules, def)
			}
		}
	})
}

type item struct {
	raw     string
	verdict Verdict
}

func genDefault(t *rapid.T) string {
	if rapid.Bool().Draw(t, "defaultAllow") {
		return fallbackAllow
	}
	return fallbackDeny
}

func genItems(t *rapid.T) []item {
	n := rapid.IntRange(0, 12).Draw(t, "n")
	items := make([]item, 0, n)
	var last netip.Prefix
	var have bool
	for i := range n {
		label := fmt.Sprintf("i%d", i)
		raw := genRaw(t, label)
		if have && last.Bits() < last.Addr().BitLen() && rapid.Bool().Draw(t, label+"sub") {
			bits := rapid.IntRange(last.Bits(), last.Addr().BitLen()).Draw(t, label+"nb")
			raw = netip.PrefixFrom(last.Addr(), bits).Masked().String()
		}
		prefixes, err := parseEntry(raw)
		if err != nil {
			t.Fatalf("generator produced %q: %v", raw, err)
		}
		if len(prefixes) == 1 {
			last = prefixes[0]
			have = true
		}
		verdict := Deny
		if rapid.Bool().Draw(t, label+"allow") {
			verdict = Allow
		}
		items = append(items, item{raw: raw, verdict: verdict})
	}
	return items
}

func genRaw(t *rapid.T, label string) string {
	switch rapid.IntRange(0, 4).Draw(t, label+"kind") {
	case 0:
		return EntryAll
	case 1:
		return genV4(t, label).String()
	case 2:
		return genV6(t, label).String()
	case 3:
		p := genV4(t, label+"m")
		return "::ffff:" + p.Addr().String() + "/" + strconv.Itoa(p.Bits()+96)
	default:
		return genV4(t, label+"h").Addr().String()
	}
}

func genV4(t *rapid.T, label string) netip.Prefix {
	bits := rapid.IntRange(0, 32).Draw(t, label+"b4")
	raw := rapid.Uint32().Draw(t, label+"u4")
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], raw)
	return netip.PrefixFrom(netip.AddrFrom4(b), bits).Masked()
}

func genV6(t *rapid.T, label string) netip.Prefix {
	bits := rapid.IntRange(0, 128).Draw(t, label+"b6")
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], rapid.Uint64().Draw(t, label+"u6a"))
	binary.BigEndian.PutUint64(b[8:], rapid.Uint64().Draw(t, label+"u6b"))
	return netip.PrefixFrom(netip.AddrFrom16(b), bits).Masked()
}

func splitLongest(t *rapid.T, items []item) (allow, deny []string, flat []scored) {
	t.Helper()
	for _, item := range items {
		if item.verdict == Allow {
			allow = append(allow, item.raw)
		} else {
			deny = append(deny, item.raw)
		}
		prefixes, err := parseEntry(item.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", item.raw, err)
		}
		for _, p := range prefixes {
			flat = append(flat, scored{prefix: p, verdict: item.verdict})
		}
	}
	return allow, deny, flat
}

func genAddrs(t *rapid.T, flat []scored) []netip.Addr {
	out := []netip.Addr{{}}
	for _, rule := range flat {
		out = append(out, rule.prefix.Addr())
		if rule.prefix.Addr().Is4() {
			out = append(out, netip.MustParseAddr("::ffff:"+rule.prefix.Addr().String()))
		}
	}
	for i := range 4 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], rapid.Uint32().Draw(t, fmt.Sprintf("a4%d", i)))
		out = append(out, netip.AddrFrom4(b))
		var c [16]byte
		binary.BigEndian.PutUint64(c[:8], rapid.Uint64().Draw(t, fmt.Sprintf("a6%d", i)))
		out = append(out, netip.AddrFrom16(c))
	}
	zoned := netip.MustParseAddr("fe80::1").WithZone("eth0")
	out = append(out, zoned, netip.MustParseAddr("fe80::1"))
	return out
}
