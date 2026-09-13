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

// Package cachecontrol parses the Cache-Control field. RFC 9111 defines two
// separate grammars for it -- 5.2.1 for a request and 5.2.2 for a response --
// which share directive names that do not mean the same thing on both sides,
// so each has its own type here rather than one shared set of flags.
package cachecontrol

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// RequestDirectives holds the directives RFC 9111 5.2.1 defines for a request.
// A delta-seconds directive is a pointer because absent and zero ask for
// different things: no max-age states no preference, while max-age=0 demands
// a response that has not aged at all.
type RequestDirectives struct {
	NoCache      bool
	NoStore      bool
	NoTransform  bool
	OnlyIfCached bool
	MaxAge       *int
	MaxStale     *int
	// MaxStaleAny records a bare max-stale, which accepts a stale response of
	// any age rather than naming a bound
	MaxStaleAny bool
	MinFresh    *int
}

// ResponseDirectives holds the directives RFC 9111 5.2.2 defines for a
// response, plus the RFC 5861 stale-serving extensions.
type ResponseDirectives struct {
	NoCache         bool
	NoStore         bool
	NoTransform     bool
	Private         bool
	Public          bool
	MustRevalidate  bool
	ProxyRevalidate bool
	MustUnderstand  bool
	Immutable       bool

	MaxAge               *int
	SharedMaxAge         *int
	StaleWhileRevalidate *int
	StaleIfError         *int

	// FreshnessSeen records that a lifetime directive was present even if its
	// argument would not parse, so a cache can revalidate rather than assume
	// the response is fresh forever
	FreshnessSeen bool
}

// Lifetime returns the freshness lifetime a shared cache should apply, which
// RFC 9111 4.2.1 takes from s-maxage ahead of max-age.
func (d *ResponseDirectives) Lifetime() (int, bool) {
	if d == nil {
		return 0, false
	}
	if d.SharedMaxAge != nil {
		return *d.SharedMaxAge, true
	}
	if d.MaxAge != nil {
		return *d.MaxAge, true
	}
	return 0, false
}

// ParseRequest reads the request directives from every Cache-Control field
// line on the header.
func ParseRequest(h http.Header) *RequestDirectives {
	d := &RequestDirectives{}
	if h == nil {
		return d
	}
	scan(h.Values(headers.NameCacheControl), func(name, arg string, hasArg bool) {
		switch name {
		case headers.ValueNoCache:
			d.NoCache = true
		case headers.ValueNoStore:
			d.NoStore = true
		case headers.ValueNoTransform:
			d.NoTransform = true
		case headers.ValueOnlyIfCached:
			d.OnlyIfCached = true
		case headers.ValueMaxAge:
			d.MaxAge = delta(arg)
		case headers.ValueMinFresh:
			d.MinFresh = delta(arg)
		case headers.ValueMaxStale:
			if !hasArg {
				d.MaxStaleAny = true
				return
			}
			d.MaxStale = delta(arg)
		}
	})
	return d
}

// ParseResponse reads the response directives from every Cache-Control field
// line on the header.
func ParseResponse(h http.Header) *ResponseDirectives {
	if h == nil {
		return &ResponseDirectives{}
	}
	return parseResponseValues(h.Values(headers.NameCacheControl))
}

// ParseResponseTargeted reads the response directives a shared cache should
// apply, preferring the first usable targeted field. RFC 9213 3 has a targeted
// field override Cache-Control entirely, so a CDN can be told something
// different from what downstream caches are told -- but 2.1 only allows that
// for a non-empty Structured Field Dictionary. An empty or malformed field
// falls back to Cache-Control rather than denying the response a policy.
func ParseResponseTargeted(h http.Header, targets ...string) *ResponseDirectives {
	if h == nil {
		return &ResponseDirectives{}
	}
	for _, t := range targets {
		vv := h.Values(t)
		if len(vv) == 0 {
			continue
		}
		if !validStructuredDictionary(vv) {
			continue
		}
		return parseResponseValues(vv)
	}
	return parseResponseValues(h.Values(headers.NameCacheControl))
}

func parseResponseValues(values []string) *ResponseDirectives {
	d := &ResponseDirectives{}
	// RFC 9111 4.2.1: when a freshness directive repeats, the first occurrence
	// stands. Tracking that separately from the parsed value matters because a
	// first occurrence that would not parse must not let a later one through.
	var sawMaxAge, sawSharedMaxAge bool
	scan(values, func(name, arg string, _ bool) {
		switch name {
		case headers.ValueNoCache:
			d.NoCache = true
		case headers.ValueNoStore:
			d.NoStore = true
		case headers.ValueNoTransform:
			d.NoTransform = true
		case headers.ValuePrivate:
			d.Private = true
		case headers.ValuePublic:
			d.Public = true
		case headers.ValueMustRevalidate:
			d.MustRevalidate = true
		case headers.ValueProxyRevalidate:
			d.ProxyRevalidate = true
		case headers.ValueMustUnderstand:
			d.MustUnderstand = true
		case headers.ValueImmutable:
			d.Immutable = true
		case headers.ValueMaxAge:
			// a later duplicate must not relax what the first one said, here
			// or on any subsequent field line
			if sawMaxAge {
				return
			}
			sawMaxAge = true
			d.FreshnessSeen = true
			d.MaxAge = delta(arg)
		case headers.ValueSharedMaxAge:
			if sawSharedMaxAge {
				return
			}
			sawSharedMaxAge = true
			d.FreshnessSeen = true
			d.SharedMaxAge = delta(arg)
		case headers.ValueStaleWhileRevalidate:
			d.StaleWhileRevalidate = delta(arg)
		case headers.ValueStaleIfError:
			d.StaleIfError = delta(arg)
		}
	})
	return d
}

func delta(arg string) *int {
	if arg == "" {
		return nil
	}
	v, err := strconv.Atoi(arg)
	if err != nil || v < 0 {
		return nil
	}
	return &v
}

// scan walks every directive across all field lines of a Cache-Control header.
// RFC 9110 5.3 makes repeated field lines equivalent to one comma-separated
// value, so a directive carries the same weight wherever it appears. An
// argument may be a quoted string holding commas, which is why this does not
// simply split on ",".
func scan(values []string, fn func(name, arg string, hasArg bool)) bool {
	var malformed bool
	for _, v := range values {
		var i int
		for i < len(v) {
			for i < len(v) && (v[i] == ',' || v[i] == ' ' || v[i] == '\t') {
				i++
			}
			start := i
			for i < len(v) && v[i] != '=' && v[i] != ',' {
				i++
			}
			name := strings.ToLower(strings.TrimRight(v[start:i], " \t"))
			var arg string
			var hasArg, bad bool
			if i < len(v) && v[i] == '=' {
				i++
				hasArg = true
				arg, i, bad = argument(v, i)
				malformed = malformed || bad
			}
			if name != "" {
				fn(name, arg, hasArg)
			}
		}
	}
	return malformed
}

// argument reads a directive argument starting at i, returning it, the index
// just past it, and whether it was malformed. A quoted argument may contain
// commas and escapes; one that never closes is a syntax error.
func argument(v string, i int) (string, int, bool) {
	for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
		i++
	}
	if i < len(v) && v[i] == '"' {
		i++
		var sb strings.Builder
		for i < len(v) && v[i] != '"' {
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			sb.WriteByte(v[i])
			i++
		}
		if i >= len(v) {
			return sb.String(), i, true
		}
		return sb.String(), i + 1, false
	}
	start := i
	for i < len(v) && v[i] != ',' {
		i++
	}
	return strings.TrimSpace(v[start:i]), i, false
}

type sfParser struct {
	value string
	pos   int
}

func validStructuredDictionary(values []string) bool {
	if len(values) == 0 {
		return false
	}
	p := sfParser{value: strings.Join(values, ",")}
	p.skipSpaces()
	if p.pos == len(p.value) {
		return false
	}
	for {
		if !p.parseKey() {
			return false
		}
		if p.take('=') && !p.parseMemberValue() {
			return false
		}
		if !p.parseParameters() {
			return false
		}
		p.skipSpaces()
		if p.pos == len(p.value) {
			return true
		}
		if !p.take(',') {
			return false
		}
		p.skipSpaces()
		if p.pos == len(p.value) {
			return false
		}
	}
}

func (p *sfParser) parseMemberValue() bool {
	if p.take('(') {
		p.skipSpaces()
		for !p.take(')') {
			if !p.parseBareItem() || !p.parseParameters() {
				return false
			}
			if p.take(')') {
				return true
			}
			if !p.take(' ') {
				return false
			}
			p.skipSpaces()
		}
		return true
	}
	return p.parseBareItem()
}

func (p *sfParser) parseParameters() bool {
	for {
		if !p.take(';') {
			return true
		}
		if !p.parseKey() {
			return false
		}
		if p.take('=') && !p.parseBareItem() {
			return false
		}
	}
}

func (p *sfParser) parseBareItem() bool {
	if p.pos == len(p.value) {
		return false
	}
	switch p.value[p.pos] {
	case '"':
		return p.parseString()
	case ':':
		return p.parseByteSequence()
	case '?':
		p.pos++
		return p.take('0') || p.take('1')
	case '-':
		return p.parseNumber()
	default:
		if isDigit(p.value[p.pos]) {
			return p.parseNumber()
		}
		return p.parseToken()
	}
}

func (p *sfParser) parseKey() bool {
	if p.pos == len(p.value) ||
		(!isLowerAlpha(p.value[p.pos]) && p.value[p.pos] != '*') {
		return false
	}
	p.pos++
	for p.pos < len(p.value) && isKeyChar(p.value[p.pos]) {
		p.pos++
	}
	return true
}

func (p *sfParser) parseToken() bool {
	if p.pos == len(p.value) ||
		(!isAlpha(p.value[p.pos]) && p.value[p.pos] != '*') {
		return false
	}
	p.pos++
	for p.pos < len(p.value) && isTokenChar(p.value[p.pos]) {
		p.pos++
	}
	return true
}

func (p *sfParser) parseString() bool {
	p.pos++
	for p.pos < len(p.value) {
		c := p.value[p.pos]
		p.pos++
		switch {
		case c == '"':
			return true
		case c == '\\':
			if p.pos == len(p.value) ||
				(p.value[p.pos] != '"' && p.value[p.pos] != '\\') {
				return false
			}
			p.pos++
		case c < 0x20 || c > 0x7e:
			return false
		}
	}
	return false
}

func (p *sfParser) parseByteSequence() bool {
	p.pos++
	start := p.pos
	for p.pos < len(p.value) && p.value[p.pos] != ':' {
		p.pos++
	}
	if p.pos == len(p.value) {
		return false
	}
	encoded := p.value[start:p.pos]
	p.pos++
	_, err := base64.StdEncoding.DecodeString(encoded)
	return err == nil
}

func (p *sfParser) parseNumber() bool {
	start := p.pos
	p.take('-')
	digits := 0
	for p.pos < len(p.value) && isDigit(p.value[p.pos]) {
		p.pos++
		digits++
	}
	if digits == 0 {
		return false
	}
	if !p.take('.') {
		return digits <= 15
	}
	fraction := 0
	for p.pos < len(p.value) && isDigit(p.value[p.pos]) {
		p.pos++
		fraction++
	}
	return fraction >= 1 && fraction <= 3 && p.pos-start <= 16
}

func (p *sfParser) skipSpaces() {
	for p.pos < len(p.value) && p.value[p.pos] == ' ' {
		p.pos++
	}
}

func (p *sfParser) take(c byte) bool {
	if p.pos == len(p.value) || p.value[p.pos] != c {
		return false
	}
	p.pos++
	return true
}

func isLowerAlpha(c byte) bool {
	return c >= 'a' && c <= 'z'
}

func isAlpha(c byte) bool {
	return isLowerAlpha(c) || c >= 'A' && c <= 'Z'
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func isKeyChar(c byte) bool {
	return isLowerAlpha(c) || isDigit(c) || strings.ContainsRune("_-.*", rune(c))
}

func isTokenChar(c byte) bool {
	return isAlpha(c) || isDigit(c) ||
		strings.ContainsRune("!#$%&'*+-.^_`|~:/", rune(c))
}
