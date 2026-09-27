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

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

// Every kind of key is pinned to its hash: replicas behind one front door must agree on where
// a key lands, and a later change to an extractor or to the hash must not move it.
func TestGoldenKeys(t *testing.T) {
	r := request("http://example.com/q?a=1&tenant=acme&b=2", func(r *http.Request) {
		r.RemoteAddr = "192.0.2.1:50000"
		r.Host = "API.Example.COM:8443"
		r.Header["X-Tenant"] = []string{"acme"}
		r.Header["Cookie"] = []string{"theme=dark; session=abc123"}
	})
	for source, want := range map[string]uint64{
		"client_ip":       0x1e00e9a12f8249cb,
		"host":            0xfcbbe0defd3b8b64,
		"header:X-Tenant": 0x130a76222d4427ef,
		"cookie:session":  0xa3d3d132cee652ad,
		"query:tenant":    0x130a76222d4427ef,
		"method":          0xbc92c6e893bba505,
		"path":            0xb1783a77d2c5fda8,
		"query":           0x6b25dfcc83662d74,
	} {
		if got := httpFor(t, source)(r); !got.OK || got.Hash != want {
			t.Errorf("request %s = %#x (%v), want %#x", source, got.Hash, got.OK, want)
		}
	}
	client := httpFor(t, "client_ip")
	v6 := request("http://example.com/", func(r *http.Request) { r.RemoteAddr = "[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443" })
	if got := client(v6); got.Hash != 0x136e302507a26a64 {
		t.Errorf("request client_ip of an IPv6 client = %#x", got.Hash)
	}
	resolved := r.WithContext(tctx.WithClientIP(r.Context(), "198.51.100.7"))
	if got := client(resolved); got.Hash != 0x3303e9606b4ef5a4 {
		t.Errorf("request client_ip resolved through a trusted proxy = %#x", got.Hash)
	}

	// a value learned from a response keys as the request that sends it back does
	resp := http.Header{"X-Tenant": {"acme"}, "Set-Cookie": {"theme=dark; Path=/", "session=abc123; Path=/; HttpOnly"}}
	for source, want := range map[string]uint64{
		"header:X-Tenant": 0x130a76222d4427ef,
		"cookie:session":  0xa3d3d132cee652ad,
	} {
		if got := HTTPResponse(sourcesOf(t, source)[0])(resp); !got.OK || got.Hash != want {
			t.Errorf("response %s = %#x (%v), want %#x", source, got.Hash, got.OK, want)
		}
	}

	f := flowOf("192.0.2.1:50000", "Shop.Example.COM")
	f.Proxy = tlvs{0xEA: []byte("vpce-1")}
	for source, want := range map[string]uint64{
		"client_ip":      0x1e00e9a12f8249cb,
		"sni":            0x25541bf29b108d5c,
		"proxy_tlv:0xEA": 0xb3484a99373963b8,
	} {
		if got := streamFor(t, source)(f); !got.OK || got.Hash != want {
			t.Errorf("flow %s = %#x (%v), want %#x", source, got.Hash, got.OK, want)
		}
	}
	f.Client = netip.MustParseAddrPort("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443")
	if got := streamFor(t, "client_ip")(f); got.Hash != 0x136e302507a26a64 {
		t.Errorf("flow client_ip of an IPv6 client = %#x", got.Hash)
	}

	// a native session keys as a request or flow from the same address does
	for source, want := range map[string]uint64{"user": 0xc5d1556d66774a5c, "client_ip": 0x1e00e9a12f8249cb} {
		if got := sessionFor(t, source)(sessionUser, netip.MustParseAddr("192.0.2.1")); !got.OK || got.Hash != want {
			t.Errorf("session %s = %#x (%v), want %#x", source, got.Hash, got.OK, want)
		}
	}
	if got := sessionFor(t, "client_ip")("", f.Client.Addr()); got.Hash != 0x136e302507a26a64 {
		t.Errorf("session client_ip of an IPv6 client = %#x", got.Hash)
	}
}
