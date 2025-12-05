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

package methods

import (
	"net/http"
	"testing"
)

func TestAllHTTPMethods(t *testing.T) {
	expected := 10
	l := len(AllHTTPMethods())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestGetAndPost(t *testing.T) {
	expected := 2
	l := len(GetAndPost())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestCacheableHTTPMethods(t *testing.T) {
	expected := 2
	l := len(CacheableHTTPMethods())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestUncacheableHTTPMethods(t *testing.T) {
	expected := 8
	l := len(UncacheableHTTPMethods())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestIsCacheable(t *testing.T) {
	if !IsCacheable(http.MethodGet) {
		t.Error("expected true")
	}
	if IsCacheable(http.MethodPut) {
		t.Error("expected false")
	}
	if IsCacheable("invalid_method") {
		t.Error("expected false")
	}
}

func TestHasBody(t *testing.T) {
	if HasBody(http.MethodGet) {
		t.Error("expected false")
	}
	if !HasBody(http.MethodPut) {
		t.Error("expected true")
	}
	if HasBody("invalid_method") {
		t.Error("expected false")
	}
}

func TestMethodMask(t *testing.T) {
	if v := MethodMask(http.MethodGet); v != 1 {
		t.Errorf("expected 1 got %d", v)
	}
}

func TestHasAll(t *testing.T) {
	tests := []struct {
		name     string
		methods1 []string
		methods2 []string
		expected bool
	}{
		{
			name:     "empty methods1 returns true",
			methods1: []string{},
			methods2: []string{http.MethodGet},
			expected: true,
		},
		{
			name:     "empty methods2 returns false",
			methods1: []string{http.MethodGet},
			methods2: []string{},
			expected: false,
		},
		{
			name:     "both empty",
			methods1: []string{},
			methods2: []string{},
			expected: true,
		},
		{
			name:     "exact match",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet, http.MethodPost},
			expected: true,
		},
		{
			name:     "methods2 contains all methods from methods1",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet, http.MethodPost, http.MethodPut},
			expected: true,
		},
		{
			name:     "methods2 contains some but not all methods from methods1",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet},
			expected: false,
		},
		{
			name:     "methods2 contains none of methods1",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodPut, http.MethodDelete},
			expected: false,
		},
		{
			name:     "single method match",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodGet},
			expected: true,
		},
		{
			name:     "single method no match",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodPost},
			expected: false,
		},
		{
			name:     "methods2 has all plus more",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodGet, http.MethodPost, http.MethodPut},
			expected: true,
		},
		{
			name:     "duplicate methods in methods1 (XOR cancels duplicates, mask becomes 0)",
			methods1: []string{http.MethodGet, http.MethodGet},
			methods2: []string{http.MethodGet},
			expected: true,
		},
		{
			name:     "duplicate methods in methods2 (XOR cancels duplicates, mask becomes 0)",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodGet, http.MethodGet},
			expected: false,
		},
		{
			name:     "multiple methods with partial overlap",
			methods1: []string{http.MethodGet, http.MethodPost, http.MethodPut},
			methods2: []string{http.MethodGet, http.MethodPost},
			expected: false,
		},
		{
			name:     "case insensitive - all match",
			methods1: []string{"get", "post"},
			methods2: []string{http.MethodGet, http.MethodPost},
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := HasAll(test.methods1, test.methods2)
			if result != test.expected {
				t.Errorf("HasAll(%v, %v) = %v, expected %v", test.methods1, test.methods2, result, test.expected)
			}
		})
	}
}

func TestHasAny(t *testing.T) {
	tests := []struct {
		name     string
		methods1 []string
		methods2 []string
		expected bool
	}{
		{
			name:     "empty methods1 returns false",
			methods1: []string{},
			methods2: []string{http.MethodGet},
			expected: false,
		},
		{
			name:     "empty methods2 returns false",
			methods1: []string{http.MethodGet},
			methods2: []string{},
			expected: false,
		},
		{
			name:     "both empty",
			methods1: []string{},
			methods2: []string{},
			expected: false,
		},
		{
			name:     "exact match",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet, http.MethodPost},
			expected: true,
		},
		{
			name:     "methods2 contains all methods from methods1",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet, http.MethodPost, http.MethodPut},
			expected: true,
		},
		{
			name:     "methods2 contains some methods from methods1",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet},
			expected: true,
		},
		{
			name:     "methods2 contains none of methods1",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodPut, http.MethodDelete},
			expected: false,
		},
		{
			name:     "single method match",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodGet},
			expected: true,
		},
		{
			name:     "single method no match",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodPost},
			expected: false,
		},
		{
			name:     "partial overlap - one method",
			methods1: []string{http.MethodGet, http.MethodPost},
			methods2: []string{http.MethodGet, http.MethodPut},
			expected: true,
		},
		{
			name:     "partial overlap - multiple methods",
			methods1: []string{http.MethodGet, http.MethodPost, http.MethodPut},
			methods2: []string{http.MethodGet, http.MethodPost},
			expected: true,
		},
		{
			name:     "duplicate methods in methods1 (XOR cancels duplicates, mask becomes 0)",
			methods1: []string{http.MethodGet, http.MethodGet},
			methods2: []string{http.MethodGet},
			expected: false,
		},
		{
			name:     "duplicate methods in methods2 (XOR cancels duplicates, mask becomes 0)",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodGet, http.MethodGet},
			expected: false,
		},
		{
			name:     "case insensitive - any match",
			methods1: []string{"get", "post"},
			methods2: []string{http.MethodGet, http.MethodPut},
			expected: true,
		},
		{
			name:     "methods1 has more methods, methods2 has one match",
			methods1: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete},
			methods2: []string{http.MethodPost},
			expected: true,
		},
		{
			name:     "methods2 has more methods, methods1 has one match",
			methods1: []string{http.MethodGet},
			methods2: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete},
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := HasAny(test.methods1, test.methods2)
			if result != test.expected {
				t.Errorf("HasAny(%v, %v) = %v, expected %v", test.methods1, test.methods2, result, test.expected)
			}
		})
	}
}

func TestIsValidMethod(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		expected bool
	}{
		{
			name:     "GET is valid",
			method:   http.MethodGet,
			expected: true,
		},
		{
			name:     "HEAD is valid",
			method:   http.MethodHead,
			expected: true,
		},
		{
			name:     "POST is valid",
			method:   http.MethodPost,
			expected: true,
		},
		{
			name:     "PUT is valid",
			method:   http.MethodPut,
			expected: true,
		},
		{
			name:     "PATCH is valid",
			method:   http.MethodPatch,
			expected: true,
		},
		{
			name:     "DELETE is valid",
			method:   http.MethodDelete,
			expected: true,
		},
		{
			name:     "OPTIONS is valid",
			method:   http.MethodOptions,
			expected: true,
		},
		{
			name:     "CONNECT is valid",
			method:   http.MethodConnect,
			expected: true,
		},
		{
			name:     "TRACE is valid",
			method:   http.MethodTrace,
			expected: true,
		},
		{
			name:     "PURGE is valid",
			method:   MethodPurge,
			expected: true,
		},
		{
			name:     "case insensitive - lowercase get",
			method:   "get",
			expected: true,
		},
		{
			name:     "case insensitive - uppercase GET",
			method:   "GET",
			expected: true,
		},
		{
			name:     "case insensitive - mixed case Get",
			method:   "Get",
			expected: true,
		},
		{
			name:     "empty string is invalid",
			method:   "",
			expected: false,
		},
		{
			name:     "invalid method name",
			method:   "INVALID_METHOD",
			expected: false,
		},
		{
			name:     "random string is invalid",
			method:   "random",
			expected: false,
		},
		{
			name:     "numeric string is invalid",
			method:   "123",
			expected: false,
		},
		{
			name:     "similar but invalid - GOT",
			method:   "GOT",
			expected: false,
		},
		{
			name:     "similar but invalid - PUSH",
			method:   "PUSH",
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := IsValidMethod(test.method)
			if result != test.expected {
				t.Errorf("IsValidMethod(%q) = %v, expected %v", test.method, result, test.expected)
			}
		})
	}
}

func TestAreEqual(t *testing.T) {
	tests := []struct {
		name     string
		l1       []string
		l2       []string
		expected bool
	}{
		{
			name:     "empty lists are equal",
			l1:       []string{},
			l2:       []string{},
			expected: true,
		},
		{
			name:     "exact match single method",
			l1:       []string{http.MethodGet},
			l2:       []string{http.MethodGet},
			expected: true,
		},
		{
			name:     "exact match multiple methods same order",
			l1:       []string{http.MethodGet, http.MethodPost},
			l2:       []string{http.MethodGet, http.MethodPost},
			expected: true,
		},
		{
			name:     "exact match multiple methods different order",
			l1:       []string{http.MethodGet, http.MethodPost},
			l2:       []string{http.MethodPost, http.MethodGet},
			expected: true,
		},
		{
			name:     "different lengths",
			l1:       []string{http.MethodGet},
			l2:       []string{http.MethodGet, http.MethodPost},
			expected: false,
		},
		{
			name:     "same length different methods",
			l1:       []string{http.MethodGet, http.MethodPost},
			l2:       []string{http.MethodGet, http.MethodPut},
			expected: false,
		},
		{
			name:     "same length no overlap",
			l1:       []string{http.MethodGet, http.MethodPost},
			l2:       []string{http.MethodPut, http.MethodDelete},
			expected: false,
		},
		{
			name:     "duplicate methods in l1 (XOR cancels, but length check fails first)",
			l1:       []string{http.MethodGet, http.MethodGet},
			l2:       []string{},
			expected: false,
		},
		{
			name:     "duplicate methods in l2 (XOR cancels, but length check fails first)",
			l1:       []string{},
			l2:       []string{http.MethodGet, http.MethodGet},
			expected: false,
		},
		{
			name:     "duplicate methods in both (XOR cancels)",
			l1:       []string{http.MethodGet, http.MethodGet},
			l2:       []string{http.MethodPost, http.MethodPost},
			expected: true,
		},
		{
			name:     "three methods same order",
			l1:       []string{http.MethodGet, http.MethodPost, http.MethodPut},
			l2:       []string{http.MethodGet, http.MethodPost, http.MethodPut},
			expected: true,
		},
		{
			name:     "three methods different order",
			l1:       []string{http.MethodGet, http.MethodPost, http.MethodPut},
			l2:       []string{http.MethodPut, http.MethodGet, http.MethodPost},
			expected: true,
		},
		{
			name:     "case insensitive match",
			l1:       []string{"get", "post"},
			l2:       []string{http.MethodGet, http.MethodPost},
			expected: true,
		},
		{
			name:     "case insensitive different order",
			l1:       []string{"GET", "POST"},
			l2:       []string{http.MethodPost, http.MethodGet},
			expected: true,
		},
		{
			name:     "single method mismatch",
			l1:       []string{http.MethodGet},
			l2:       []string{http.MethodPost},
			expected: false,
		},
		{
			name:     "multiple methods one different",
			l1:       []string{http.MethodGet, http.MethodPost, http.MethodPut},
			l2:       []string{http.MethodGet, http.MethodPost, http.MethodDelete},
			expected: false,
		},
		{
			name:     "all HTTP methods",
			l1:       AllHTTPMethods(),
			l2:       AllHTTPMethods(),
			expected: true,
		},
		{
			name: "all HTTP methods different order",
			l1:   AllHTTPMethods(),
			l2: func() []string {
				all := AllHTTPMethods()
				reversed := make([]string, len(all))
				for i := range all {
					reversed[i] = all[len(all)-1-i]
				}
				return reversed
			}(),
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := AreEqual(test.l1, test.l2)
			if result != test.expected {
				t.Errorf("AreEqual(%v, %v) = %v, expected %v", test.l1, test.l2, result, test.expected)
			}
		})
	}
}
