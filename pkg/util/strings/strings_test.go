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

package strings

import (
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
)

func TestSubstring(t *testing.T) {
	str := "hello world"
	if got := Substring(str, 0, 2); got != "he" {
		t.Errorf("Expected 'he', got %s", got)
	}
	if got := Substring(str, 4, 3); got != "o w" {
		t.Errorf("Expected 'o w', got %s", got)
	}
	if got := Substring(str, 9, 3); got != "" {
		t.Errorf("Expected Substring(str, 9, 3) to overflow and return '', got %s", got)
	}
}

func TestMap(t *testing.T) {
	sm := Map(map[string]string{"test": "value"})
	s := sm.String()
	const expected = `{"test":"value"}`

	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}

func TestUnique(t *testing.T) {
	initial := []string{"test", "test", "test1", "test2", "test2", "test", "test3"}
	expected := "test,test1,test2,test3"
	after := strings.Join(Unique(initial), ",")
	if expected != after {
		t.Errorf("expected %s got %s", expected, after)
	}

	empty := Unique(nil)
	if len(empty) != 0 {
		t.Error("expected empty list")
	}
}

func BenchmarkUnique(b *testing.B) {
	initial := []string{"test", "test", "test1", "test2", "test2", "test", "test3"}
	for i := 0; i < b.N; i++ {
		Unique(initial)
	}
}

func TestGetInt(t *testing.T) {
	m := Map{"trickster": providers.Proxy, "test": "1"}

	if _, err := m.GetInt("invalid"); err != ErrKeyNotInMap {
		t.Error("expected err for Key Not In Map", err)
	}

	if _, err := m.GetInt("trickster"); err == nil {
		t.Error("expected err for invalid conversion", err)
	}
	i, err := m.GetInt("test")
	if err != nil {
		t.Error(err)
	}
	if i != 1 {
		t.Errorf("expected %d got %d", 1, i)
	}
}

func TestEscapeQuotes(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{`"test"`, `\"test\"`},
		{`say "hi"`, `say \"hi\"`},
		{`\\"x\\"`, `\\"x\\"`},
		{`already\"escaped`, `already\"escaped`},
	}
	for _, c := range cases {
		if got := EscapeQuotes(c.input); got != c.want {
			t.Errorf("EscapeQuotes(%q) = %q; want %q", c.input, got, c.want)
		}
	}
}

func TestIsApparentSQLDateFormat(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"2024-01-15 12:30:45", true},
		{"3928-31-28 88:73:95", true},
		{"2024-01-15", false},
		{"2024_01-15 12:30:45", false},
		{"2024-01-15 12:3a:45", false},
		{"2024-01-15T12:30:45", false},
		{"2024-01-15 12-30:45", false},
		{"2024-01-15 12:30-45", false},
	}
	for _, c := range cases {
		if got := IsApparentSQLDateFormat(c.input); got != c.want {
			t.Errorf("IsApparentSQLDateFormat(%q) = %t; want %t", c.input, got, c.want)
		}
	}
}

func TestPare(t *testing.T) {
	cases := []struct {
		name    string
		s       []string
		exclude []string
		want    []string
	}{
		{
			name:    "filters excluded values",
			s:       []string{"GET", "POST", "PUT", "DELETE"},
			exclude: []string{"POST", "DELETE"},
			want:    []string{"GET", "PUT"},
		},
		{
			name:    "empty exclude returns input order",
			s:       []string{"a", "b", "c"},
			exclude: nil,
			want:    []string{"a", "b", "c"},
		},
		{
			name:    "empty input",
			s:       nil,
			exclude: []string{"a"},
			want:    nil,
		},
		{
			name:    "all excluded",
			s:       []string{"a", "b"},
			exclude: []string{"a", "b"},
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Pare(c.s, c.exclude)
			if !stringSlicesEqual(got, c.want) {
				t.Errorf("Pare(%v, %v) = %v; want %v", c.s, c.exclude, got, c.want)
			}
		})
	}
}

func stringSlicesEqual(a, b []string) bool {
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
