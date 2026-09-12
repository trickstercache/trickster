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

// Package matching provides the request-matching primitives shared by the
// router and the rule backend: exact and regular-expression value matchers
// validated when they are built, header and query lookups, and immutable
// header and query predicates. Nothing here compiles, locks or writes at
// request time.
package matching

import (
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
)

// Predicates are the header and query parameter conditions a request must
// satisfy to select a route, compiled once when the route is configured
type Predicates struct {
	Headers []HeaderPredicate
	Queries []QueryPredicate
}

// Empty reports whether the predicates condition nothing
func (p *Predicates) Empty() bool {
	return p == nil || len(p.Headers)+len(p.Queries) == 0
}

// Match reports whether the request satisfies every predicate. The query
// string is parsed only when a query predicate is reached, and the parsed
// values are kept in q for the rest of the matching attempt.
func (p *Predicates) Match(r *http.Request, q *QueryValues) bool {
	for i := range p.Headers {
		if !p.Headers[i].Match(r.Header) {
			return false
		}
	}
	if len(p.Queries) > 0 {
		values := q.Get(r)
		for i := range p.Queries {
			if !p.Queries[i].Match(values) {
				return false
			}
		}
	}
	return true
}

// QueryValues parses a request's query string at most once per matching
// attempt, however many candidates and tiers consult it
type QueryValues struct {
	values url.Values
	parsed bool
}

// Get returns the parsed query values, parsing them on first use
func (q *QueryValues) Get(r *http.Request) url.Values {
	if !q.parsed {
		q.values, q.parsed = r.URL.Query(), true
	}
	return q.values
}

// Regex is a regular expression compiled once and safe for concurrent use
type Regex struct {
	re *regexp.Regexp
}

// NewRegex compiles pattern; the error is the regexp package's
func NewRegex(pattern string) (*Regex, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return &Regex{re: re}, nil
}

// Match reports whether s contains a match of the expression
func (r *Regex) Match(s string) bool {
	return r != nil && r.re.MatchString(s)
}

// MatchesEmpty reports whether the expression is satisfied by an empty
// value, which is what an absent header or parameter reads as
func (r *Regex) MatchesEmpty() bool {
	return r.Match("")
}

// Regexp returns the compiled expression for callers that need submatches
func (r *Regex) Regexp() *regexp.Regexp {
	if r == nil {
		return nil
	}
	return r.re
}

func (r *Regex) String() string {
	if r == nil {
		return ""
	}
	return r.re.String()
}

// Exact matches a value byte for byte
type Exact string

// Match reports whether s equals the exact value
func (e Exact) Match(s string) bool {
	return string(e) == s
}

// AnchorStart anchors a pattern to the start of the value unless it is already
func AnchorStart(pattern string) string {
	if strings.HasPrefix(pattern, "^") {
		return pattern
	}
	return "^" + pattern
}

// AnchorWhole anchors a pattern so it must match the entire value
func AnchorWhole(pattern string) string {
	return "^(?:" + pattern + ")$"
}

// CutPathPrefix returns the remainder of path after prefix when prefix matches
// on a segment boundary: /foo matches /foo and /foo/bar, but not /foobar. It
// allocates only when path or prefix lacks its leading slash.
func CutPathPrefix(path, prefix string) (string, bool) {
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	if rest == "" || strings.HasSuffix(prefix, "/") || strings.HasPrefix(rest, "/") {
		return rest, true
	}
	return "", false
}

// CanonicalHeaderName returns the canonical spelling of a header name, which
// the header lookups below expect to be given
func CanonicalHeaderName(name string) string {
	return textproto.CanonicalMIMEHeaderKey(name)
}

// HeaderValue returns the first value of a header, or an empty string when
// the header is absent; name must be canonical
func HeaderValue(h http.Header, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// HasHeader reports whether the header is present, which a value lookup
// cannot tell from a header that is present and empty; name must be canonical
func HasHeader(h http.Header, name string) bool {
	_, ok := h[name]
	return ok
}

// QueryValue returns the first value of a parameter in already parsed
// query values, or an empty string when the parameter is absent
func QueryValue(v url.Values, name string) string {
	return v.Get(name)
}

// HasQuery reports whether the parameter is present, empty or not
func HasQuery(v url.Values, name string) bool {
	return v.Has(name)
}

// predicate is one named field compared exactly or against an expression
type predicate struct {
	name  string
	exact string
	re    *Regex
}

func newPredicate(name, value string, regex bool) (predicate, error) {
	p := predicate{name: name, exact: value}
	if regex {
		re, err := NewRegex(value)
		if err != nil {
			return predicate{}, err
		}
		p.re = re
	}
	return p, nil
}

func (p predicate) matches(v string) bool {
	if p.re != nil {
		return p.re.Match(v)
	}
	return v == p.exact
}

// HeaderPredicate is a condition on one request header. A request satisfies
// it when the header is present and its first value matches; an absent
// header never matches, even a pattern the empty string would satisfy.
type HeaderPredicate struct {
	predicate
}

// NewHeaderPredicate builds a predicate on the named header, canonicalizing
// the name and compiling the value when regex is set
func NewHeaderPredicate(name, value string, regex bool) (HeaderPredicate, error) {
	p, err := newPredicate(CanonicalHeaderName(name), value, regex)
	if err != nil {
		return HeaderPredicate{}, err
	}
	return HeaderPredicate{predicate: p}, nil
}

// Name is the canonical header name the predicate reads
func (p HeaderPredicate) Name() string {
	return p.name
}

// Match reports whether the headers satisfy the predicate
func (p HeaderPredicate) Match(h http.Header) bool {
	v, ok := h[p.name]
	return ok && len(v) > 0 && p.matches(v[0])
}

// QueryPredicate is a condition on one query parameter, evaluated over
// values the caller parsed once. A request satisfies it when the parameter
// is present and its first value matches; an absent parameter never matches.
type QueryPredicate struct {
	predicate
}

// NewQueryPredicate builds a predicate on the named parameter, compiling
// the value when regex is set
func NewQueryPredicate(name, value string, regex bool) (QueryPredicate, error) {
	p, err := newPredicate(name, value, regex)
	if err != nil {
		return QueryPredicate{}, err
	}
	return QueryPredicate{predicate: p}, nil
}

// Name is the parameter name the predicate reads
func (p QueryPredicate) Name() string {
	return p.name
}

// Match reports whether the parsed query values satisfy the predicate
func (p QueryPredicate) Match(values url.Values) bool {
	v, ok := values[p.name]
	return ok && len(v) > 0 && p.matches(v[0])
}
