/*
 * Copyright 2018 The Trickster Authors
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

package redact

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const (
	testCleanQuery  = "query=SELECT+1&database=default&format=JSON"
	testCookieName  = "grafana_session"
	testValue       = "abc"
	testPasswordURI = "/?password=p"
)

func TestQuery(t *testing.T) {
	for _, unchanged := range []string{"", testCleanQuery, "password=&key"} {
		if got := Query(unchanged); got != unchanged {
			t.Errorf("Query(%q) = %q; want it unchanged", unchanged, got)
		}
	}
	tests := []struct{ in, want string }{
		{"user=u&password=p&query=SELECT+1", "user=u&password=" + Value + "&query=SELECT+1"},
		{"PASSWORD=p", "PASSWORD=" + Value},
		{"pass%77ord=p&x=1", "pass%77ord=" + Value + "&x=1"},
		{"a=1&token=t&sig=s", "a=1&token=" + Value + "&sig=" + Value},
		{"x=1&&code=c&", "x=1&&code=" + Value + "&"},
		{"client_secret=a=b", "client_secret=" + Value},
	}
	for _, tc := range tests {
		if got := Query(tc.in); got != tc.want {
			t.Errorf("Query(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestURI(t *testing.T) {
	const path = "/path"
	for _, unchanged := range []string{path, path + "?" + testCleanQuery} {
		if got := URI(unchanged); got != unchanged {
			t.Errorf("URI(%q) = %q; want it unchanged", unchanged, got)
		}
	}
	tests := []struct{ in, want string }{
		{testPasswordURI, "/?password=" + Value},
		{"https://example.com/cb?code=c&x=1#frag", "https://example.com/cb?code=" + Value + "&x=1#frag"},
	}
	for _, tc := range tests {
		if got := URI(tc.in); got != tc.want {
			t.Errorf("URI(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestURL(t *testing.T) {
	u, err := url.Parse("http://origin:secret@ch.example:8123/?user=u&password=p")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := URL(u), "http://origin:xxxxx@ch.example:8123/?user=u&password="+Value; got != want {
		t.Errorf("URL = %q; want %q", got, want)
	}
	if URL(nil) != "" {
		t.Error("nil URL must render empty")
	}
}

func TestError(t *testing.T) {
	const target = "http://ch.example/?password=p&x=1"
	err := fmt.Errorf("fetch: %w", &url.Error{Op: "Get", URL: target, Err: errors.New("refused")})
	if got, want := Error(err), `fetch: Get "http://ch.example/?password=`+Value+`&x=1": refused`; got != want {
		t.Errorf("Error = %q; want %q", got, want)
	}
	plain := errors.New("plain")
	if got := Error(plain); got != plain.Error() {
		t.Errorf("Error = %q", got)
	}
	if Error(nil) != "" {
		t.Error("nil error must render empty")
	}
}

func TestHeaderAndCookie(t *testing.T) {
	t.Cleanup(func() { Configure(nil) })
	if got := Header(headers.NameAuthorization, testValue); got != Value {
		t.Errorf("Authorization = %q", got)
	}
	if got := Header(headers.NameUserAgent, testValue); got != testValue {
		t.Errorf("User-Agent = %q", got)
	}
	if got := Header(headers.NameAuthorization, ""); got != "" {
		t.Errorf("an absent header must stay empty, got %q", got)
	}
	if got := Header(headers.NameReferer, "https://example.com/?token=t"); got != "https://example.com/?token="+Value {
		t.Errorf("Referer = %q", got)
	}
	if got := Cookie(testCookieName, testValue); got != testValue {
		t.Errorf("no cookie is redacted by default, got %q", got)
	}

	const header = "X-Internal-Token"
	Configure(&Options{
		QueryParams: []string{"Session_ID"}, Headers: []string{strings.ToLower(header)},
		Cookies: []string{testCookieName},
	})
	if got := Header(header, testValue); got != Value {
		t.Errorf("configured header = %q", got)
	}
	if got := Cookie(testCookieName, testValue); got != Value {
		t.Errorf("configured cookie = %q", got)
	}
	if got := Query("session_id=s&password=p"); got != "session_id="+Value+"&password="+Value {
		t.Errorf("configured query = %q", got)
	}

	disabled := false
	Configure(&Options{Enabled: &disabled, Cookies: []string{testCookieName}})
	if got := URI(testPasswordURI); got != testPasswordURI {
		t.Errorf("redaction disabled, but URI = %q", got)
	}
	if got := Header(headers.NameAuthorization, testValue); got != testValue {
		t.Errorf("redaction disabled, but Authorization = %q", got)
	}
	if got := Cookie(testCookieName, testValue); got != testValue {
		t.Errorf("redaction disabled, but cookie = %q", got)
	}
}

func TestOptionsClone(t *testing.T) {
	enabled := false
	o := &Options{Enabled: &enabled, QueryParams: []string{"a"}, Headers: []string{"b"}, Cookies: []string{"c"}}
	c := o.Clone()
	*c.Enabled = true
	c.QueryParams[0], c.Headers[0], c.Cookies[0] = "x", "y", "z"
	if *o.Enabled || o.QueryParams[0] != "a" || o.Headers[0] != "b" || o.Cookies[0] != "c" {
		t.Errorf("Clone aliased the original: %+v", o)
	}
	if (*Options)(nil).Clone() != nil || !(*Options)(nil).IsEnabled() || o.IsEnabled() {
		t.Error("unexpected nil or disabled Options behavior")
	}
}

func TestNoAllocationWhenClean(t *testing.T) {
	uri := "/api/query?" + testCleanQuery
	for name, f := range map[string]func(){
		"Query":  func() { _ = Query(testCleanQuery) },
		"URI":    func() { _ = URI(uri) },
		"Header": func() { _ = Header(headers.NameUserAgent, testValue) },
		"Cookie": func() { _ = Cookie(testCookieName, testValue) },
	} {
		if allocs := testing.AllocsPerRun(100, f); allocs != 0 {
			t.Errorf("%s allocated %v times on a clean value", name, allocs)
		}
	}
}

func BenchmarkQuery(b *testing.B) {
	for name, q := range map[string]string{"clean": testCleanQuery, "redacted": "user=u&password=p&" + testCleanQuery} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = Query(q)
			}
		})
	}
}
