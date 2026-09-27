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
package flowkey

import (
	"net/http"
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/flow"
)

type tlvs map[byte][]byte

func (h tlvs) ProxyTLV(typ byte) ([]byte, bool) {
	v, ok := h[typ]
	return v, ok
}

func flowOf(client, serverName string) flow.Flow {
	f := flow.Flow{Listener: "test", ServerName: serverName}
	if client != "" {
		f.Client = netip.MustParseAddrPort(client)
	}
	return f
}

func streamFor(t *testing.T, source string) func(flow.Flow) Value {
	t.Helper()
	return Stream(sourcesOf(t, source)[0], DefaultIPv6Prefix)
}

func TestStreamClientIPValue(t *testing.T) {
	key := streamFor(t, "client_ip")
	a := key(flowOf("192.0.2.1:50000", ""))
	if !a.OK || a != key(flowOf("192.0.2.1:61234", "")) {
		t.Error("the client's ephemeral port changed its key")
	}
	if a == key(flowOf("192.0.2.2:50000", "")) {
		t.Error("two clients share a key")
	}
	v6 := key(flowOf("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443", ""))
	if !v6.OK || v6 != key(flowOf("[2001:db8:1:2:1111:2222:3333:4444]:443", "")) {
		t.Error("two addresses in one /64 keyed differently")
	}
	if v6 == key(flowOf("[2001:db8:1:3::1]:443", "")) {
		t.Error("two /64s share a key")
	}
	if key(flow.Flow{}).OK {
		t.Error("a flow with no client address produced a key")
	}
	// a request and a connection from one client land on one key
	r := request("http://example.com/", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:50000" })
	if httpFor(t, "client_ip")(r) != a {
		t.Error("the http and stream planes keyed one client differently")
	}
}

func TestStreamServerNameValue(t *testing.T) {
	key := streamFor(t, "sni")
	a := key(flowOf("192.0.2.1:1", "shop.example.com"))
	if !a.OK || a != key(flowOf("203.0.113.9:2", "Shop.Example.COM")) {
		t.Error("the client or the case of the name changed its key")
	}
	if a == key(flowOf("192.0.2.1:1", "api.example.com")) {
		t.Error("two server names share a key")
	}
	if key(flowOf("192.0.2.1:1", "")).OK {
		t.Error("a flow that offered no server name produced a key")
	}
}

func TestStreamProxyTLVValue(t *testing.T) {
	key := streamFor(t, "proxy_tlv:0xEA")
	with := func(h flow.ProxyHeader) flow.Flow {
		f := flowOf("192.0.2.1:1", "")
		f.Proxy = h
		return f
	}
	a := key(with(tlvs{0xEA: []byte("vpce-1"), 0x02: []byte("ignored")}))
	if !a.OK || a != key(with(tlvs{0xEA: []byte("vpce-1")})) {
		t.Error("another TLV changed the key")
	}
	if a == key(with(tlvs{0xEA: []byte("vpce-2")})) {
		t.Error("two TLV values share a key")
	}
	for name, h := range map[string]flow.ProxyHeader{"no": nil, "another": tlvs{0x02: []byte("x")}, "an empty": tlvs{0xEA: {}}} {
		if key(with(h)).OK {
			t.Errorf("a connection with %s TLV produced a key", name)
		}
	}
}

func TestUnreadableStreamSourcesAreNeverPresent(t *testing.T) {
	f := flowOf("192.0.2.1:1", "shop.example.com")
	f.Proxy = tlvs{0xEA: []byte("vpce-1")}
	for _, source := range []string{"host", "header:X-Tenant", "cookie:session", "query:tenant", "user", "method", "path", "query"} {
		if streamFor(t, source)(f).OK {
			t.Errorf("%s was read from a flow", source)
		}
	}
}

func TestStreamComposite(t *testing.T) {
	f := flowOf("192.0.2.1:1", "shop.example.com")
	client := streamFor(t, "client_ip")(f).Hash
	both := StreamComposite(sourcesOf(t, "client_ip", "sni"), DefaultIPv6Prefix)
	got := both(f)
	if !got.OK || got.Present != 2 {
		t.Fatalf("composite = %+v", got)
	}
	if want := lb.Mix(lb.Mix(client) ^ lb.HashFold("shop.example.com")); got.Hash != want {
		t.Errorf("composite hash = %#x, want %#x", got.Hash, want)
	}
	if swapped := StreamComposite(sourcesOf(t, "sni", "client_ip"), DefaultIPv6Prefix)(f); swapped.Hash == got.Hash {
		t.Error("the order of the parts did not matter")
	}
	if partial := both(flowOf("192.0.2.1:1", "")); partial.OK || partial.Present != 1 || partial.Hash != lb.Mix(lb.Mix(client)) {
		t.Errorf("partial composite = %+v", partial)
	}
	if none := StreamComposite(nil, DefaultIPv6Prefix)(f); none.OK || none.Present != 0 {
		t.Errorf("empty composite = %+v", none)
	}
	if allocs := testing.AllocsPerRun(200, func() { _ = both(f) }); allocs != 0 {
		t.Errorf("a composite allocates %v per flow", allocs)
	}
}

func TestStreamExtractionDoesNotAllocate(t *testing.T) {
	f := flowOf("[2001:db8::7]:4431", "shop.example.com")
	f.Proxy = tlvs{0xEA: []byte("vpce-1")}
	for _, source := range []string{"client_ip", "sni", "proxy_tlv:0xEA"} {
		key := streamFor(t, source)
		if allocs := testing.AllocsPerRun(200, func() { _ = key(f) }); allocs != 0 {
			t.Errorf("%s allocates %v per flow", source, allocs)
		}
	}
}

// a composite keeps the sources it was built with, whatever the caller then does with its slice
func TestStreamCompositeOwnsItsSources(t *testing.T) {
	f := flowOf("192.0.2.1:1", "shop.example.com")
	sources := sourcesOf(t, "client_ip", "sni")
	both := StreamComposite(sources, DefaultIPv6Prefix)
	want := both(f)
	sources[1] = sourcesOf(t, "proxy_tlv:0xEA")[0]
	if got := both(f); got != want {
		t.Errorf("composite changed from %+v to %+v after its sources were modified", want, got)
	}
}
