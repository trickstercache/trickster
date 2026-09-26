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
package pick

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

func keyFuncFor(t *testing.T, source string) keyFunc {
	t.Helper()
	ks, err := options.ParseKeySource(source)
	if err != nil {
		t.Fatal(err)
	}
	return newKeyFunc(ks, options.DefaultIPv6Prefix)
}

func request(target string, mutate func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestClientIPKey(t *testing.T) {
	key := keyFuncFor(t, "client_ip")
	from := func(remote, resolved string) lb.Flow {
		r := request("http://example.com/", func(r *http.Request) { r.RemoteAddr = remote })
		if resolved != "" {
			r = r.WithContext(tctx.WithClientIP(r.Context(), resolved))
		}
		return key(r)
	}
	a := from("192.0.2.1:50000", "")
	if !a.HasKey || a != from("192.0.2.1:61234", "") {
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
	if odd := from("@", "not-an-ip"); !odd.HasKey || odd != from("@", "not-an-ip") {
		t.Error("an unparsable resolved address did not key consistently")
	}
	if odd := from("pipe", ""); !odd.HasKey {
		t.Error("an unparsable peer address did not key")
	}
	if from("", "").HasKey {
		t.Error("no address at all produced a key")
	}
}

func TestHostKey(t *testing.T) {
	key := keyFuncFor(t, "host")
	of := func(host string) lb.Flow {
		return key(request("http://example.com/", func(r *http.Request) { r.Host = host }))
	}
	if !of("api.example.com").HasKey || of("api.example.com") != of("API.Example.COM:8443") {
		t.Error("case or port changed a host's key")
	}
	if of("api.example.com") == of("www.example.com") {
		t.Error("two hosts share a key")
	}
	if of("[2001:db8::1]:8080") != of("[2001:db8::1]") {
		t.Error("the port of an IPv6 literal changed its key")
	}
	if of("").HasKey {
		t.Error("an empty host produced a key")
	}
}

func TestHeaderCookieAndQueryKeys(t *testing.T) {
	header := keyFuncFor(t, "header:x-tenant")
	with := func(v ...string) *http.Request {
		return request("http://example.com/", func(r *http.Request) { r.Header["X-Tenant"] = v })
	}
	if a := header(with("acme")); !a.HasKey || a != header(with("acme", "other")) || a == header(with("globex")) {
		t.Error("the header key does not follow the first header value")
	}
	if header(with()).HasKey || header(with("")).HasKey {
		t.Error("a missing or empty header produced a key")
	}

	cookie := keyFuncFor(t, "cookie:session")
	jar := func(lines ...string) *http.Request {
		return request("http://example.com/", func(r *http.Request) { r.Header["Cookie"] = lines })
	}
	want := lb.Flow{Key: lb.HashString("abc123"), HasKey: true}
	for _, lines := range [][]string{
		{"session=abc123"}, {"theme=dark; session=abc123"}, {"theme=dark;session=abc123 ; x=1"},
		{"theme=dark", "session=abc123"}, {`session="abc123"`}, {"xsession=no; session=abc123"},
	} {
		if got := cookie(jar(lines...)); got != want {
			t.Errorf("cookies %q keyed %+v", lines, got)
		}
	}
	for _, lines := range [][]string{nil, {"theme=dark"}, {"session="}, {"sessionid=abc123"}, {"session"}} {
		if cookie(jar(lines...)).HasKey {
			t.Errorf("cookies %q produced a key", lines)
		}
	}

	query := keyFuncFor(t, "query:tenant")
	if got := query(request("http://example.com/q?a=1&tenant=acme&b=2", nil)); got != (lb.Flow{Key: lb.HashString("acme"), HasKey: true}) {
		t.Errorf("query key = %+v", got)
	}
	for _, target := range []string{"http://example.com/q", "http://example.com/q?tenant=", "http://example.com/q?subtenant=acme"} {
		if query(request(target, nil)).HasKey {
			t.Errorf("%s produced a key", target)
		}
	}
	if query(&http.Request{}).HasKey {
		t.Error("a request with no URL produced a key")
	}
}

func TestKeyExtractionDoesNotAllocate(t *testing.T) {
	r := request("http://example.com/q?a=1&tenant=acme", func(r *http.Request) {
		r.RemoteAddr = "[2001:db8::7]:4431"
		r.Host = "API.example.com:8443"
		r.Header["X-Tenant"] = []string{"acme"}
		r.Header["Cookie"] = []string{"theme=dark; session=abc123"}
	})
	for _, source := range []string{"client_ip", "host", "header:X-Tenant", "cookie:session", "query:tenant"} {
		key := keyFuncFor(t, source)
		if allocs := testing.AllocsPerRun(200, func() { _ = key(r) }); allocs != 0 {
			t.Errorf("%s allocates %v per request", source, allocs)
		}
	}
}

func FuzzKeyExtraction(f *testing.F) {
	f.Add("10.0.0.1:1", "example.com", "a=1; session=x", "tenant=acme&x")
	f.Add("[::1]:80", "[::1]:8080", ";;==;", "&&==&")
	f.Add("", "", "", "")
	f.Fuzz(func(t *testing.T, remote, host, cookies, rawQuery string) {
		r := &http.Request{RemoteAddr: remote, Host: host, URL: &url.URL{RawQuery: rawQuery}, Header: http.Header{
			"Cookie": {cookies}, "X-Tenant": {cookies},
		}}
		for _, source := range []string{"client_ip", "host", "header:X-Tenant", "cookie:session", "query:tenant"} {
			ks, err := options.ParseKeySource(source)
			if err != nil {
				t.Fatal(err)
			}
			key := newKeyFunc(ks, 64)
			if a, b := key(r), key(r); a != b {
				t.Errorf("%s keyed one request two ways", source)
			}
		}
	})
}
