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

package options

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	reqmatching "github.com/trickstercache/trickster/v2/pkg/proxy/request/matching"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func conditioned(hdrs, queries []*Condition) *Options {
	o := New()
	o.Path = "/api/"
	o.MatchTypeName = matching.PathMatchNamePrefix
	o.MatchHeaders = hdrs
	o.MatchQueryParams = queries
	return o
}

func TestConditionsCompileOnInitialize(t *testing.T) {
	o := conditioned([]*Condition{{Name: "x-tenant", Value: "gold"}},
		[]*Condition{{Name: "version", Value: "^v[0-9]+$", Regex: true}})
	require.NoError(t, o.Initialize(""))
	require.NotNil(t, o.Predicates)
	require.Len(t, o.Predicates.Headers, 1)
	require.Len(t, o.Predicates.Queries, 1)
	require.Equal(t, "X-Tenant", o.Predicates.Headers[0].Name())
	_, err := o.Validate()
	require.NoError(t, err)

	r, _ := http.NewRequest(http.MethodGet, "http://example.com/api/?version=v2", nil)
	r.Header.Set("X-Tenant", "gold")
	var q reqmatching.QueryValues
	require.True(t, o.Predicates.Match(r, &q))
	r.Header.Set("X-Tenant", "silver")
	require.False(t, o.Predicates.Match(r, &q))
}

func TestConditionsClone(t *testing.T) {
	o := conditioned([]*Condition{{Name: "X-Tenant", Value: "gold"}},
		[]*Condition{{Name: "v", Value: "1"}})
	require.NoError(t, o.Initialize(""))
	c := o.Clone()
	require.Equal(t, o.MatchHeaders, c.MatchHeaders)
	require.Equal(t, o.MatchQueryParams, c.MatchQueryParams)
	require.NotSame(t, o.MatchHeaders[0], c.MatchHeaders[0])
	c.MatchHeaders[0].Value = "silver"
	require.Equal(t, "gold", o.MatchHeaders[0].Value)
	require.Nil(t, cloneConditions(nil))
	var none *Condition
	require.Nil(t, none.Clone())
	require.Nil(t, New().Predicates)
}

func TestConditionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		hdrs    []*Condition
		queries []*Condition
		want    string
	}{
		{"nil header condition", []*Condition{nil}, nil, "a valid header name is required"},
		{"empty header name", []*Condition{{Value: "x"}}, nil, "a valid header name is required"},
		{"invalid header name", []*Condition{{Name: "X Tenant"}}, nil, "a valid header name is required"},
		{"bad header regex", []*Condition{{Name: "X-Tenant", Value: "^(", Regex: true}}, nil, "invalid match_headers regex"},
		{"nil query condition", nil, []*Condition{nil}, "a name is required"},
		{"empty query name", nil, []*Condition{{Value: "x"}}, "a name is required"},
		{"bad query regex", nil, []*Condition{{Name: "v", Value: "^(", Regex: true}}, "invalid match_query_params regex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := conditioned(tt.hdrs, tt.queries)
			require.NoError(t, o.Initialize(""), "compile errors are deferred to Validate")
			require.Nil(t, o.Predicates)
			_, err := o.Validate()
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestSegmentMatchType(t *testing.T) {
	o := New()
	o.Path = "/foo"
	o.MatchTypeName = "SEGMENT"
	require.NoError(t, o.Initialize(""))
	require.Equal(t, matching.PathMatchTypeSegment, o.MatchType)
	require.Equal(t, matching.PathMatchNameSegment, o.MatchTypeName)
	_, err := o.Validate()
	require.NoError(t, err)

	catchAll := New()
	catchAll.Path = "/"
	catchAll.MatchTypeName = matching.PathMatchNameSegment
	re := New()
	re.Path = "^/api/[0-9]+"
	l := List{catchAll, re}
	require.NoError(t, l.Initialize())
	require.True(t, l.RegexShadowedByCatchAll())
}

func TestListMatchSegment(t *testing.T) {
	seg := New()
	seg.Path = "/foo"
	seg.MatchTypeName = matching.PathMatchNameSegment
	seg.HandlerName = "segment"
	root := New()
	root.Path = "/"
	root.MatchTypeName = matching.PathMatchNamePrefix
	root.HandlerName = "root"
	l := List{seg, root}
	require.NoError(t, l.Initialize())
	require.Equal(t, "segment", l.Match(http.MethodGet, "/foo").HandlerName)
	require.Equal(t, "segment", l.Match(http.MethodGet, "/foo/bar").HandlerName)
	require.Equal(t, "root", l.Match(http.MethodGet, "/foobar").HandlerName)
	require.Nil(t, l.Match(http.MethodPost, "/foo/bar"), "matched path without the method")
}

func TestConditionsFromYAML(t *testing.T) {
	var o Options
	require.NoError(t, yaml.Unmarshal([]byte(`
path: /api/
match_type: segment
match_headers:
  - name: X-Tenant
    value: gold
match_query_params:
  - name: version
    value: '^v[0-9]+$'
    regex: true
`), &o))
	require.NoError(t, o.Initialize(""))
	require.Equal(t, matching.PathMatchTypeSegment, o.MatchType)
	require.Equal(t, []*Condition{{Name: "X-Tenant", Value: "gold"}}, o.MatchHeaders)
	require.Equal(t, []*Condition{{Name: "version", Value: "^v[0-9]+$", Regex: true}}, o.MatchQueryParams)
	require.NotNil(t, o.Predicates)
}

func TestMatchIdentities(t *testing.T) {
	newPath := func(path string, mt matching.PathMatchName, hdr string,
		methods []string, conds []*Condition,
	) *Options {
		o := New()
		o.Path = path
		o.MatchTypeName = mt
		o.Methods = methods
		o.MatchHeaders = conds
		if hdr != "" {
			o.RequestHeaders = map[string]string{"X-Auth": hdr}
		}
		require.NoError(t, o.Initialize(""))
		return o
	}
	gold := newPath("/api/v1", matching.PathMatchNameExact, "gold",
		[]string{http.MethodGet}, []*Condition{{Name: "X-Tenant", Value: "gold"}})
	silver := newPath("/api/v1", matching.PathMatchNameExact, "silver",
		[]string{http.MethodGet}, []*Condition{{Name: "X-Tenant", Value: "silver"}})
	// the same configured headers as gold, so one identity covers both
	twin := newPath("/api/v1", matching.PathMatchNameExact, "gold",
		[]string{http.MethodGet, http.MethodPost}, nil)
	segment := newPath("/api", matching.PathMatchNameSegment, "segment",
		[]string{http.MethodGet}, nil)
	regex := newPath("^/api/[0-9]+", matching.PathMatchNameRegex, "regex",
		[]string{http.MethodGet}, nil)
	plain := newPath("/api/", matching.PathMatchNamePrefix, "", []string{http.MethodGet}, nil)
	l := List{gold, silver, twin, segment, regex, plain}

	// every tier a request for the pathname could resolve through
	got := l.MatchIdentities(http.MethodGet, "/api/v1")
	require.Equal(t, []string{"", gold.IdentityKeyPart(), silver.IdentityKeyPart(),
		segment.IdentityKeyPart()}, got)
	require.Equal(t, gold.IdentityKeyPart(), twin.IdentityKeyPart(),
		"the twin configures the same headers, so it shares gold's identity and is listed once")

	// HEAD follows GET, and POST reaches only the path that names it
	require.Equal(t, got, l.MatchIdentities(http.MethodHead, "/api/v1"))
	require.Equal(t, []string{"", twin.IdentityKeyPart()},
		l.MatchIdentities(http.MethodPost, "/api/v1"))

	// a pathname under the segment prefix reaches it and the regex path
	require.Equal(t, []string{"", segment.IdentityKeyPart(), regex.IdentityKeyPart()},
		l.MatchIdentities(http.MethodGet, "/api/42"))
	// off a segment boundary neither matches
	require.Equal(t, []string{""}, l.MatchIdentities(http.MethodGet, "/apiary"))
	require.Equal(t, []string{"", segment.IdentityKeyPart()},
		l.MatchIdentities(http.MethodGet, "/api"))
	// the plain prefix matches here but configures no identity to purge
	require.Equal(t, []string{"", segment.IdentityKeyPart()},
		l.MatchIdentities(http.MethodGet, "/api/other"))
	require.Equal(t, []string{""}, List{nil}.MatchIdentities(http.MethodGet, "/api"))
}
