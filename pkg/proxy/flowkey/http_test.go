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
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

func sourcesOf(t *testing.T, specs ...string) []KeySource {
	t.Helper()
	out := make([]KeySource, len(specs))
	for i, s := range specs {
		ks, err := ParseKeySource(s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = ks
	}
	return out
}

func httpFor(t *testing.T, source string) func(*http.Request) Value {
	t.Helper()
	return HTTP(sourcesOf(t, source)[0], DefaultIPv6Prefix)
}

func request(target string, mutate func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestClientIPValue(t *testing.T) {
	key := httpFor(t, "client_ip")
	from := func(remote, resolved string) Value {
		r := request("http://example.com/", func(r *http.Request) { r.RemoteAddr = remote })
		if resolved != "" {
			r = r.WithContext(tctx.WithClientIP(r.Context(), resolved))
		}
		return key(r)
	}
	a := from("192.0.2.1:50000", "")
	if !a.OK || a != from("192.0.2.1:61234", "") {
		t.Error("the client's ephemeral port changed its key")
	}
	if a == from("192.0.2.2:50000", "") {
		t.Error("two clients share a key")
	}
	// the address resolved from a trusted proxy wins over the proxy's own
	if from("10.0.0.9:443", "192.0.2.1") != a {
		t.Error("the resolved client address was not used")
	}
	if from("192.0.2.1", "") != a {
		t.Error("a peer address without a port keyed differently")
	}
	// IPv6 clients key on their /64: privacy addresses rotate the rest
	v6 := from("[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443", "")
	if v6 != from("[2001:db8:1:2:1111:2222:3333:4444]:443", "") {
		t.Error("two addresses in one /64 keyed differently")
	}
	if v6 == from("[2001:db8:1:3:aaaa:bbbb:cccc:dddd]:443", "") {
		t.Error("two /64s share a key")
	}
	// something unparsable still keys, consistently; nothing at all does not
	if odd := from("@", "not-an-ip"); !odd.OK || odd != from("@", "not-an-ip") {
		t.Error("an unparsable resolved address did not key consistently")
	}
	if odd := from("pipe", ""); !odd.OK {
		t.Error("an unparsable peer address did not key")
	}
	if from("", "").OK {
		t.Error("no address at all produced a key")
	}
}

func TestHostValue(t *testing.T) {
	key := httpFor(t, "host")
	of := func(host string) Value {
		return key(request("http://example.com/", func(r *http.Request) { r.Host = host }))
	}
	if !of("api.example.com").OK || of("api.example.com") != of("API.Example.COM:8443") {
		t.Error("case or port changed a host's key")
	}
	if of("api.example.com") == of("www.example.com") {
		t.Error("two hosts share a key")
	}
	if of("[2001:db8::1]:8080") != of("[2001:db8::1]") {
		t.Error("the port of an IPv6 literal changed its key")
	}
	if of("").OK {
		t.Error("an empty host produced a key")
	}
}

func TestHeaderCookieAndQueryValues(t *testing.T) {
	header := httpFor(t, "header:x-tenant")
	with := func(v ...string) *http.Request {
		return request("http://example.com/", func(r *http.Request) { r.Header["X-Tenant"] = v })
	}
	if a := header(with("acme")); !a.OK || a != header(with("acme", "other")) || a == header(with("globex")) {
		t.Error("the header key does not follow the first header value")
	}
	if header(with()).OK || header(with("")).OK {
		t.Error("a missing or empty header produced a key")
	}

	cookie := httpFor(t, "cookie:session")
	jar := func(lines ...string) *http.Request {
		return request("http://example.com/", func(r *http.Request) { r.Header["Cookie"] = lines })
	}
	want := Value{Hash: lb.HashString("abc123"), OK: true}
	for _, lines := range [][]string{
		{"session=abc123"}, {"theme=dark; session=abc123"}, {"theme=dark;session=abc123 ; x=1"},
		{"theme=dark", "session=abc123"}, {`session="abc123"`}, {"xsession=no; session=abc123"},
	} {
		if got := cookie(jar(lines...)); got != want {
			t.Errorf("cookies %q keyed %+v", lines, got)
		}
	}
	for _, lines := range [][]string{nil, {"theme=dark"}, {"session="}, {"sessionid=abc123"}, {"session"}} {
		if cookie(jar(lines...)).OK {
			t.Errorf("cookies %q produced a key", lines)
		}
	}

	query := httpFor(t, "query:tenant")
	if got := query(request("http://example.com/q?a=1&tenant=acme&b=2", nil)); got != (Value{Hash: lb.HashString("acme"), OK: true}) {
		t.Errorf("query key = %+v", got)
	}
	for _, target := range []string{"http://example.com/q", "http://example.com/q?tenant=", "http://example.com/q?subtenant=acme"} {
		if query(request(target, nil)).OK {
			t.Errorf("%s produced a key", target)
		}
	}
	if query(&http.Request{}).OK {
		t.Error("a request with no URL produced a key")
	}
}

func TestMethodPathAndRawQueryValues(t *testing.T) {
	r := request("http://example.com/api/v1/items?tenant=acme&x=1", nil)
	method := httpFor(t, "method")
	if got := method(r); got != valueOf(http.MethodGet) {
		t.Errorf("method key = %+v", got)
	}
	if method(&http.Request{}).OK {
		t.Error("an empty method produced a key")
	}

	path := httpFor(t, "path")
	if got := path(r); got != valueOf("/api/v1/items") {
		t.Errorf("path key = %+v", got)
	}
	// the path is keyed as decoded, so two spellings of one path key alike, and never with its query
	if path(request("http://example.com/api/v1/items%2Fx?a=1", nil)) != path(request("http://example.com/api/v1/items/x?b=2", nil)) {
		t.Error("two spellings of one path keyed differently")
	}
	if path(request("http://example.com", nil)).OK || path(&http.Request{}).OK {
		t.Error("a request with no path produced a key")
	}

	query := httpFor(t, "query")
	if got := query(r); got != valueOf("tenant=acme&x=1") {
		t.Errorf("query string key = %+v", got)
	}
	if query(request("http://example.com/api", nil)).OK || query(&http.Request{}).OK {
		t.Error("a request with no query string produced a key")
	}
}

func TestUnreadableRequestSourcesAreNeverPresent(t *testing.T) {
	r := request("http://example.com/", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1" })
	for _, source := range []string{"sni", "proxy_tlv:0xEA", "user"} {
		if httpFor(t, source)(r).OK {
			t.Errorf("%s was read from a request", source)
		}
	}
}

func TestHTTPComposite(t *testing.T) {
	r := request("http://example.com/q?tenant=acme", func(r *http.Request) {
		r.RemoteAddr = "192.0.2.1:1"
		r.Header["X-Tenant"] = []string{"acme"}
	})
	client := httpFor(t, "client_ip")(r).Hash
	both := HTTPComposite(sourcesOf(t, "client_ip", "header:X-Tenant"), DefaultIPv6Prefix)
	got := both(r)
	if !got.OK || got.Present != 2 {
		t.Fatalf("composite = %+v", got)
	}
	// the parts fold in order, so swapping them is a different key
	if want := lb.Mix(lb.Mix(client) ^ lb.HashString("acme")); got.Hash != want {
		t.Errorf("composite hash = %#x, want %#x", got.Hash, want)
	}
	if swapped := HTTPComposite(sourcesOf(t, "header:X-Tenant", "client_ip"), DefaultIPv6Prefix)(r); swapped.Hash == got.Hash {
		t.Error("the order of the parts did not matter")
	}
	// a part the request lacks folds as zero: requests lacking it share a key, and are not OK
	partial := HTTPComposite(sourcesOf(t, "client_ip", "cookie:session"), DefaultIPv6Prefix)
	a := partial(r)
	b := partial(request("http://example.com/elsewhere", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:2" }))
	if a.OK || a.Present != 1 || a != b {
		t.Errorf("partial composites = %+v and %+v", a, b)
	}
	if want := lb.Mix(lb.Mix(client)); a.Hash != want {
		t.Errorf("partial composite hash = %#x, want %#x", a.Hash, want)
	}
	// one part alone is still folded, so it is not the bare key of that part
	if one := HTTPComposite(sourcesOf(t, "header:X-Tenant"), DefaultIPv6Prefix)(r); !one.OK || one.Hash == lb.HashString("acme") {
		t.Errorf("single-part composite = %+v", one)
	}
	if none := HTTPComposite(nil, DefaultIPv6Prefix)(r); none.OK || none.Present != 0 {
		t.Errorf("empty composite = %+v", none)
	}
	if allocs := testing.AllocsPerRun(200, func() { _ = both(r) }); allocs != 0 {
		t.Errorf("a composite allocates %v per request", allocs)
	}
}

func TestHTTPExtractionDoesNotAllocate(t *testing.T) {
	r := request("http://example.com/q?a=1&tenant=acme", func(r *http.Request) {
		r.RemoteAddr = "[2001:db8::7]:4431"
		r.Host = "API.example.com:8443"
		r.Header["X-Tenant"] = []string{"acme"}
		r.Header["Cookie"] = []string{"theme=dark; session=abc123"}
	})
	for _, source := range []string{"client_ip", "host", "header:X-Tenant", "cookie:session", "query:tenant", "method", "path", "query"} {
		key := httpFor(t, source)
		if allocs := testing.AllocsPerRun(200, func() { _ = key(r) }); allocs != 0 {
			t.Errorf("%s allocates %v per request", source, allocs)
		}
	}
}

func FuzzHTTPExtraction(f *testing.F) {
	f.Add("10.0.0.1:1", "example.com", "a=1; session=x", "/p", "tenant=acme&x")
	f.Add("[::1]:80", "[::1]:8080", ";;==;", "", "&&==&")
	f.Add("", "", "", "", "")
	f.Fuzz(func(t *testing.T, remote, host, cookies, path, rawQuery string) {
		r := &http.Request{Method: http.MethodPost, RemoteAddr: remote, Host: host, URL: &url.URL{Path: path, RawQuery: rawQuery}, Header: http.Header{
			"Cookie": {cookies}, "X-Tenant": {cookies},
		}}
		for _, source := range []string{"client_ip", "host", "header:X-Tenant", "cookie:session", "query:tenant", "method", "path", "query"} {
			ks, err := ParseKeySource(source)
			if err != nil {
				t.Fatal(err)
			}
			key := HTTP(ks, 64)
			if a, b := key(r), key(r); a != b {
				t.Errorf("%s keyed one request two ways", source)
			}
		}
	})
}

// a composite keeps the sources it was built with, whatever the caller then does with its slice
func TestHTTPCompositeOwnsItsSources(t *testing.T) {
	r := request("http://example.com/", func(r *http.Request) {
		r.RemoteAddr = "192.0.2.1:1"
		r.Header["X-Tenant"] = []string{"acme"}
	})
	sources := sourcesOf(t, "client_ip", "header:X-Tenant")
	both := HTTPComposite(sources, DefaultIPv6Prefix)
	want := both(r)
	sources[1] = sourcesOf(t, "cookie:session")[0]
	if got := both(r); got != want {
		t.Errorf("composite changed from %+v to %+v after its sources were modified", want, got)
	}
}
