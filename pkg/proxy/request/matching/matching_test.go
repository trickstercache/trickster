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

package matching

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegex(t *testing.T) {
	re, err := NewRegex("^v[0-9]+$")
	require.NoError(t, err)
	require.True(t, re.Match("v12"))
	require.False(t, re.Match("v12x"))
	require.False(t, re.MatchesEmpty())
	require.Equal(t, "^v[0-9]+$", re.String())
	require.NotNil(t, re.Regexp())

	empty, err := NewRegex("^.*$")
	require.NoError(t, err)
	require.True(t, empty.MatchesEmpty())

	_, err = NewRegex("^(")
	require.Error(t, err)

	var none *Regex
	require.False(t, none.Match("x"))
	require.Nil(t, none.Regexp())
	require.Empty(t, none.String())
}

func TestExact(t *testing.T) {
	require.True(t, Exact("a").Match("a"))
	require.False(t, Exact("a").Match("A"))
	require.True(t, Exact("").Match(""))
}

func TestCutPathPrefix(t *testing.T) {
	tests := []struct {
		path, prefix, rest string
		ok                 bool
	}{
		{"/foo", "/foo", "", true},
		{"/foo/bar", "/foo", "/bar", true},
		{"/foo/", "/foo", "/", true},
		{"/foobar", "/foo", "", false},
		{"/foo/bar", "/foo/", "bar", true},
		{"/foo", "/foo/", "", false},
		{"/anything", "/", "anything", true},
		{"foo/bar", "foo", "/bar", true},
		{"/other", "/foo", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.path+"|"+tt.prefix, func(t *testing.T) {
			rest, ok := CutPathPrefix(tt.path, tt.prefix)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.rest, rest)
		})
	}
}

func TestAnchors(t *testing.T) {
	require.Equal(t, "^/a", AnchorStart("/a"))
	require.Equal(t, "^/a", AnchorStart("^/a"))
	require.Equal(t, "^(?:a|b)$", AnchorWhole("a|b"))
}

func TestHeaderLookups(t *testing.T) {
	h := http.Header{"X-Tenant": {"a", "b"}, "X-Empty": {""}}
	require.Equal(t, "X-Tenant", CanonicalHeaderName("x-tenant"))
	require.Equal(t, "a", HeaderValue(h, "X-Tenant"))
	require.Empty(t, HeaderValue(h, "X-Empty"))
	require.Empty(t, HeaderValue(h, "X-Absent"))
	require.True(t, HasHeader(h, "X-Empty"))
	require.False(t, HasHeader(h, "X-Absent"))
	require.Empty(t, HeaderValue(nil, "X-Tenant"))
	require.False(t, HasHeader(nil, "X-Tenant"))
}

func TestQueryLookups(t *testing.T) {
	v, _ := url.ParseQuery("a=1&a=2&empty=")
	require.Equal(t, "1", QueryValue(v, "a"))
	require.Empty(t, QueryValue(v, "empty"))
	require.Empty(t, QueryValue(v, "absent"))
	require.True(t, HasQuery(v, "empty"))
	require.False(t, HasQuery(v, "absent"))
	require.Empty(t, QueryValue(nil, "a"))
	require.False(t, HasQuery(nil, "a"))
}

func TestHeaderPredicate(t *testing.T) {
	exact, err := NewHeaderPredicate("x-tenant", "a", false)
	require.NoError(t, err)
	require.Equal(t, "X-Tenant", exact.Name())
	require.True(t, exact.Match(http.Header{"X-Tenant": {"a"}}))
	require.False(t, exact.Match(http.Header{"X-Tenant": {"b", "a"}}))
	require.False(t, exact.Match(http.Header{}))
	require.False(t, exact.Match(nil))

	re, err := NewHeaderPredicate("X-Tenant", "^a.*$", true)
	require.NoError(t, err)
	require.True(t, re.Match(http.Header{"X-Tenant": {"abc"}}))
	require.False(t, re.Match(http.Header{"X-Tenant": {"b"}}))

	// an absent header never matches, even a pattern the empty value satisfies
	any, err := NewHeaderPredicate("X-Tenant", ".*", true)
	require.NoError(t, err)
	require.True(t, any.Match(http.Header{"X-Tenant": {""}}))
	require.False(t, any.Match(http.Header{}))
	require.False(t, any.Match(http.Header{"X-Tenant": {}}))

	_, err = NewHeaderPredicate("X-Tenant", "^(", true)
	require.Error(t, err)
}

func TestQueryPredicate(t *testing.T) {
	exact, err := NewQueryPredicate("v", "1", false)
	require.NoError(t, err)
	require.Equal(t, "v", exact.Name())
	values, _ := url.ParseQuery("v=1&w=2")
	require.True(t, exact.Match(values))
	values, _ = url.ParseQuery("v=2&v=1")
	require.False(t, exact.Match(values))
	require.False(t, exact.Match(url.Values{}))
	require.False(t, exact.Match(nil))

	re, err := NewQueryPredicate("v", "^[0-9]+$", true)
	require.NoError(t, err)
	values, _ = url.ParseQuery("v=42")
	require.True(t, re.Match(values))
	values, _ = url.ParseQuery("v=x")
	require.False(t, re.Match(values))

	any, err := NewQueryPredicate("v", ".*", true)
	require.NoError(t, err)
	values, _ = url.ParseQuery("v=")
	require.True(t, any.Match(values))
	require.False(t, any.Match(url.Values{}))

	_, err = NewQueryPredicate("v", "^(", true)
	require.Error(t, err)
}

func TestPredicates(t *testing.T) {
	var none *Predicates
	require.True(t, none.Empty())
	require.True(t, (&Predicates{}).Empty())

	hp, _ := NewHeaderPredicate("X-Tenant", "a", false)
	qp, _ := NewQueryPredicate("v", "^[0-9]+$", true)
	p := &Predicates{Headers: []HeaderPredicate{hp}, Queries: []QueryPredicate{qp}}
	require.False(t, p.Empty())

	r, _ := http.NewRequest(http.MethodGet, "http://example.com/?v=42", nil)
	r.Header.Set("X-Tenant", "a")
	var q QueryValues
	require.True(t, p.Match(r, &q))
	require.True(t, q.parsed)
	// the parsed values are reused, so a later change to the URL is not seen
	r.URL.RawQuery = "v=x"
	require.True(t, p.Match(r, &q))
	require.False(t, p.Match(r, &QueryValues{}))

	r.Header.Del("X-Tenant")
	q = QueryValues{}
	require.False(t, p.Match(r, &q))
	require.False(t, q.parsed, "a header miss must not parse the query")

	headersOnly := &Predicates{Headers: []HeaderPredicate{hp}}
	r.Header.Set("X-Tenant", "a")
	q = QueryValues{}
	require.True(t, headersOnly.Match(r, &q))
	require.False(t, q.parsed)
}
