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
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

func TestNew(t *testing.T) {

	pc := New()
	require.NotNil(t, pc)

	if pc.HandlerName != providers.Proxy {
		t.Errorf("expected value %s, got %s", providers.Proxy, pc.HandlerName)
	}

}

func TestPathClone(t *testing.T) {

	pc := New()
	pc2 := pc.Clone()
	require.NotNil(t, pc2)

	if pc2.HandlerName != providers.Proxy {
		t.Errorf("expected value %s, got %s", providers.Proxy, pc2.HandlerName)
	}

}

func TestInitialize(t *testing.T) {
	tests := []struct {
		name          string
		options       *Options
		expectedError error
	}{
		{
			name:          "default options",
			options:       New(),
			expectedError: nil,
		},
		{
			name: "options with custom methods",
			options: func() *Options {
				o := New()
				o.Methods = []string{"GET", "POST"}
				return o
			}(),
			expectedError: nil,
		},
		{
			name: "options with empty methods",
			options: func() *Options {
				o := New()
				o.Methods = []string{}
				return o
			}(),
			expectedError: nil,
		},
		{
			name: "options with invalid match type",
			options: func() *Options {
				o := New()
				o.MatchTypeName = "invalid"
				return o
			}(),
			expectedError: nil, // Should default to exact
		},
		{
			name: "options with invalid collapsed forwarding",
			options: func() *Options {
				o := New()
				o.CollapsedForwardingName = "invalid"
				return o
			}(),
			expectedError: nil,
		},
		{
			name: "options with response body",
			options: func() *Options {
				o := New()
				responseBody := "test response"
				o.ResponseBody = &responseBody
				return o
			}(),
			expectedError: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.options.Initialize("")

			if test.expectedError != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", test.expectedError)
					return
				}
				if err.Error() != test.expectedError.Error() {
					t.Errorf("expected error %v, got %v", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLookupInitialize(t *testing.T) {
	tests := []struct {
		name          string
		lookup        Lookup
		expectedError error
	}{
		{
			name:          "empty lookup",
			lookup:        Lookup{},
			expectedError: nil,
		},
		{
			name: "lookup with valid options",
			lookup: Lookup{
				"test": New(),
			},
			expectedError: nil,
		},
		{
			name: "lookup with invalid options",
			lookup: Lookup{
				"test": func() *Options {
					o := New()
					o.CollapsedForwardingName = "invalid"
					return o
				}(),
			},
			expectedError: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.lookup.Initialize()

			if test.expectedError != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", test.expectedError)
					return
				}
				if err.Error() != test.expectedError.Error() {
					t.Errorf("expected error %v, got %v", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name          string
		options       *Options
		expectedError string
	}{
		{
			name:          "valid options",
			options:       New(),
			expectedError: "",
		},
		{
			name: "invalid collapsed forwarding",
			options: func() *Options {
				o := New()
				o.CollapsedForwardingName = "invalid"
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "invalid collapsed_forwarding name: invalid",
		},
		{
			name: "invalid HTTP method",
			options: func() *Options {
				o := New()
				o.Methods = []string{"GET", "INVALID_METHOD"}
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "invalid HTTP method: INVALID_METHOD",
		},
		{
			name: "invalid response code",
			options: func() *Options {
				o := New()
				o.ResponseCode = 999
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "invalid response_code: 999 (must be between 100 and 599)",
		},
		{
			name: "valid response code",
			options: func() *Options {
				o := New()
				o.ResponseCode = 404
				_ = o.Initialize("")
				return o
			}(),
			expectedError: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.options.Validate()

			if test.expectedError != "" {
				if err == nil {
					t.Errorf("expected '%s', got nil", test.expectedError)
					return
				}
				if !strings.Contains(err.Error(), test.expectedError) {
					t.Errorf("expected '%s', got '%v'", test.expectedError, err)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestOverlay(t *testing.T) {
	tests := []struct {
		name     string
		l1       List
		l2       List
		expected func(t *testing.T, result List)
	}{
		{
			name: "empty lists",
			l1:   List{},
			l2:   List{},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 0)
			},
		},
		{
			name: "l1 empty, l2 has options",
			l1:   List{},
			l2: List{
				{Path: "/path1", Methods: []string{"GET"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 1)
				require.Equal(t, "/path1", result[0].Path)
				require.Equal(t, []string{"GET"}, result[0].Methods)
			},
		},
		{
			name: "l1 has options, l2 empty",
			l1: List{
				{Path: "/path1", Methods: []string{"GET"}},
			},
			l2: List{},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 1)
				require.Equal(t, "/path1", result[0].Path)
				require.Equal(t, []string{"GET"}, result[0].Methods)
			},
		},
		{
			name: "paths not in other slice - both directions",
			l1: List{
				{Path: "/path1", Methods: []string{"GET"}},
			},
			l2: List{
				{Path: "/path2", Methods: []string{"POST"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 2)
				paths := []string{result[0].Path, result[1].Path}
				require.Contains(t, paths, "/path1")
				require.Contains(t, paths, "/path2")
			},
		},
		{
			name: "same path, o2 has all of o's methods - replace",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"GET", "POST", "PUT"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 1)
				require.Equal(t, "/path1", result[0].Path)
				require.Equal(t, []string{"GET", "POST", "PUT"}, result[0].Methods)
			},
		},
		{
			name: "same path, o2 has all of o's methods - exact match",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 1)
				require.Equal(t, "/path1", result[0].Path)
				require.Equal(t, []string{"GET", "POST"}, result[0].Methods)
			},
		},
		{
			name: "same path, o2 has none of o's methods - both added",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"PUT", "DELETE"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 2)
				methods1 := result[0].Methods
				methods2 := result[1].Methods
				if len(methods1) == 2 && len(methods2) == 2 {
					if methods1[0] == "GET" || methods1[0] == "POST" {
						require.Equal(t, []string{"GET", "POST"}, methods1)
						require.Equal(t, []string{"PUT", "DELETE"}, methods2)
					} else {
						require.Equal(t, []string{"PUT", "DELETE"}, methods1)
						require.Equal(t, []string{"GET", "POST"}, methods2)
					}
				} else {
					t.Errorf("unexpected method counts: %v, %v", methods1, methods2)
				}
			},
		},
		{
			name: "same path, o2 has some of o's methods - partial overlap",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST", "PUT"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"POST", "DELETE"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 2)
				foundClone := slices.ContainsFunc(result, func(opt *Options) bool {
					return len(opt.Methods) == 2 &&
						((opt.Methods[0] == "GET" && opt.Methods[1] == "PUT") ||
							(opt.Methods[0] == "PUT" && opt.Methods[1] == "GET"))
				})
				foundO2 := slices.ContainsFunc(result, func(opt *Options) bool {
					return len(opt.Methods) == 2 &&
						((opt.Methods[0] == "POST" && opt.Methods[1] == "DELETE") ||
							(opt.Methods[0] == "DELETE" && opt.Methods[1] == "POST"))
				})
				require.True(t, foundClone, "expected clone with GET and PUT")
				require.True(t, foundO2, "expected o2 with POST and DELETE")
			},
		},
		{
			name: "same path, o has empty methods, o2 has methods",
			l1: List{
				{Path: "/path1", Methods: []string{}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 1)
				require.Equal(t, "/path1", result[0].Path)
				require.Equal(t, []string{"GET", "POST"}, result[0].Methods)
			},
		},
		{
			name: "same path, o has methods, o2 has empty methods",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 2)
				hasEmpty := false
				hasMethods := false
				for _, opt := range result {
					if len(opt.Methods) == 0 {
						hasEmpty = true
					} else if len(opt.Methods) == 2 {
						hasMethods = true
					}
				}
				require.True(t, hasEmpty, "expected option with empty methods")
				require.True(t, hasMethods, "expected option with GET and POST")
			},
		},
		{
			name: "multiple paths with various overlaps",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
				{Path: "/path2", Methods: []string{"PUT"}},
				{Path: "/path3", Methods: []string{"GET", "POST", "DELETE"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"GET", "POST", "PUT"}},
				{Path: "/path3", Methods: []string{"POST"}},
				{Path: "/path4", Methods: []string{"GET"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 5)
				pathCounts := make(map[string]int)
				for _, opt := range result {
					pathCounts[opt.Path]++
				}
				require.Equal(t, 1, pathCounts["/path1"])
				require.Equal(t, 1, pathCounts["/path2"])
				require.Equal(t, 2, pathCounts["/path3"])
				require.Equal(t, 1, pathCounts["/path4"])
			},
		},
		{
			name: "multiple options with same path in l2",
			l1: List{
				{Path: "/path1", Methods: []string{"GET", "POST"}},
			},
			l2: List{
				{Path: "/path1", Methods: []string{"PUT"}},
				{Path: "/path1", Methods: []string{"DELETE"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 4)
			},
		},
		{
			name: "nil options are skipped",
			l1: List{
				{Path: "/path1", Methods: []string{"GET"}},
				nil,
				{Path: "/path2", Methods: []string{"POST"}},
			},
			l2: List{
				{Path: "/path3", Methods: []string{"PUT"}},
				nil,
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 3)
				paths := make(map[string]bool)
				for _, opt := range result {
					if opt != nil {
						paths[opt.Path] = true
					}
				}
				require.True(t, paths["/path1"])
				require.True(t, paths["/path2"])
				require.True(t, paths["/path3"])
			},
		},
		{
			name: "complex overlap scenario",
			l1: List{
				{Path: "/api/v1", Methods: []string{"GET", "POST", "PUT", "DELETE"}},
			},
			l2: List{
				{Path: "/api/v1", Methods: []string{"GET", "POST"}},
			},
			expected: func(t *testing.T, result List) {
				require.Len(t, result, 2)
				foundClone := slices.ContainsFunc(result, func(opt *Options) bool {
					return len(opt.Methods) == 2 &&
						((opt.Methods[0] == "PUT" && opt.Methods[1] == "DELETE") ||
							(opt.Methods[0] == "DELETE" && opt.Methods[1] == "PUT"))
				})
				foundO2 := slices.ContainsFunc(result, func(opt *Options) bool {
					return len(opt.Methods) == 2 &&
						((opt.Methods[0] == "GET" && opt.Methods[1] == "POST") ||
							(opt.Methods[0] == "POST" && opt.Methods[1] == "GET"))
				})
				require.True(t, foundClone, "expected clone with PUT and DELETE")
				require.True(t, foundO2, "expected o2 with GET and POST")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.l1.Overlay(test.l2)
			test.expected(t, result)
		})
	}
}
