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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"

	"github.com/stretchr/testify/require"
)

func newPathOptions(path string, matchTypeName matching.PathMatchName,
	methods ...string,
) *Options {
	o := New()
	o.Path = path
	o.MatchTypeName = matchTypeName
	if len(methods) > 0 {
		o.Methods = methods
	}
	return o
}

func TestInitializeRegexAutoDetect(t *testing.T) {
	tests := []struct {
		name              string
		path              string
		matchTypeName     matching.PathMatchName
		expectedMatchType matching.PathMatchType
		expectedPath      string
		expectCompiled    bool
	}{
		{
			name:              "caret slash prefix",
			path:              "^/api/[0-9]+/results",
			expectedMatchType: matching.PathMatchTypeRegex,
			expectedPath:      "^/api/[0-9]+/results",
			expectCompiled:    true,
		},
		{
			name:              "escaped caret slash prefix",
			path:              `^\/api\/[0-9]+`,
			expectedMatchType: matching.PathMatchTypeRegex,
			expectedPath:      `^\/api\/[0-9]+`,
			expectCompiled:    true,
		},
		{
			name:              "auto-detection overrides explicit match_type",
			path:              "^/api/.*",
			matchTypeName:     matching.PathMatchNameExact,
			expectedMatchType: matching.PathMatchTypeRegex,
			expectedPath:      "^/api/.*",
			expectCompiled:    true,
		},
		{
			name:              "explicit regex is anchored when unanchored",
			path:              "/api/.*",
			matchTypeName:     matching.PathMatchNameRegex,
			expectedMatchType: matching.PathMatchTypeRegex,
			expectedPath:      "^/api/.*",
			expectCompiled:    true,
		},
		{
			name:              "explicit regex already anchored",
			path:              "^/api/.*$",
			matchTypeName:     matching.PathMatchNameRegex,
			expectedMatchType: matching.PathMatchTypeRegex,
			expectedPath:      "^/api/.*$",
			expectCompiled:    true,
		},
		{
			name:              "literal path remains exact",
			path:              "/api/query",
			expectedMatchType: matching.PathMatchTypeExact,
			expectedPath:      "/api/query",
			expectCompiled:    false,
		},
		{
			name:              "literal path remains prefix",
			path:              "/api",
			matchTypeName:     matching.PathMatchNamePrefix,
			expectedMatchType: matching.PathMatchTypePrefix,
			expectedPath:      "/api",
			expectCompiled:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := newPathOptions(test.path, test.matchTypeName)
			require.NoError(t, o.Initialize(""))
			if o.MatchType != test.expectedMatchType {
				t.Errorf("expected match type %s, got %s",
					test.expectedMatchType, o.MatchType)
			}
			if o.Path != test.expectedPath {
				t.Errorf("expected path %s, got %s", test.expectedPath, o.Path)
			}
			if test.expectCompiled {
				require.NotNil(t, o.Regexp)
				if o.MatchTypeName != matching.PathMatchNameRegex {
					t.Errorf("expected match type name %s, got %s",
						matching.PathMatchNameRegex, o.MatchTypeName)
				}
			} else {
				require.Nil(t, o.Regexp)
			}
		})
	}
}

func TestValidateRegexCompileFailure(t *testing.T) {
	o := newPathOptions("^/api/(unclosed", "")
	require.NoError(t, o.Initialize(""))
	require.Nil(t, o.Regexp)

	_, err := o.Validate()
	require.Error(t, err)
	if !strings.Contains(err.Error(), "^/api/(unclosed") {
		t.Errorf("expected error to contain the pattern, got %v", err)
	}

	err = List{o}.Validate("test-backend")
	require.Error(t, err)
	if !strings.Contains(err.Error(), "test-backend") {
		t.Errorf("expected error to contain the backend name, got %v", err)
	}

	// a valid regex path passes and Validate compiles when Initialize was skipped
	o2 := newPathOptions("^/api/.*", "")
	o2.MatchType = matching.PathMatchTypeRegex
	o2.MatchTypeName = matching.PathMatchNameRegex
	ok, err := o2.Validate()
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, o2.Regexp)
}

func TestRegexClone(t *testing.T) {
	o := newPathOptions("^/api/.*", "")
	require.NoError(t, o.Initialize(""))
	require.NotNil(t, o.Regexp)
	o2 := o.Clone()
	if o2.Regexp != o.Regexp {
		t.Error("expected Clone to copy the compiled regexp pointer")
	}
	if o2.MatchType != matching.PathMatchTypeRegex {
		t.Errorf("expected match type %s, got %s",
			matching.PathMatchTypeRegex, o2.MatchType)
	}
}

func TestRegexOverlay(t *testing.T) {
	// regex paths overlay by exact pattern-string equality
	o1 := newPathOptions("^/api/.*", "", http.MethodGet)
	o2 := newPathOptions("^/api/.*", "", http.MethodGet)
	o3 := newPathOptions("^/other/.*", "", http.MethodGet)
	l := List{o1, o2, o3}
	require.NoError(t, l.Initialize())

	out := List{o1}.Overlay(List{o2})
	require.Equal(t, 1, len(out))
	if out[0] != o2 {
		t.Error("expected overlay with identical pattern and methods to replace")
	}

	out = List{o1}.Overlay(List{o3})
	require.Equal(t, 2, len(out))
	if out[0] != o1 || out[1] != o3 {
		t.Error("expected overlay with a distinct pattern to append")
	}
}

func TestListMatch(t *testing.T) {
	exact := newPathOptions("/api/query", "", http.MethodGet, http.MethodPost)
	prefixShort := newPathOptions("/api", matching.PathMatchNamePrefix, http.MethodGet)
	prefixLong := newPathOptions("/api/admin", matching.PathMatchNamePrefix, http.MethodGet)
	regexLong := newPathOptions("^/results/[0-9]+/detail", "", http.MethodGet)
	regexShort := newPathOptions("^/results/[0-9]+", "", http.MethodGet)
	regexCatchAll := newPathOptions("^/.*", "", http.MethodGet)
	postOnly := newPathOptions("^/write/.*", "", http.MethodPost)

	l := List{exact, prefixShort, prefixLong, regexShort, regexLong,
		regexCatchAll, postOnly}
	require.NoError(t, l.Initialize())

	tests := []struct {
		name     string
		method   string
		path     string
		expected *Options
	}{
		{"exact match wins", http.MethodGet, "/api/query", exact},
		{"longest prefix wins", http.MethodGet, "/api/admin/users", prefixLong},
		{"shorter prefix", http.MethodGet, "/api/other", prefixShort},
		{"regex only after classic misses", http.MethodGet,
			"/results/42/detail", regexLong},
		{"shorter regex when longer misses", http.MethodGet,
			"/results/42/summary", regexShort},
		{"regex catch-all evaluates last", http.MethodGet, "/misc", regexCatchAll},
		{"regex method match", http.MethodPost, "/write/data", postOnly},
		{"implicit HEAD for GET on exact", http.MethodHead, "/api/query", exact},
		{"implicit HEAD for GET on regex", http.MethodHead,
			"/results/42/detail", regexLong},
		{"method not allowed on exact returns nil", http.MethodDelete,
			"/api/query", nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := l.Match(test.method, test.path)
			if out != test.expected {
				t.Errorf("expected %v, got %v", test.expected, out)
			}
		})
	}

	// tie in pattern length is broken by config order
	tieA := newPathOptions("^/tie/[ab]+", "", http.MethodGet)
	tieB := newPathOptions("^/tie/[ba]+", "", http.MethodGet)
	l2 := List{tieB, tieA}
	require.NoError(t, l2.Initialize())
	if out := l2.Match(http.MethodGet, "/tie/ab"); out != tieB {
		t.Error("expected pattern-length tie to be broken by config order")
	}

	// no match at all
	if out := (List{exact}).Match(http.MethodGet, "/nope"); out != nil {
		t.Errorf("expected nil, got %v", out)
	}
}

func TestListValidate(t *testing.T) {
	o := newPathOptions("^/api/.*", "")
	require.NoError(t, o.Initialize(""))
	require.NoError(t, List{nil, o}.Validate("test-backend"))

	bad := newPathOptions("/api/query", "bogus")
	err := List{bad}.Validate("test-backend")
	require.Error(t, err)
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected error to contain the invalid match_type, got %v", err)
	}
}

func TestListClone(t *testing.T) {
	o := newPathOptions("^/api/.*", "")
	require.NoError(t, o.Initialize(""))
	l := List{o}
	l2 := l.Clone()
	require.Equal(t, 1, len(l2))
	if l2[0] == o || l2[0].Regexp != o.Regexp {
		t.Error("expected a cloned Options sharing the compiled regexp pointer")
	}
}

func TestRegexShadowedByCatchAll(t *testing.T) {
	regex := newPathOptions("^/api/.*", "")
	catchAll := newPathOptions("/", matching.PathMatchNamePrefix)
	exact := newPathOptions("/api/query", "")

	l := List{regex, catchAll}
	require.NoError(t, l.Initialize())
	l = append(l, nil)
	if !l.RegexShadowedByCatchAll() {
		t.Error("expected regex + catch-all prefix to report shadowing")
	}

	l = List{regex, exact}
	require.NoError(t, l.Initialize())
	if l.RegexShadowedByCatchAll() {
		t.Error("expected no shadowing without a catch-all prefix")
	}

	l = List{catchAll, exact}
	require.NoError(t, l.Initialize())
	if l.RegexShadowedByCatchAll() {
		t.Error("expected no shadowing without a regex path")
	}
}
