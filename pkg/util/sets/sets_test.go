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
	"math"
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
				s.Set(val)
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
				s.Set(val)
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

func TestMapKeysToStringSet(t *testing.T) {
	if got := MapKeysToStringSet(map[string]int(nil)); got != nil {
		t.Errorf("MapKeysToStringSet(nil) = %v; want nil", got)
	}

	input := map[string]int{"a": 1, "b": 2}
	got := MapKeysToStringSet(input)
	if len(got) != 2 || !got.Contains("a") || !got.Contains("b") {
		t.Errorf("MapKeysToStringSet() = %v; want keys a and b", got.Keys())
	}
}

func TestSetConstructors(t *testing.T) {
	if len(NewInt64Set()) != 0 {
		t.Error("expected empty NewInt64Set")
	}
	if len(NewIntSet()) != 0 {
		t.Error("expected empty NewIntSet")
	}
	if len(NewStringSet()) != 0 {
		t.Error("expected empty NewStringSet")
	}
	if len(NewByteSet()) != 0 {
		t.Error("expected empty NewByteSet")
	}
}

func TestSetAddCloneMerge(t *testing.T) {
	s := New([]string{"a"})
	if !s.Add("b") {
		t.Error("expected Add to return true for new value")
	}
	if s.Add("b") {
		t.Error("expected Add to return false for existing value")
	}

	clone := s.Clone()
	clone.Remove("a")
	if !s.Contains("a") {
		t.Error("expected original set to be unchanged after clone modification")
	}

	other := New([]string{"c", "d"})
	s.Merge(other)
	for _, key := range []string{"a", "b", "c", "d"} {
		if !s.Contains(key) {
			t.Errorf("expected merged set to contain %q", key)
		}
	}
}

func TestCounterSet(t *testing.T) {
	cs := NewStringCounterSet()

	old, newVal, existed := cs.Increment("hits", 3)
	if old != 0 || newVal != 3 || existed {
		t.Errorf("new key Increment() = (%d, %d, %t); want (0, 3, false)", old, newVal, existed)
	}

	old, newVal, existed = cs.Increment("hits", 2)
	if old != 3 || newVal != 5 || !existed {
		t.Errorf("existing key Increment() = (%d, %d, %t); want (3, 5, true)", old, newVal, existed)
	}

	old, newVal, existed = cs.Increment("hits", -2)
	if old != 5 || newVal != 3 || !existed {
		t.Errorf("decrement Increment() = (%d, %d, %t); want (5, 3, true)", old, newVal, existed)
	}

	key := "overflow"
	cs[key] = math.MaxInt
	old, newVal, existed = cs.Increment(key, 1)
	if old != math.MaxInt || newVal != math.MaxInt || !existed {
		t.Errorf("overflow Increment() = (%d, %d, %t); want (%d, %d, true)",
			old, newVal, existed, math.MaxInt, math.MaxInt)
	}

	cs.Reset("hits")
	if val, ok := cs.Value("hits"); !ok || val != 0 {
		t.Errorf("Reset() value = (%d, %t); want (0, true)", val, ok)
	}

	cs.Reset("new")
	if val, ok := cs.Value("new"); !ok || val != 0 {
		t.Errorf("Reset() on missing key = (%d, %t); want (0, true)", val, ok)
	}

	if val, ok := cs.Value("missing"); ok || val != -1 {
		t.Errorf("Value() on missing key = (%d, %t); want (-1, false)", val, ok)
	}
}

func TestStringCounterSetCap(t *testing.T) {
	cs := NewStringCounterSetCap(4)
	if cs == nil {
		t.Fatal("expected non-nil counter set")
	}
	cs.Increment("a", 1)
	if val, ok := cs.Value("a"); !ok || val != 1 {
		t.Errorf("Value() = (%d, %t); want (1, true)", val, ok)
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
