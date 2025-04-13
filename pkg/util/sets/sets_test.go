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

package sets

import (
	"fmt"
	"testing"
)

func TestStringSet(t *testing.T) {
	type testCase struct {
		name           string
		input          []string
		add            []string
		remove         []string
		expectContains map[string]bool
		expectSorted   []string
	}

	tests := []testCase{
		{
			name:   "basic string set behavior",
			input:  []string{"alice", "bob", "alice", "carol"},
			add:    []string{"dave"},
			remove: []string{"carol"},
			expectContains: map[string]bool{
				"bob":   true,
				"carol": false,
			},
			expectSorted: []string{"alice", "bob", "dave"},
		},
		{
			name:   "empty input with additions",
			input:  []string{},
			add:    []string{"x", "y"},
			remove: []string{},
			expectContains: map[string]bool{
				"x": true,
				"z": false,
			},
			expectSorted: []string{"x", "y"},
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d / %s", i, test.name), func(t *testing.T) {
			s := New(test.input)
			for _, val := range test.add {
				s.Add(val)
			}
			for _, val := range test.remove {
				s.Remove(val)
			}
			for key, expected := range test.expectContains {
				if s.Contains(key) != expected {
					t.Errorf("Contains(%q) = %v; want %v", key, s.Contains(key), expected)
				}
			}
			gotSorted := s.Sorted(func(a, b string) int {
				if a == b {
					return 0
				}
				if a > b {
					return 1
				}
				return -1
			})
			if !equalStrings(gotSorted, test.expectSorted) {
				t.Errorf("Sorted() = %v; want %v", gotSorted, test.expectSorted)
			}
		})
	}
}

func TestIntSet(t *testing.T) {
	type testCase struct {
		name           string
		input          []int
		add            []int
		remove         []int
		expectContains map[int]bool
		expectSorted   []int
	}

	tests := []testCase{
		{
			name:   "basic int set behavior",
			input:  []int{5, 2, 8, 5, 2},
			add:    []int{10},
			remove: []int{2},
			expectContains: map[int]bool{
				8:  true,
				2:  false,
				10: true,
			},
			expectSorted: []int{5, 8, 10},
		},
		{
			name:   "empty set plus one",
			input:  []int{},
			add:    []int{42},
			remove: []int{},
			expectContains: map[int]bool{
				42: true,
			},
			expectSorted: []int{42},
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("%d / %s", i, test.name), func(t *testing.T) {
			s := New(test.input)
			for _, val := range test.add {
				s.Add(val)
			}
			for _, val := range test.remove {
				s.Remove(val)
			}
			for key, expected := range test.expectContains {
				if s.Contains(key) != expected {
					t.Errorf("Contains(%v) = %v; want %v", key, s.Contains(key), expected)
				}
			}
			gotSorted := s.Sorted(func(a, b int) int {
				if a == b {
					return 0
				}
				if a > b {
					return 1
				}
				return -1
			})
			if !equalInts(gotSorted, test.expectSorted) {
				t.Errorf("Sorted() = %v; want %v", gotSorted, test.expectSorted)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
