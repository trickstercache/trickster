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

func TestIndexOfString(t *testing.T) {

	arr := []string{"string0", "string1", "string2"}

	i := IndexInSlice(arr, "string0")
	if i != 0 {
		t.Errorf(`expected 0. got %d`, i)
	}

	i = IndexInSlice(arr, "string3")
	if i != -1 {
		t.Errorf(`expected -1. got %d`, i)
	}

}

func TestEqual(t *testing.T) {

	l1 := []string{"test1", "test2"}
	l2 := []string{"test1", "test2"}
	l3 := []string{"test3", "test4"}
	l4 := []string{}

	if !Equal(l1, l2) {
		t.Error("expected true got false")
	}

	if Equal(l1, l3) {
		t.Error("expected false got true")
	}

	if Equal(l1, l4) {
		t.Error("expected false got true")
	}

	if !Equal(nil, nil) {
		t.Error("expected true got false")
	}
}

func TestStringMap(t *testing.T) {

	sm := StringMap(map[string]string{"test": "value"})
	s := sm.String()
	const expected = `{"test":"value"}`

	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}

func TestCloneBoolMap(t *testing.T) {

	m1 := CloneBoolMap(nil)
	if m1 != nil {
		t.Error("expected nil map")
	}

	const expected = true

	m := map[string]bool{"test": expected}
	m2 := CloneBoolMap(m)

	v, ok := m2["test"]
	if !ok {
		t.Errorf("expected true got %t", ok)
	}

	if v != expected {
		t.Errorf("expected %t got %t", expected, v)
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

func TestGetInt(t *testing.T) {

	m := StringMap{"trickster": "proxy", "test": "1"}

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
