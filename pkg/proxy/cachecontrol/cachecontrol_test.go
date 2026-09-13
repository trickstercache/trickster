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

package cachecontrol

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func header(lines ...string) http.Header {
	h := http.Header{}
	for _, l := range lines {
		h.Add(headers.NameCacheControl, l)
	}
	return h
}

func eq(t *testing.T, name string, got, want *int) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s: got %v want %v", name, got, want)
	case *got != *want:
		t.Errorf("%s: got %d want %d", name, *got, *want)
	}
}

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected RequestDirectives
	}{
		{"empty", nil, RequestDirectives{}},
		{"no-cache", []string{"no-cache"}, RequestDirectives{NoCache: true}},
		{"no-store", []string{"no-store"}, RequestDirectives{NoStore: true}},
		{"only-if-cached", []string{"only-if-cached"}, RequestDirectives{OnlyIfCached: true}},
		{
			"max-age zero is not absent",
			[]string{"max-age=0"},
			RequestDirectives{MaxAge: new(0)},
		},
		{"max-age", []string{"max-age=60"}, RequestDirectives{MaxAge: new(60)}},
		{"min-fresh", []string{"min-fresh=30"}, RequestDirectives{MinFresh: new(30)}},
		{"bare max-stale", []string{"max-stale"}, RequestDirectives{MaxStaleAny: true}},
		{"bounded max-stale", []string{"max-stale=10"}, RequestDirectives{MaxStale: new(10)}},
		{
			"combined",
			[]string{"max-age=5, only-if-cached"},
			RequestDirectives{MaxAge: new(5), OnlyIfCached: true},
		},
		{"uppercase", []string{"NO-CACHE"}, RequestDirectives{NoCache: true}},
		{"spacing", []string{"  max-age =  7 "}, RequestDirectives{MaxAge: new(7)}},
		// RFC 9110 5.3: repeated field lines carry the same weight as one list
		{
			"second field line",
			[]string{"max-age=5", "no-store"},
			RequestDirectives{MaxAge: new(5), NoStore: true},
		},
		{"negative is invalid", []string{"max-age=-5"}, RequestDirectives{}},
		{"unparsable is invalid", []string{"max-age=abc"}, RequestDirectives{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := ParseRequest(header(test.lines...))
			if d.NoCache != test.expected.NoCache || d.NoStore != test.expected.NoStore ||
				d.NoTransform != test.expected.NoTransform ||
				d.OnlyIfCached != test.expected.OnlyIfCached ||
				d.MaxStaleAny != test.expected.MaxStaleAny {
				t.Errorf("flags: got %+v want %+v", *d, test.expected)
			}
			eq(t, "MaxAge", d.MaxAge, test.expected.MaxAge)
			eq(t, "MinFresh", d.MinFresh, test.expected.MinFresh)
			eq(t, "MaxStale", d.MaxStale, test.expected.MaxStale)
		})
	}
	if got := ParseRequest(nil); got == nil {
		t.Error("expected directives for a nil header")
	}
}

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name                      string
		lines                     []string
		noCache, noStore, private bool
		public, mustRevalidate    bool
		immutable, mustUnderstand bool
		maxAge, sMaxAge           *int
		staleWhileRev, staleIfErr *int
	}{
		{name: "empty"},
		{name: "no-cache", lines: []string{"no-cache"}, noCache: true},
		{name: "private", lines: []string{"private"}, private: true},
		{
			name: "public with s-maxage", lines: []string{"public, s-maxage=60"},
			public: true, sMaxAge: new(60),
		},
		{name: "max-age", lines: []string{"max-age=300"}, maxAge: new(300)},
		{name: "must-revalidate", lines: []string{"must-revalidate"}, mustRevalidate: true},
		{name: "immutable", lines: []string{"immutable"}, immutable: true},
		{name: "must-understand", lines: []string{"must-understand"}, mustUnderstand: true},
		{
			name: "stale extensions", lines: []string{"max-age=1, stale-while-revalidate=10, stale-if-error=20"},
			maxAge: new(1), staleWhileRev: new(10), staleIfErr: new(20),
		},
		// the reason storage-no-store-on-second-field-line failed: Get() reads
		// only the first line
		{
			name: "no-store on a second field line", lines: []string{"max-age=3600", "no-store"},
			noStore: true, maxAge: new(3600),
		},
		// a quoted argument may hold commas, which naive splitting would break on
		{
			name: "quoted argument with comma", lines: []string{`no-cache="Set-Cookie, X-Foo", max-age=30`},
			noCache: true, maxAge: new(30),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := ParseResponse(header(test.lines...))
			if d.NoCache != test.noCache || d.NoStore != test.noStore ||
				d.Private != test.private || d.Public != test.public ||
				d.MustRevalidate != test.mustRevalidate ||
				d.Immutable != test.immutable || d.MustUnderstand != test.mustUnderstand {
				t.Errorf("flags: got %+v", *d)
			}
			eq(t, "MaxAge", d.MaxAge, test.maxAge)
			eq(t, "SharedMaxAge", d.SharedMaxAge, test.sMaxAge)
			eq(t, "StaleWhileRevalidate", d.StaleWhileRevalidate, test.staleWhileRev)
			eq(t, "StaleIfError", d.StaleIfError, test.staleIfErr)
		})
	}
	if got := ParseResponse(nil); got == nil {
		t.Error("expected directives for a nil header")
	}
}

func TestResponseLifetime(t *testing.T) {
	var nilD *ResponseDirectives
	if _, ok := nilD.Lifetime(); ok {
		t.Error("expected no lifetime from nil")
	}
	if _, ok := (&ResponseDirectives{}).Lifetime(); ok {
		t.Error("expected no lifetime when neither directive is present")
	}
	// s-maxage outranks max-age for a shared cache
	d := &ResponseDirectives{MaxAge: new(60), SharedMaxAge: new(120)}
	if v, ok := d.Lifetime(); !ok || v != 120 {
		t.Errorf("got %d,%t want 120,true", v, ok)
	}
	d = &ResponseDirectives{MaxAge: new(60)}
	if v, ok := d.Lifetime(); !ok || v != 60 {
		t.Errorf("got %d,%t want 60,true", v, ok)
	}
}

func TestParseResponseTargeted(t *testing.T) {
	h := header("max-age=60")
	h.Set(headers.NameCDNCacheControl, "max-age=600")
	// RFC 9213 3: the targeted field replaces Cache-Control, it does not merge
	d := ParseResponseTargeted(h, headers.NameCDNCacheControl)
	if v, ok := d.Lifetime(); !ok || v != 600 {
		t.Errorf("got %d,%t want 600,true", v, ok)
	}

	// with no targeted field present, Cache-Control applies
	d = ParseResponseTargeted(header("max-age=60"), headers.NameCDNCacheControl)
	if v, ok := d.Lifetime(); !ok || v != 60 {
		t.Errorf("got %d,%t want 60,true", v, ok)
	}

	if got := ParseResponseTargeted(nil, headers.NameCDNCacheControl); got == nil {
		t.Error("expected directives for a nil header")
	}
}

func TestFreshnessSeen(t *testing.T) {
	// an unparsable lifetime still counts as stated, so the cache revalidates
	// rather than treating the response as having no expiry at all
	d := ParseResponse(header("max-age=abc"))
	if !d.FreshnessSeen || d.MaxAge != nil {
		t.Errorf("got %+v", *d)
	}
	if ParseResponse(header("public")).FreshnessSeen {
		t.Error("expected no freshness directive")
	}
}

// RFC 9213 2.1: a targeted field only overrides Cache-Control when it actually
// carries a directive and parses
func TestParseResponseTargetedFallback(t *testing.T) {
	tests := []struct {
		name     string
		targeted string
		setField bool
		expected int
	}{
		{"usable targeted field wins", "max-age=600", true, 600},
		{"empty targeted field falls back", "", true, 3600},
		{"unterminated quote falls back", `max-age="`, true, 3600},
		{"invalid dictionary key falls back", "@", true, 3600},
		{"uppercase dictionary key falls back", "Max-Age=600", true, 3600},
		{"empty dictionary member falls back", "none,", true, 3600},
		{"bad parameter falls back", "none;@=?1", true, 3600},

		{"absent targeted field uses cache-control", "", false, 3600},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := header("max-age=3600")
			if test.setField {
				h.Set(headers.NameCDNCacheControl, test.targeted)
			}
			d := ParseResponseTargeted(h, headers.NameCDNCacheControl)
			v, ok := d.Lifetime()
			if !ok || v != test.expected {
				t.Errorf("got %d,%t want %d,true", v, ok, test.expected)
			}
		})
	}
}

// a targeted field naming only no-store is usable, and must not fall back to a
// Cache-Control that would have allowed storage
func TestParseResponseTargetedNoStoreIsUsable(t *testing.T) {
	h := header("max-age=3600")
	h.Set(headers.NameCDNCacheControl, "no-store")
	d := ParseResponseTargeted(h, headers.NameCDNCacheControl)
	if !d.NoStore {
		t.Error("expected no-store from the targeted field")
	}
	if _, ok := d.Lifetime(); ok {
		t.Error("expected no lifetime to carry over from Cache-Control")
	}
}

// RFC 9213 3: a valid, non-empty targeted field is the selected policy whether
// or not this cache recognizes what it names. The specification's own "none"
// example exists to suppress an accompanying Cache-Control.
func TestParseResponseTargetedUnknownDirectiveStillSelects(t *testing.T) {
	tests := []struct {
		name         string
		cacheControl string
		targeted     string
		expectUsable bool
	}{
		{"none suppresses no-store", "no-store", "none", true},
		{"none suppresses a cacheable policy", "public, max-age=3600", "none", true},
		{"an unknown extension still selects", "max-age=3600", "mecone-only", true},
		{
			"unknown value and parameter still select", "max-age=3600",
			`mecone-only="yes";scope=?1`, true,
		},
		{"semantically invalid known value still selects", "max-age=3600", "max-age=abc", true},
		{"an empty field does not select", "max-age=3600", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := header(test.cacheControl)
			h.Set(headers.NameCDNCacheControl, test.targeted)
			d := ParseResponseTargeted(h, headers.NameCDNCacheControl)
			_, hasLifetime := d.Lifetime()
			if test.expectUsable {
				// the targeted field replaced Cache-Control wholesale, so
				// neither its lifetime nor its no-store carries over
				if hasLifetime || d.NoStore {
					t.Errorf("Cache-Control leaked through: %+v", *d)
				}
				return
			}
			if !hasLifetime {
				t.Error("expected the fallback to Cache-Control")
			}
		})
	}
}

// RFC 9111 4.2.1: a repeated freshness directive keeps its first value, so a
// later duplicate cannot relax the first restriction
func TestDuplicateFreshnessDirectiveKeepsTheFirst(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		expected int
	}{
		{"tightest first", []string{"max-age=0, max-age=3600"}, 0},
		{"loosest first", []string{"max-age=3600, max-age=0"}, 3600},
		{"across field lines", []string{"max-age=0", "max-age=3600"}, 0},
		{"s-maxage duplicate", []string{"s-maxage=0, s-maxage=3600"}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseResponse(header(test.lines...)).Lifetime()
			if !ok || got != test.expected {
				t.Errorf("got %d,%t want %d,true", got, ok, test.expected)
			}
		})
	}
	// a first occurrence that will not parse must not let a later one through
	d := ParseResponse(header("max-age=abc, max-age=3600"))
	if d.MaxAge != nil {
		t.Errorf("expected the invalid first value to stand, got %d", *d.MaxAge)
	}
}
