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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

func responseFor(t *testing.T, source string) func(http.Header) Value {
	t.Helper()
	return HTTPResponse(sourcesOf(t, source)[0])
}

func TestResponseHeaderValue(t *testing.T) {
	key := responseFor(t, "header:mcp-session-id")
	with := func(v ...string) http.Header { return http.Header{"Mcp-Session-Id": v} }
	if a := key(with("s1")); !a.OK || a != key(with("s1", "s2")) || a == key(with("s2")) {
		t.Error("the header key does not follow the first header value")
	}
	if key(with()).OK || key(with("")).OK || key(http.Header{}).OK {
		t.Error("a missing or empty header produced a key")
	}
}

func TestResponseCookieValue(t *testing.T) {
	key := responseFor(t, "cookie:session")
	set := func(lines ...string) Value { return key(http.Header{"Set-Cookie": lines}) }
	want := Value{Hash: lb.HashString("abc123"), OK: true}
	for _, lines := range [][]string{
		{"session=abc123"},
		{"session=abc123; Path=/; HttpOnly; Secure"},
		{`session="abc123"; Path=/`},
		{" session = abc123 ;Path=/"},
		{"theme=dark; Path=/", "session=abc123"},
		{"session=old", "session=abc123; Max-Age=60"},
		{"session=abc123; Max-Age=0; Max-Age=60"},
		{"session=abc123; Max-Age=soon"},
		{"xsession=no", "session=abc123", "sessionid=no"},
	} {
		if got := set(lines...); got != want {
			t.Errorf("Set-Cookie %q keyed %+v", lines, got)
		}
	}
	for _, lines := range [][]string{
		nil,
		{"theme=dark"},
		{"session="},
		{"session"},
		{"sessionid=abc123"},
		{"Session=abc123"},
		{"session=abc123; Max-Age=0"},
		{"session=abc123; max-age=-1; Path=/"},
		{"session=abc123", "session=; Expires=Thu, 01 Jan 1970 00:00:00 GMT"},
		{"session=abc123", "session=abc123; Max-Age=0"},
	} {
		if got := set(lines...); got.OK {
			t.Errorf("Set-Cookie %q produced a key", lines)
		}
	}
}

// A value learned from a response is found again when the client sends it back: the two
// sides must hash it alike, whatever quoting or spacing the upstream wrote it with.
func TestResponseKeyMatchesTheRequestThatReturnsIt(t *testing.T) {
	for _, tc := range []struct{ source, response, request string }{
		{"cookie:session", "session=abc123; Path=/; HttpOnly", "theme=dark; session=abc123"},
		{"cookie:session", `session="abc123"; Path=/`, `session="abc123"`},
		{"cookie:session", " session =  abc123 ; Secure", "session=abc123"},
		{"header:Mcp-Session-Id", "1868a90c-4f3b-4e7b", "1868a90c-4f3b-4e7b"},
	} {
		ks := sourcesOf(t, tc.source)[0]
		name := "Mcp-Session-Id"
		h, sent := http.Header{name: {tc.response}}, request("http://example.com/", nil)
		sent.Header[name] = []string{tc.request}
		if ks.Kind == KeyCookie {
			h = http.Header{"Set-Cookie": {tc.response}}
			sent.Header = http.Header{"Cookie": {tc.request}}
		}
		learned, found := HTTPResponse(ks)(h), HTTP(ks, DefaultIPv6Prefix)(sent)
		if !learned.OK || learned != found {
			t.Errorf("%s: learned %+v from %q, found %+v in %q", tc.source, learned, tc.response, found, tc.request)
		}
	}
}

func TestUnreadableResponseSourcesAreNeverPresent(t *testing.T) {
	h := http.Header{"Set-Cookie": {"session=abc123"}, "Host": {"example.com"}, "X-Forwarded-For": {"192.0.2.1"}}
	for _, source := range []string{"client_ip", "host", "query:session", "sni", "proxy_tlv:0xEA", "user", "method", "path", "query"} {
		ks := sourcesOf(t, source)[0]
		if ks.OnHTTPResponse() {
			t.Errorf("%s claims to be readable from a response", source)
		}
		if HTTPResponse(ks)(h).OK {
			t.Errorf("%s was read from a response", source)
		}
	}
}

func TestResponseExtractionDoesNotAllocate(t *testing.T) {
	h := http.Header{
		"Mcp-Session-Id": {"1868a90c"},
		"Set-Cookie":     {"theme=dark; Path=/", "session=abc123; Path=/; Max-Age=3600; HttpOnly"},
	}
	for _, source := range []string{"header:Mcp-Session-Id", "cookie:session"} {
		key := responseFor(t, source)
		if allocs := testing.AllocsPerRun(200, func() { _ = key(h) }); allocs != 0 {
			t.Errorf("%s allocates %v per response", source, allocs)
		}
	}
}

// isCookieOctet reports whether b may appear in a cookie value that a browser sends back as set
func isCookieOctet(b byte) bool {
	return b == 0x21 || (b >= 0x23 && b <= 0x2B) || (b >= 0x2D && b <= 0x3A) ||
		(b >= 0x3C && b <= 0x5B) || (b >= 0x5D && b <= 0x7E)
}

func FuzzResponseKeyMatchesRequest(f *testing.F) {
	f.Add("abc123", "; Path=/")
	f.Add("a", "; Max-Age=10; Secure")
	f.Add("x=y", "")
	f.Fuzz(func(t *testing.T, value, attrs string) {
		// the value ends at the first semicolon, where the attributes begin
		if value == "" || (attrs != "" && attrs[0] != ';') {
			return
		}
		for i := range len(value) {
			if !isCookieOctet(value[i]) {
				return
			}
		}
		ks := sourcesOf(t, "cookie:session")[0]
		learned := HTTPResponse(ks)(http.Header{"Set-Cookie": {"session=" + value + attrs}})
		if !learned.OK {
			// only an attribute that expires the cookie can take the value away
			if !deletesCookie(attrs) {
				t.Errorf("session=%q%s set no key", value, attrs)
			}
			return
		}
		sent := request("http://example.com/", func(r *http.Request) { r.Header["Cookie"] = []string{"session=" + value} })
		if found := HTTP(ks, DefaultIPv6Prefix)(sent); found != learned {
			t.Errorf("session=%q: learned %+v, found %+v", value, learned, found)
		}
	})
}
