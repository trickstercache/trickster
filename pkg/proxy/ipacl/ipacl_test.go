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
	"net/netip"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustCompile(t *testing.T, o Options) (*List, []string) {
	t.Helper()
	list, warnings, err := Compile(o)
	require.NoError(t, err)
	require.NotNil(t, list)
	return list, warnings
}

func TestLongestIPv4(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Allow: []string{"10.0.0.0/8", "10.1.2.3", "192.168.1.5"},
		Deny:  []string{"10.1.2.0/24"},
	})
	require.Empty(t, warnings)

	checks := []struct {
		addr string
		want Verdict
	}{
		{"10.1.2.3", Allow}, // /32 beats the /24 deny
		{"10.1.2.9", Deny},
		{"10.9.9.9", Allow},
		{"192.168.1.5", Allow},
		{"192.168.1.6", Deny},
		{"11.0.0.1", Deny},
		{"::ffff:10.1.2.3", Allow},
		{"::ffff:10.1.2.9", Deny},
		{"::ffff:10.9.9.9", Allow},
		{"::ffff:11.0.0.1", Deny},
	}
	for _, tc := range checks {
		t.Run(tc.addr, func(t *testing.T) {
			require.Equal(t, tc.want, list.Check(netip.MustParseAddr(tc.addr)))
		})
	}
}

func TestLongestIPv6(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Allow: []string{"2001:db8::/32", "2001:db8:1:2::5"},
		Deny:  []string{"2001:db8:1::/48"},
	})
	require.Empty(t, warnings)
	checks := []struct {
		addr string
		want Verdict
	}{
		{"2001:db8:1:2::5", Allow},
		{"2001:db8:1::1", Deny},
		{"2001:db8:2::1", Allow},
		{"2001:db9::1", Deny},
		{"::1", Deny},
		{"10.1.2.3", Deny},
	}
	for _, tc := range checks {
		t.Run(tc.addr, func(t *testing.T) {
			require.Equal(t, tc.want, list.Check(netip.MustParseAddr(tc.addr)))
		})
	}
}

func TestLongestTieDenies(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Name:  "office",
		Allow: []string{"10.1.2.0/24", "2001:db8::/32"},
		Deny:  []string{"10.1.2.0/24", "2001:db8::/32"},
	})
	require.Equal(t, []string{
		`ip acl "office": 10.1.2.0/24 is listed as both allow and deny; deny wins (deny)`,
		`ip acl "office": 2001:db8::/32 is listed as both allow and deny; deny wins (deny)`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8::1")))

	// Allow is compiled before deny, so the warning names the deny list.
	// The verdict does not depend on that order.
	list, warnings = mustCompile(t, Options{
		Deny:  []string{"10.1.2.0/24"},
		Allow: []string{"10.1.2.0/24"},
	})
	require.Equal(t, []string{
		"ip acl: 10.1.2.0/24 is listed as both allow and deny; deny wins (deny)",
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.1")))
}

func TestLongestDuplicate(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Allow: []string{"10.1.2.3", "10.1.2.3/32", "all", "all"},
	})
	require.Equal(t, []string{
		"ip acl: duplicate entry 10.1.2.3/32 (allow)",
		"ip acl: duplicate entry 0.0.0.0/0 (allow)",
		"ip acl: duplicate entry ::/0 (allow)",
	}, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("192.0.2.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8::1")))
}

func TestHostBitsMasked(t *testing.T) {
	// clientip.ParseTrusted keeps p.Masked(), so host bits are not an error.
	// 10.1.2.3/24 is the prefix 10.1.2.0/24.
	list, warnings := mustCompile(t, Options{Allow: []string{"10.1.2.3/24"}})
	require.Empty(t, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.1.2.50")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.3.1")))
}

func TestLongestAllAndDefaults(t *testing.T) {
	// all matches every address. The configured default is still deny; it
	// is only used when nothing matches, which all makes impossible.
	list, warnings := mustCompile(t, Options{Allow: []string{"all"}})
	require.Empty(t, warnings)
	require.Equal(t, Deny, list.Default())
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("203.0.113.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("::1")))

	denied, warnings := mustCompile(t, Options{})
	require.Equal(t, []string{
		"ip acl: no entries and default deny; every address is denied",
	}, warnings)
	require.Equal(t, Deny, denied.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Deny, denied.Check(netip.MustParseAddr("2001:db8::1")))
	require.Equal(t, Deny, denied.Default())
	require.Equal(t, Reject, denied.Action())
	require.Equal(t, ClientIP, denied.Source())
	require.Equal(t, DefaultStatus, denied.Status())

	allowed, warnings := mustCompile(t, Options{Default: "allow"})
	require.Empty(t, warnings)
	require.Equal(t, Allow, allowed.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Allow, allowed.Default())
	// A missing address is still denied.
	require.Equal(t, Deny, allowed.Check(netip.Addr{}))
}

func TestMappedPrefixes(t *testing.T) {
	list, _ := mustCompile(t, Options{
		Allow: []string{"::ffff:10.0.0.0/104", "::ffff:192.0.2.10"},
	})
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("::ffff:10.255.0.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("192.0.2.10")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("11.0.0.1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8::1")))

	allV4, _ := mustCompile(t, Options{Allow: []string{"::ffff:0:0/96"}})
	require.Equal(t, Allow, allV4.Check(netip.MustParseAddr("8.8.8.8")))
	require.Equal(t, Allow, allV4.Check(netip.MustParseAddr("::ffff:1.2.3.4")))
	require.Equal(t, Deny, allV4.Check(netip.MustParseAddr("2001:db8::1")))

	// Shorter than /96, the mapped marker is not part of the network.
	// ::ffff:0:0/80 masks to ::/80 and does not match IPv4.
	wide, _ := mustCompile(t, Options{Allow: []string{"::ffff:0:0/80"}, Default: "deny"})
	require.Equal(t, Allow, wide.Check(netip.MustParseAddr("::1")))
	require.Equal(t, Deny, wide.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Deny, wide.Check(netip.MustParseAddr("::ffff:10.1.2.3")))

	// Check unmaps first, then matches one family. ::/0 is only the v6 prefix.
	// all is the entry defined as both 0.0.0.0/0 and ::/0. An unmapped v4
	// address is not inside ::/0, same as clientip.Contains.
	v6only, _ := mustCompile(t, Options{Allow: []string{"::/0"}})
	require.Equal(t, Allow, v6only.Check(netip.MustParseAddr("2001:db8::1")))
	require.Equal(t, Deny, v6only.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Deny, v6only.Check(netip.MustParseAddr("::ffff:10.1.2.3")))

	// Unmap drops the zone from an IPv4-mapped address, so it still matches v4.
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("::ffff:10.1.2.3%eth0")))
}

func TestZone(t *testing.T) {
	// A zoned entry is accepted. PrefixFrom strips the zone, so it is the
	// same prefix as the address without one, and a second copy is a duplicate.
	list, warnings := mustCompile(t, Options{
		Allow: []string{"fe80::1%eth0"},
	})
	require.Empty(t, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("fe80::1")))
	_, warnings = mustCompile(t, Options{Allow: []string{"fe80::1", "fe80::1%eth0"}})
	require.Equal(t, []string{
		"ip acl: duplicate entry fe80::1/128 (allow)",
	}, warnings)

	// A zoned address matches no prefix. The default applies, including when
	// that lets it past a deny of the same address without a zone.
	denied, _ := mustCompile(t, Options{Allow: []string{"fe80::1"}})
	require.Equal(t, Allow, denied.Check(netip.MustParseAddr("fe80::1")))
	require.Equal(t, Deny, denied.Check(netip.MustParseAddr("fe80::1%eth0")))

	allowed, _ := mustCompile(t, Options{
		Deny:    []string{"fe80::1"},
		Default: "allow",
	})
	require.Equal(t, Deny, allowed.Check(netip.MustParseAddr("fe80::1")))
	require.Equal(t, Allow, allowed.Check(netip.MustParseAddr("fe80::1%eth0")))
}

func TestOrderedFirstMatch(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Name:  "legacy",
		Match: "ordered",
		Rules: []Rule{
			{Deny: "10.1.2.0/24"},
			{Allow: "10.0.0.0/8"},
			{Allow: "10.1.2.9"},
			{Deny: "all"},
			{Allow: "192.0.2.1"},
		},
	})
	require.Equal(t, []string{
		`ip acl "legacy": unreachable rule "10.1.2.9" for 10.1.2.9/32 (rule 3 allow)`,
		`ip acl "legacy": unreachable rule "192.0.2.1" for 192.0.2.1/32 (rule 5 allow)`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.9.1.1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("11.0.0.1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("192.0.2.1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8::1")))

	// The more specific rule is reachable when it comes first, including
	// against a later all. Equal prefixes keep the first rule, so an
	// ordered allow is not turned into a deny by a later copy.
	list, warnings = mustCompile(t, Options{
		Match: "ordered",
		Rules: []Rule{
			{Allow: "10.1.2.0/24"},
			{Deny: "10.1.2.0/24"},
			{Deny: "all"},
		},
	})
	require.Equal(t, []string{
		`ip acl: unreachable rule "10.1.2.0/24" for 10.1.2.0/24 (rule 2 deny)`,
	}, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.1.2.9")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.9.9.9")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8::1")))
}

func TestOrderedIPv6(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Match: "ordered",
		Rules: []Rule{
			{Allow: "2001:db8:1:2::5"},
			{Deny: "2001:db8:1::/48"},
			{Allow: "2001:db8::/32"},
			{Deny: "2001:db8:1:2::5"},
		},
	})
	require.Equal(t, []string{
		`ip acl: unreachable rule "2001:db8:1:2::5" for 2001:db8:1:2::5/128 (rule 4 deny)`,
	}, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8:1:2::5")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8:1::1")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8:2::1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db9::1")))
}

func TestOrderedAllCoversLaterRules(t *testing.T) {
	list, warnings := mustCompile(t, Options{
		Match: "ordered",
		Rules: []Rule{
			{Deny: "all"},
			{Allow: "10.0.0.0/8"},
			{Allow: "2001:db8::/32"},
		},
	})
	require.Equal(t, []string{
		`ip acl: unreachable rule "10.0.0.0/8" for 10.0.0.0/8 (rule 2 allow)`,
		`ip acl: unreachable rule "2001:db8::/32" for 2001:db8::/32 (rule 3 allow)`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8::1")))
}

func TestOrderedPartialAll(t *testing.T) {
	// A v4 deny does not cover the v6 half of a later all.
	list, warnings := mustCompile(t, Options{
		Match: "ordered",
		Rules: []Rule{
			{Deny: "0.0.0.0/0"},
			{Allow: "all"},
		},
	})
	require.Equal(t, []string{
		`ip acl: unreachable rule "all" for 0.0.0.0/0 (rule 2 allow)`,
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("2001:db8::1")))
}

func TestCompileDoesNotRetainInput(t *testing.T) {
	allow := []string{"10.0.0.0/8"}
	list, _ := mustCompile(t, Options{Allow: allow})
	allow[0] = "11.0.0.0/8"
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.1.2.3")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("11.1.2.3")))
}

func TestAccessors(t *testing.T) {
	list, _ := mustCompile(t, Options{
		Source: "peer",
		Action: "drop",
		Status: 451,
		Allow:  []string{"10.0.0.0/8"},
	})
	require.Equal(t, Peer, list.Source())
	require.Equal(t, Drop, list.Action())
	require.Equal(t, 451, list.Status())
	require.Equal(t, "peer", list.Source().String())
	require.Equal(t, "drop", list.Action().String())

	trimmed, _ := mustCompile(t, Options{
		Match:   " longest ",
		Default: " allow ",
		Source:  " client_ip ",
		Action:  " reject ",
		Allow:   []string{"  10.0.0.0/8  ", ""},
	})
	require.Equal(t, Allow, trimmed.Check(netip.MustParseAddr("10.1.1.1")))
	require.Equal(t, Allow, trimmed.Check(netip.MustParseAddr("11.0.0.1")))
	require.Equal(t, ClientIP, trimmed.Source())
	require.Equal(t, Reject, trimmed.Action())
	require.Equal(t, DefaultStatus, trimmed.Status())
}

func TestValidate(t *testing.T) {
	checks := []struct {
		name string
		o    Options
		err  error
	}{
		{"match", Options{Match: "first", Allow: []string{"10.0.0.0/8"}}, ErrInvalidMatch},
		{"ordered lists", Options{Match: "ordered", Allow: []string{"10.0.0.0/8"}}, ErrInvalidMatch},
		{"ordered file", Options{Match: "ordered", DenyFile: "x"}, ErrInvalidMatch},
		{"rules on longest", Options{Rules: []Rule{{Allow: "10.0.0.0/8"}}}, ErrInvalidMatch},
		{"default", Options{Default: "accept", Allow: []string{"10.0.0.0/8"}}, ErrInvalidDefault},
		{"source", Options{Source: "header", Allow: []string{"10.0.0.0/8"}}, ErrInvalidSource},
		{"action", Options{Action: "DROP", Allow: []string{"10.0.0.0/8"}}, ErrInvalidAction},
		{"status low", Options{Status: 399, Allow: []string{"10.0.0.0/8"}}, ErrInvalidStatus},
		{"status high", Options{Status: 600, Allow: []string{"10.0.0.0/8"}}, ErrInvalidStatus},
		{"status 200", Options{Status: 200, Allow: []string{"10.0.0.0/8"}}, ErrInvalidStatus},
		{"empty rule", Options{Match: "ordered", Rules: []Rule{{}}}, ErrInvalidRule},
		{"blank rule", Options{Match: "ordered", Rules: []Rule{{Allow: "  "}}}, ErrInvalidRule},
		{
			"two actions",
			Options{Match: "ordered", Rules: []Rule{{Allow: "10.0.0.0/8", Deny: "10.1.2.0/24"}}},
			ErrInvalidRule,
		},
		{"bad entry", Options{Allow: []string{"ALL"}}, ErrInvalidEntry},
		{"bad cidr", Options{Allow: []string{"10.0.0.0/33"}}, ErrInvalidEntry},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			list, warnings, err := Compile(tc.o)
			require.ErrorIs(t, err, tc.err)
			require.Nil(t, list)
			require.Nil(t, warnings)
		})
	}

	list, _ := mustCompile(t, Options{Status: 400, Allow: []string{"10.0.0.0/8"}})
	require.Equal(t, 400, list.Status())
	list, _ = mustCompile(t, Options{Status: 599, Allow: []string{"10.0.0.0/8"}})
	require.Equal(t, 599, list.Status())

	_, _, err := Compile(Options{
		Match: "ordered",
		Rules: []Rule{{Allow: "10.0.0.0/8"}, {}},
	})
	require.ErrorIs(t, err, ErrInvalidRule)
	require.ErrorContains(t, err, "rule 2")

	_, _, err = Compile(Options{Match: "ordered", Rules: []Rule{{Allow: "nope"}}})
	require.ErrorIs(t, err, ErrInvalidEntry)
	_, _, err = Compile(Options{Match: "ordered", Rules: []Rule{{Deny: "nope"}}})
	require.ErrorIs(t, err, ErrInvalidEntry)
	_, _, err = Compile(Options{Match: "ordered", Rules: []Rule{{DenyFile: "no-such-acl-file"}}})
	require.ErrorIs(t, err, ErrInvalidFile)
}

func TestOrderedEmptyRules(t *testing.T) {
	// No rules means no entries. That is the empty-list warning, not an error.
	list, warnings := mustCompile(t, Options{Match: "ordered"})
	require.Equal(t, []string{
		"ip acl: no entries and default deny; every address is denied",
	}, warnings)
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("10.0.0.1")))
	require.Equal(t, Deny, list.Check(netip.MustParseAddr("2001:db8::1")))

	list, warnings = mustCompile(t, Options{Match: "ordered", Default: "allow", Rules: []Rule{}})
	require.Empty(t, warnings)
	require.Equal(t, Allow, list.Check(netip.MustParseAddr("10.0.0.1")))
}

func TestStringers(t *testing.T) {
	require.Equal(t, "allow", Allow.String())
	require.Equal(t, "deny", Deny.String())
	require.Equal(t, "verdict(9)", Verdict(9).String())
	require.Equal(t, "reject", Reject.String())
	require.Equal(t, "drop", Drop.String())
	require.Equal(t, "action(9)", Action(9).String())
	require.Equal(t, "client_ip", ClientIP.String())
	require.Equal(t, "peer", Peer.String())
	require.Equal(t, "source(9)", Source(9).String())
}

func TestCheckConcurrent(t *testing.T) {
	list, _ := mustCompile(t, Options{
		Allow: []string{"10.0.0.0/8", "2001:db8::/32"},
		Deny:  []string{"10.1.2.0/24"},
	})
	addrs := []netip.Addr{
		netip.MustParseAddr("10.9.9.9"),
		netip.MustParseAddr("10.1.2.1"),
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("2001:db9::1"),
		netip.MustParseAddr("::ffff:10.1.2.1"),
		{},
	}
	want := []Verdict{Allow, Deny, Allow, Deny, Deny, Deny}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				for i := range addrs {
					if got := list.Check(addrs[i]); got != want[i] {
						t.Errorf("Check(%v) = %s, want %s", addrs[i], got, want[i])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

func TestCheckAllocs(t *testing.T) {
	allow := make([]string, 0, 33)
	for bits := range 33 {
		allow = append(allow, netip.PrefixFrom(netip.MustParseAddr("10.0.0.0"), bits).Masked().String())
	}
	v6 := make([]string, 0, 8)
	for bits := 1; bits <= 128; bits += 16 {
		v6 = append(v6, netip.PrefixFrom(netip.MustParseAddr("2001:db8::"), bits).Masked().String())
	}
	list, _ := mustCompile(t, Options{Allow: append(allow, v6...)})

	block := make([]string, 1000)
	for i := range block {
		block[i] = netip.AddrFrom4([4]byte{10, byte(i >> 8), byte(i), 1}).String()
	}
	blockList, _ := mustCompile(t, Options{Allow: block})

	checks := []struct {
		name string
		list *List
		addr netip.Addr
	}{
		{"v4 full walk", list, netip.MustParseAddr("11.0.0.1")},
		{"v4 hit", list, netip.MustParseAddr("10.1.2.3")},
		{"v6", list, netip.MustParseAddr("2001:db8::1")},
		{"mapped", list, netip.MustParseAddr("::ffff:10.1.2.3")},
		{"invalid", list, netip.Addr{}},
		{"zoned", list, netip.MustParseAddr("fe80::1%eth0")},
		{"block hit", blockList, netip.MustParseAddr("10.0.1.1")},
		{"block miss", blockList, netip.MustParseAddr("11.0.0.1")},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(200, func() {
				tc.list.Check(tc.addr)
			})
			if allocs != 0 {
				t.Fatalf("Check allocated %v times, want 0", allocs)
			}
		})
	}
}
