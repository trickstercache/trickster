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

package l4

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestTableLookup(t *testing.T) {
	tbl := NewTable()
	if !tbl.Empty() {
		t.Fatal("new table is not empty")
	}
	exact, one, any, all := Static("exact"), Static("one"), Static("any"), Static("all")
	for host, up := range map[string]Upstream{
		"Shop.Example.com.": exact, "*.example.com": one, "**.wild.example.com": any, "": all,
	} {
		if err := tbl.Add(host, up); err != nil {
			t.Fatal(err)
		}
	}
	if tbl.Empty() {
		t.Fatal("populated table reports empty")
	}
	cases := map[string]Upstream{
		"shop.example.com":        exact,
		"SHOP.example.com.":       exact,
		"api.example.com":         one,
		"a.b.example.com":         all,
		"deep.wild.example.com":   any,
		"a.b.c.wild.example.com":  any,
		"other.org":               all,
		"":                        all,
		"wild.example.com":        one,
		"example.com":             all,
		"notexample.com":          all,
		"sub.notwild.example.com": all,
		"x.wild.example.com.":     any,
	}
	for host, want := range cases {
		if got := tbl.Lookup(host); got != want {
			t.Errorf("Lookup(%q) = %v, want %v", host, got, want)
		}
	}
	for _, host := range []string{"shop.example.com", "*.example.com", "**.wild.example.com"} {
		if err := tbl.Add(host, exact); !errors.Is(err, ErrDuplicateHost) {
			t.Errorf("Add(%q) again = %v, want ErrDuplicateHost", host, err)
		}
	}
	if err := tbl.Add("", exact); !errors.Is(err, ErrDuplicateCatchAll) {
		t.Errorf("second catch-all = %v", err)
	}
	if err := tbl.Add("bad host", exact); err == nil {
		t.Error("a hostname with whitespace was routed")
	}
	var nilTable *Table
	if nilTable.Lookup("x") != nil || !nilTable.Empty() {
		t.Error("nil table must route nothing")
	}
	strict := NewTable()
	_ = strict.Add("only.example.com", exact)
	if strict.Lookup("other.example.com") != nil || strict.Lookup("") != nil {
		t.Error("a table without a catch-all routed an unknown host")
	}
}

func TestStatic(t *testing.T) {
	route, ok := Static("h:1").Pick(Flow{})
	if !ok || route.Addr() != "h:1" {
		t.Fatalf("Static = %v, %v", route, ok)
	}
	// a fixed address has no other route to fall back to, and nothing to report to
	if !route.Final() {
		t.Error("a static route is not final")
	}
	route.Dialed(time.Millisecond, nil)
	route.FirstByte()
	route.Closed(nil)
	if again, _ := Static("h:1").Pick(Flow{}); again.Addr() != "h:1" {
		t.Errorf("second pick = %v", again)
	}
	// an address under the reserved .invalid domain can never resolve, so it is refused without
	// a lookup
	for _, addr := range []string{"unresolved.kgw.invalid:1", "x.INVALID.:9", "x.invalid"} {
		if !Refusing(addr) {
			t.Errorf("Refusing(%q) = false", addr)
		}
		if _, ok := Static(addr).Pick(Flow{}); ok {
			t.Errorf("Static(%q) dials", addr)
		}
	}
	if Refusing("invalid.example.com:1") || Refusing("10.0.0.1:1") {
		t.Error("a resolvable address is refused")
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = Static("h:1").Pick(Flow{}) }); allocs > 1 {
		t.Errorf("a static pick allocates %v", allocs)
	}
	fixed := Static("h:1")
	if allocs := testing.AllocsPerRun(100, func() { _, _ = fixed.Pick(Flow{}) }); allocs != 0 {
		t.Errorf("a pick from a built static upstream allocates %v", allocs)
	}
}

func TestFlowOf(t *testing.T) {
	tcp := flowOf("l", ProtocolTLS, &net.TCPAddr{IP: net.ParseIP("192.0.2.7"), Port: 4431}, "shop.example.com")
	if tcp.Protocol != ProtocolTLS || tcp.ServerName != "shop.example.com" ||
		tcp.Client != netip.MustParseAddrPort("192.0.2.7:4431") {
		t.Errorf("tcp flow = %+v", tcp)
	}
	udp := flowOf("l", ProtocolUDP, &net.UDPAddr{IP: net.ParseIP("2001:db8::9"), Port: 53}, "")
	if udp.Client != netip.MustParseAddrPort("[2001:db8::9]:53") {
		t.Errorf("udp flow = %+v", udp)
	}
	// an IPv4 peer of a dual-stack socket arrives mapped into IPv6; it is one client either way
	mapped := flowOf("l", ProtocolTCP, &net.TCPAddr{IP: net.ParseIP("::ffff:192.0.2.7"), Port: 80}, "")
	if mapped.Client.Addr() != netip.MustParseAddr("192.0.2.7") {
		t.Errorf("mapped client = %v", mapped.Client)
	}
	// any other address is read from its text; one that is not an address leaves no client
	if other := flowOf("l", ProtocolTCP, textAddr("198.51.100.4:9"), ""); other.Client != netip.MustParseAddrPort("198.51.100.4:9") {
		t.Errorf("text address = %+v", other)
	}
	if flowOf("l", ProtocolTCP, textAddr("pipe"), "").Client.IsValid() || flowOf("l", ProtocolTCP, nil, "").Client.IsValid() {
		t.Error("an unusable peer address produced a client")
	}
}

type textAddr string

func (textAddr) Network() string { return "test" }

func (a textAddr) String() string { return string(a) }
